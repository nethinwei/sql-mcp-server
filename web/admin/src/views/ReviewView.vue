<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import {
  NAlert, NButton, NCard, NEmpty, NInput, NSpace, NSpin, NTag, NText, useDialog, useMessage,
} from 'naive-ui'
import DiffView from '@/components/DiffView.vue'
import { useWorkspace, type Change } from '@/stores/workspace'
import { ApiError, run } from '@/api/client'
import { useStatus } from '@/stores/status'
import { CreateDraftMutation, DiffQuery, PublishMutation, ValidateQuery } from '@/api/ops'
import { can } from '@/api/session'

const { t } = useI18n()
const status = useStatus()
const ws = useWorkspace()
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

async function saveDraft() {
  const { createDraft } = await run(CreateDraftMutation, { draft: ws.draft, comment: comment.value || null }, true)
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
  try {
    if (publish) {
      await status.refresh()
      if (status.publishedId && status.publishedId !== ws.basePublished) return conflict(status.publishedId)
    }
    const draft = await saveDraft()
    if (!publish) {
      message.success(t('review.savedDraft', { id: draft.id }))
      return
    }
    await run(PublishMutation, {
      input: { id: draft.id, restartRequired: restart.value.length > 0, expectedPublished: ws.basePublished },
    }, true)
    await ws.load(draft.id)
    void status.refresh()
    message.success(t(restart.value.length ? 'review.publishedRestart' : 'review.published', { id: draft.id }))
    void router.push({ name: 'revisions' })
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

      <n-card v-if="!error" size="small" :title="t('review.diffTitle', { id: ws.baseId })">
        <diff-view :diff="diff" />
      </n-card>

      <n-card size="small">
        <n-space vertical>
          <n-input v-model:value="comment" :placeholder="t('review.comment')" :disabled="Boolean(error)" />
          <n-space justify="end">
            <n-text v-if="!can('admin:publish')" depth="3" style="font-size: 12px">{{ t('review.noPublish') }}</n-text>
            <n-button :disabled="Boolean(error)" :loading="saving" @click="save(false)">{{ t('review.saveDraft') }}</n-button>
            <n-button v-if="can('admin:publish')" type="primary" :disabled="Boolean(error)" :loading="saving" @click="publish">
              {{ t('review.saveAndPublish') }}
            </n-button>
          </n-space>
        </n-space>
      </n-card>
    </n-space>
  </n-spin>
</template>
