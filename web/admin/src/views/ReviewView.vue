<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import {
  NAlert, NButton, NCard, NCollapse, NCollapseItem, NEmpty, NInput, NSpace, NSpin, NTag, NText, useDialog,
  useMessage,
} from 'naive-ui'
import DiffView from '@/components/DiffView.vue'
import ChangeSummary from '@/components/ChangeSummary.vue'
import { summarize, type Config } from '@/lib/changeSummary'
import { useWorkspace, type Change } from '@/stores/workspace'
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
const diff = ref('')
const comment = ref('')

const restartList = () => restart.value.join(t('common.listSep'))
const summary = computed(() => summarize(JSON.parse(ws.baseline) as Config, ws.$state))

async function check() {
  if (!ws.baseId) return
  checking.value = true
  try {
    const v = (await run(ValidateQuery, { draft: ws.draft })).validate
    error.value = v.ok ? null : v.error ?? t('review.validateFailed')
    restart.value = v.restartRequired
    diff.value = v.ok ? (await run(DiffQuery, { from: ws.baseId, draft: ws.draft })).diff : ''
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    checking.value = false
  }
}

function revertAndCheck(c: Change) {
  ws.revert(c)
  if (ws.dirty) void check()
}

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

async function save(publish: boolean) {
  saving.value = true
  // What is saved is the workspace at the click; edits made while the
  // requests run stay unsaved (and survive publishing, re-applied on top).
  const snap = ws.snapshot()
  const reuse = ws.savedDraftId
  try {
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
      input: { id, restartRequired: restart.value.length > 0, expectedPublished: ws.basePublished },
    }, true)
    // Edits made since the click (including undoing part of it) are measured
    // from the snapshot when the published configuration arrives, and kept.
    await ws.rebase(id, snap.sections)
    void status.refresh()
    message.success(t(restart.value.length ? 'review.publishedRestart' : 'review.published', { id }))
    void router.push({ name: 'revisions', query: { id } })
  } catch (e) {
    if (e instanceof ApiError && e.code === 'CONFLICT') return conflict(String(e.extensions.published))
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
            :closable="can('admin:write')" @close="revertAndCheck(c)"
            :type="c.type === 'added' ? 'success' : c.type === 'removed' ? 'error' : 'warning'">
            {{ t(`changes.type.${c.type}`) }} · {{ t(`changes.kind.${c.kind}`) }} · {{ c.kind === 'settings' ? t('changes.settingsName') : c.name }}
          </n-tag>
        </n-space>
      </n-card>

      <n-alert v-if="error" type="error" :title="t('review.invalid')">
        <pre class="mono" style="white-space: pre-wrap; margin: 0">{{ error }}</pre>
        <div class="alert-actions"><n-button size="small" @click="check">{{ t('review.recheck') }}</n-button></div>
      </n-alert>
      <n-alert v-else-if="!checking && restart.length" type="warning" :title="t('review.restartTitle')">
        {{ t('review.restartBody', { list: restartList() }) }}
      </n-alert>
      <n-alert v-else-if="!checking" type="success" :title="t('review.okTitle')">
        {{ t('review.okBody') }}
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
          <n-input v-model:value="comment" :placeholder="t('review.comment')"
            :disabled="Boolean(error) || Boolean(ws.savedDraftId)" />
          <n-space justify="end">
            <n-text v-if="!can('admin:publish')" depth="3" style="font-size: 12px">{{ t('review.noPublish') }}</n-text>
            <n-button :disabled="Boolean(error) || Boolean(ws.savedDraftId)" :loading="saving" @click="save(false)">
              {{ ws.savedDraftId ? t('review.draftSaved', { id: ws.savedDraftId }) : t('review.saveDraft') }}
            </n-button>
            <n-button v-if="can('admin:publish')" type="primary" :disabled="Boolean(error)" :loading="saving" @click="publish">
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
</style>
