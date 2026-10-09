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
import { summarize, type Config } from '@/lib/changeSummary'
import { useWorkspace } from '@/stores/workspace'
import { ApiError, run } from '@/api/client'
import { useStatus } from '@/stores/status'
import { useSettingsEdits } from '@/stores/settingsEdits'
import { CreateDraftMutation, DiffQuery, PublishMutation, ValidateQuery } from '@/api/ops'
import type { DraftInput } from '@/gql/graphql'
import { can } from '@/api/session'

const { t } = useI18n()
const status = useStatus()
const ws = useWorkspace()
const settingsEdits = useSettingsEdits()
const router = useRouter()
const dialog = useDialog()
const message = useMessage()
const checking = ref(false)
const saving = ref(false)
const error = ref<string | null>(null)
const restart = ref<string[]>([])
const warnings = ref<string[]>([])
const diff = ref('')
const comment = ref('')

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
    const v = (await run(ValidateQuery, { draft: snap.draft })).validate
    const d = v.ok ? (await run(DiffQuery, { from: ws.baseId, draft: snap.draft })).diff : ''
    if (mine !== seq) return
    error.value = v.ok ? null : v.error ?? t('review.validateFailed')
    restart.value = v.restartRequired
    warnings.value = v.warnings
    diff.value = d
    checkedContent.value = snap.content
  } catch (e) {
    if (mine === seq) error.value = e instanceof Error ? e.message : String(e)
  } finally {
    if (mine === seq) checking.value = false
  }
}

let recheck: ReturnType<typeof setTimeout> | undefined
watch(() => ws.content, () => {
  clearTimeout(recheck)
  if (ws.dirty) recheck = setTimeout(() => void check(), 400)
})
onBeforeUnmount(() => clearTimeout(recheck))

async function saveDraft(draft: DraftInput) {
  const { createDraft } = await run(CreateDraftMutation, { draft, comment: comment.value || null }, true)
  return createDraft
}

/** Offers to rebase when someone published since the workspace was based. */
function conflict(id: string) {
  dialog.warning({
    title: t('stale.publishConflictTitle', { id }),
    content: t('stale.publishConflictBody', { id }),
    positiveText: t('stale.rebase', { id }),
    negativeText: t('common.cancel'),
    onPositiveClick: async () => {
      const count = await status.rebaseWorkspace()
      message.success(t('stale.rebased', { id, count }, count))
      await check()
    },
  })
}

/**
 * Publishing and syncing the workspace afterwards are separate outcomes: once
 * the server published, a failed sync keeps the published id and the
 * submitted snapshot so it can be retried, and publishing is not offered
 * again (the workspace is still based on the older revision).
 */
const syncFailed = ref<{ id: string; sections: string; restart: boolean; error: string } | null>(null)

async function sync(id: string, sections: string, restartNeeded: boolean) {
  try {
    // Edits made since the click (including undoing part of it) are measured
    // from the snapshot when the published configuration arrives, and kept.
    await ws.rebase(id, sections)
    syncFailed.value = null
    message.success(t(restartNeeded ? 'review.publishedRestart' : 'review.published', { id }))
    void router.push({ name: 'revisions', query: { id } })
  } catch (e) {
    syncFailed.value = { id, sections, restart: restartNeeded, error: e instanceof Error ? e.message : String(e) }
  }
}

async function save(publish: boolean) {
  saving.value = true
  try {
    // Validation must describe what is submitted.
    if (checkStale.value) await check()
    if (error.value || checkStale.value) return
    // What is saved is the workspace at this point; edits made while the
    // requests run stay unsaved (and survive publishing, re-applied on top).
    const snap = ws.snapshot()
    const reuse = ws.savedDraftId
    const restartNeeded = restart.value.length > 0
    if (publish) {
      await status.refresh()
      if (status.publishedId && status.publishedId !== ws.basePublished) return conflict(status.publishedId)
    }
    // A draft already holding this content is published as is.
    const id = reuse ?? (await saveDraft(snap.draft)).id
    if (!publish) {
      ws.markDraftSaved(id, snap.content)
      message.success(t('review.savedDraft', { id }))
      return
    }
    await run(PublishMutation, {
      input: { id, restartRequired: restartNeeded, expectedPublished: ws.basePublished },
    }, true)
    void status.refresh()
    await sync(id, snap.sections, restartNeeded)
  } catch (e) {
    if (e instanceof ApiError && e.code === 'CONFLICT') return conflict(String(e.extensions.published))
    message.error(e instanceof Error ? e.message : String(e))
  } finally {
    saving.value = false
  }
}

async function resync() {
  const f = syncFailed.value
  if (!f) return
  saving.value = true
  try {
    await sync(f.id, f.sections, f.restart)
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
  <n-alert v-if="settingsEdits.pending.length" type="warning" class="settings-pending"
    :title="t('review.settingsPendingTitle')">
    {{ t('review.settingsPendingBody', { list: settingsEdits.pending.join(', ') }) }}
    <div class="alert-actions">
      <n-button size="small" @click="router.push({ name: 'settings' })">{{ t('review.toSettings') }}</n-button>
    </div>
  </n-alert>
  <n-empty v-if="!ws.dirty" :description="t('review.nothing')">
    <template #extra><n-button @click="router.push({ name: 'overview' })">{{ t('review.backToOverview') }}</n-button></template>
  </n-empty>
  <n-spin v-else :show="checking">
    <n-space vertical :size="16">
      <n-card size="small" :title="t('review.pending', { count: ws.changes.length })">
        <n-space :size="8">
          <n-tag v-for="c in ws.changes" :key="c.kind + c.name" size="small" :bordered="false"
            :closable="can('admin:write')" @close="ws.revert(c)"
            :type="c.type === 'added' ? 'success' : c.type === 'removed' ? 'error' : 'warning'">
            {{ t(`changes.type.${c.type}`) }} · {{ t(`changes.kind.${c.kind}`) }} · {{ c.kind === 'settings' ? t('changes.settingsName') : c.kind === 'entity' ? ws.entityIndex.shortName(c.name) : c.name }}
          </n-tag>
        </n-space>
      </n-card>

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
        <ul class="warnings"><li v-for="w in warnings" :key="w" class="mono">{{ w }}</li></ul>
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

      <n-alert v-if="syncFailed" type="warning" :title="t('review.syncFailedTitle', { id: syncFailed.id })">
        {{ t('review.syncFailedBody', { error: syncFailed.error }) }}
        <div class="alert-actions">
          <n-button size="small" type="warning" :loading="saving" @click="resync">{{ t('review.resync') }}</n-button>
          <n-button size="small" @click="router.push({ name: 'revisions', query: { id: syncFailed.id } })">
            {{ t('review.viewPublished', { id: syncFailed.id }) }}
          </n-button>
        </div>
      </n-alert>

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
          <n-input v-model:value="comment" :placeholder="t('review.comment')"
            :disabled="Boolean(error) || Boolean(ws.savedDraftId)" />
          <n-space justify="end">
            <n-text v-if="!can('admin:publish')" depth="3" style="font-size: 12px">{{ t('review.noPublish') }}</n-text>
            <n-button :disabled="Boolean(error) || Boolean(ws.savedDraftId) || Boolean(syncFailed)" :loading="saving"
              @click="save(false)">
              {{ ws.savedDraftId ? t('review.draftSaved', { id: ws.savedDraftId }) : t('review.saveDraft') }}
            </n-button>
            <n-button v-if="can('admin:publish')" type="primary" :disabled="Boolean(error) || Boolean(syncFailed)"
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
