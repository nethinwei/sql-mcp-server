<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import {
  NAlert, NButton, NCard, NCollapse, NCollapseItem, NEmpty, NInput, NSpace, NSpin, NTag, NText, useDialog,
  useMessage,
} from 'naive-ui'
import DiffView from '@/components/DiffView.vue'
import ChangeSummary from '@/components/ChangeSummary.vue'
import ConflictList from '@/components/ConflictList.vue'
import { summarize, type Config } from '@/lib/changeSummary'
import { currentLifetime, owned, useWorkspace, WorkspaceSwitched } from '@/stores/workspace'
import { useWorkspaceAction, workspace } from '@/lib/workspaceSync'
import { ApiError, run } from '@/api/client'
import { useStatus } from '@/stores/status'
import { useSettingsEdits } from '@/stores/settingsEdits'
import { DiffQuery, ValidateQuery, WorkspaceLogQuery } from '@/api/ops'
import { draftsChanged } from '@/lib/draftFeed'
import { can } from '@/api/session'
import { reasonText, type ReasonLike } from '@/lib/reasons'

const { t } = useI18n()
const status = useStatus()
const ws = useWorkspace()
const settingsEdits = useSettingsEdits()
const router = useRouter()
const dialog = useDialog()
const message = useMessage()
const action = useWorkspaceAction()
const checking = ref(false)
const saving = ref(false)
const error = ref<string | null>(null)
const restart = ref<string[]>([])
const warnings = ref<ReasonLike[]>([])
const diff = ref('')
const comment = ref('')

// The comment starts as what the agents and drafts behind the changes said
// (see Note), and follows them until edited here.
const notesText = computed(() => ws.notes
  .map((n) => t(n.source === 'agent' ? 'review.noteAgent' : 'review.noteDraft', { text: n.text.trim(), by: n.by }))
  .join(t('review.noteSep')))
let prefilled = ''
watch(notesText, (text) => {
  if (comment.value === prefilled) comment.value = text
  prefilled = text
}, { immediate: true })

// Who last wrote each changed item, from the workspace log: an
// administrator, an agent, or a draft taken in.
const writers = ref(new Map<string, string>())
let writersSeq = 0
watch(() => ws.version, async () => {
  const mine = ++writersSeq
  try {
    const { workspaceLog } = await run(WorkspaceLogQuery, { limit: 200 })
    if (mine !== writersSeq) return
    const out = new Map<string, string>()
    for (const e of workspaceLog) {
      const by = e.op === 'merge' ? t('review.byDraft', { id: (e.note ?? '').replace(/^#/, '') })
        : e.by.startsWith('agent:') ? t('review.byAgent', { agent: e.by.slice('agent:'.length) }) : e.by
      for (const it of e.items) if (!out.has(`${it.kind}/${it.key}`)) out.set(`${it.kind}/${it.key}`, by)
    }
    writers.value = out
  } catch {
    // the log only annotates the changes
  }
}, { immediate: true })
function writer(c: { kind: string; name: string }) {
  return writers.value.get(`${c.kind}/${c.name}`) ?? ''
}

const restartList = () => restart.value.join(t('common.listSep'))
const summary = computed(() => summarize(JSON.parse(ws.baseline) as Config, ws.$state))

// Validation, restart flags and the diff describe the content they were
// computed for. Any change to the workspace (here, in the header change bar or
// another tab) re-checks shortly after; saving re-checks first when stale.
const checkedContent = ref<string | null>(null)
const checkStale = computed(() => checkedContent.value !== ws.content)
let seq = 0

async function check() {
  if (!ws.baseId) return
  const snap = ws.snapshot()
  const mine = ++seq
  checking.value = true
  try {
    // Both read the workspace on the server, what was edited here written first.
    const v = (await workspace.read(ValidateQuery, {})).validate
    const d = v.ok ? (await workspace.read(DiffQuery, { from: ws.baseId })).diff : ''
    if (mine !== seq) return
    error.value = v.ok ? null : v.error ?? t('review.validateFailed')
    restart.value = v.restartRequired
    warnings.value = v.notices
    diff.value = d
    checkedContent.value = snap.content
  } catch (e) {
    if (mine !== seq) return
    // A failed check is the result for this content: shown with a recheck
    // button instead of waiting for a recheck no edit will trigger.
    error.value = e instanceof Error ? e.message : String(e)
    checkedContent.value = snap.content
  } finally {
    if (mine === seq) checking.value = false
  }
}

let recheck: ReturnType<typeof setTimeout> | undefined
watch(() => ws.content, () => {
  clearTimeout(recheck)
  if (ws.dirty) recheck = setTimeout(() => void check(), 800)
})
onBeforeUnmount(() => clearTimeout(recheck))

/** Offers to update when someone published since the workspace was based. */
function conflict(id: string) {
  dialog.warning({
    title: t('stale.publishConflictTitle', { id }),
    content: t('stale.publishConflictBody', { id }),
    positiveText: t('stale.rebase', { id }),
    negativeText: t('common.cancel'),
    onPositiveClick: async () => {
      const count = await action(() => status.rebaseWorkspace())
      if (count === undefined) return
      message.success(t('stale.rebased', { id, count }, count))
      await check()
    },
  })
}

const blocked = computed(() => Boolean(error.value) || ws.openConflicts.length > 0)

/**
 * Saves the workspace as a draft, or publishes it, on the server: what is
 * edited here is written first and checked as the server keeps it; the
 * server publishes the workspace at that version, so a write made since
 * (another console, an agent) is checked first.
 */
async function save(publish: boolean) {
  if (ws.openConflicts.length) return
  // Every request of this save is the signed-in administrator's: once
  // another signs in, none is made and no answer is taken (WorkspaceSwitched).
  const at = currentLifetime()
  saving.value = true
  try {
    await owned(() => workspace.flush(), at)
    // Validation must describe what is submitted.
    if (checkStale.value) await owned(check, at)
    if (error.value || checkStale.value || ws.openConflicts.length) return
    if (!publish) {
      const id = await owned(() => workspace.checkpoint(comment.value || null), at)
      draftsChanged.value++
      message.success(t('review.savedDraft', { id }))
      return
    }
    const expectedPublished = ws.basePublished
    if (!expectedPublished) return
    const restartNeeded = restart.value.length > 0
    const id = await owned(() => workspace.commit({
      version: ws.version, expectedPublished, comment: comment.value || null,
      restartRequired: restartNeeded,
    }), at)
    void status.refresh()
    draftsChanged.value++
    message.success(t(restartNeeded ? 'review.publishedRestart' : 'review.published', { id }))
    void router.push({ name: 'revisions', query: { id } })
  } catch (e) {
    if (e instanceof WorkspaceSwitched) return
    if (e instanceof ApiError) {
      if (e.code === 'NOT_MOVED' && e.extensions.published) {
        // Published all the same, the workspace left on the revision before:
        // it is moved on from here, or the administrator is told to.
        const id = String(e.extensions.published)
        void status.refresh()
        draftsChanged.value++
        let moved = false
        try {
          moved = await owned(() => workspace.follow(id), at)
        } catch (err) {
          if (err instanceof WorkspaceSwitched) return
        }
        if (moved) message.success(t(restart.value.length ? 'review.publishedRestart' : 'review.published', { id }))
        else message.warning(t('review.publishedNotMoved', { id }))
        void router.push({ name: 'revisions', query: { id } })
        return
      }
      if (e.code === 'CONFLICT' && e.extensions.published) return conflict(String(e.extensions.published))
      if (e.code === 'BEHIND') {
        await status.refresh()
        if (status.publishedId) return conflict(status.publishedId)
      }
      if (e.code === 'CONFLICT' || e.code === 'CONFLICTS') {
        // Written meanwhile: what it is now is read again and checked.
        await workspace.refresh()
        message.warning(t('review.changedMeanwhile'))
        return
      }
    }
    message.error(e instanceof Error ? e.message : String(e))
  } finally {
    saving.value = false
  }
}

function publish() {
  if (!restart.value.length) return void save(true)
  dialog.warning({
    title: t('review.confirmRestartTitle'),
    content: t('review.confirmRestartBody', { list: restartList() }),
    positiveText: t('review.publishAnyway'),
    negativeText: t('common.cancel'),
    onPositiveClick: () => save(true),
  })
}

onMounted(check)
</script>

<template>
  <n-alert v-if="settingsEdits.unsaved.length" type="warning" class="settings-pending"
    :title="t('review.settingsPendingTitle')">
    {{ t('review.settingsPendingBody', { list: settingsEdits.unsaved.join(', ') }) }}
    <div class="alert-actions">
      <n-button size="small" @click="router.push({ name: 'settings' })">{{ t('review.toSettings') }}</n-button>
    </div>
  </n-alert>
  <n-empty v-if="!ws.dirty && !ws.openConflicts.length" :description="t('review.nothing')">
    <template #extra><n-button @click="router.push({ name: 'overview' })">{{ t('review.backToOverview') }}</n-button></template>
  </n-empty>
  <n-spin v-else :show="checking">
    <n-space vertical :size="16">
      <n-card size="small" :title="t('review.pending', { count: ws.changes.length })">
        <n-space :size="8">
          <n-tag v-for="c in ws.changes" :key="c.kind + c.name" size="small" :bordered="false"
            :closable="can('admin:write')" @close="ws.revert(c)"
            :type="c.type === 'added' ? 'success' : c.type === 'removed' ? 'error' : 'warning'">
            {{ t(`changes.type.${c.type}`) }} · {{ t(`changes.kind.${c.kind}`) }} · {{ c.kind === 'settings' ? t('changes.settingsName') : c.kind === 'entity' ? ws.entityIndex.shortName(c.name) : c.name }}<template v-if="writer(c)"> · {{ writer(c) }}</template>
          </n-tag>
        </n-space>
      </n-card>

      <conflict-list v-if="ws.openConflicts.length" />

      <n-alert v-if="checkStale" type="info" :show-icon="false">{{ t('review.rechecking') }}</n-alert>
      <n-alert v-else-if="error" type="error" :title="t('review.invalid')">
        <pre class="mono" style="white-space: pre-wrap; margin: 0">{{ error }}</pre>
        <div class="alert-actions"><n-button size="small" @click="check">{{ t('review.recheck') }}</n-button></div>
      </n-alert>
      <n-alert v-else-if="!checking && restart.length" type="warning" :title="t('review.restartTitle')">
        {{ t('review.restartBody', { list: restartList() }) }}
      </n-alert>
      <n-alert v-else-if="!checking" type="success" :title="t('review.okTitle')">
        {{ t('review.okBody') }}
      </n-alert>

      <n-alert v-if="!checkStale && !error && !checking && warnings.length" type="warning"
        :title="t('review.capabilityTitle')">
        {{ t('review.capabilityBody') }}
        <ul class="warnings"><li v-for="w in warnings" :key="w.text">{{ reasonText(w) }}</li></ul>
      </n-alert>

      <n-card size="small" :title="t('review.summaryTitle')">
        <change-summary :summary="summary" />
      </n-card>

      <n-card v-if="!error" size="small">
        <n-collapse>
          <n-collapse-item :title="t('review.diffTitle', { id: ws.baseId })" name="diff">
            <diff-view :diff="diff" />
          </n-collapse-item>
        </n-collapse>
      </n-card>

      <n-alert v-if="ws.savedDraftId" type="info" :title="t('review.savedDraftTitle', { id: ws.savedDraftId })">
        {{ t('review.savedDraftBody') }}
        <div class="alert-actions">
          <n-button size="small" @click="router.push({ name: 'revisions', query: { id: ws.savedDraftId } })">
            {{ t('review.viewDraft') }}
          </n-button>
        </div>
      </n-alert>

      <n-card size="small">
        <n-space vertical>
          <n-input v-model:value="comment" type="textarea" :autosize="{ minRows: 1, maxRows: 4 }"
            :placeholder="t('review.comment')"
            :disabled="Boolean(error) || Boolean(ws.savedDraftId)" />
          <n-space justify="end">
            <n-text v-if="!can('admin:publish')" depth="3" style="font-size: 12px">{{ t('review.noPublish') }}</n-text>
            <n-button :disabled="blocked || Boolean(ws.savedDraftId)" :loading="saving"
              @click="save(false)">
              {{ ws.savedDraftId ? t('review.draftSaved', { id: ws.savedDraftId }) : t('review.saveDraft') }}
            </n-button>
            <n-button v-if="can('admin:publish')" type="primary" :disabled="blocked"
              :loading="saving" @click="publish">
              {{ ws.savedDraftId ? t('review.publishDraft', { id: ws.savedDraftId }) : t('review.saveAndPublish') }}
            </n-button>
          </n-space>
        </n-space>
      </n-card>
    </n-space>
  </n-spin>
</template>

<style scoped>
.settings-pending { margin-bottom: 16px; }
.warnings { margin: 8px 0 0; padding-left: 20px; }
</style>
