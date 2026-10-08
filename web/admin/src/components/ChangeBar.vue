<script setup lang="ts">
import { computed } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { NButton, NEmpty, NIcon, NList, NListItem, NPopover, NSpace, NTag, NText, useDialog } from 'naive-ui'
import { ArrowUndoOutline } from '@vicons/ionicons5'
import { useWorkspace, type Change } from '@/stores/workspace'
import { can } from '@/api/session'

const { t } = useI18n()
const ws = useWorkspace()
const router = useRouter()
const dialog = useDialog()

const typeTag: Record<Change['type'], 'success' | 'error' | 'warning'> = {
  added: 'success', removed: 'error', modified: 'warning',
}
const routeFor = (c: Change) =>
  c.type === 'removed'
    ? null
    : c.kind === 'settings'
      ? { name: 'settings' }
      : { name: c.kind, params: { name: c.name } }
const displayName = (c: Change) => (c.kind === 'settings' ? t('changes.settingsName') : c.name)

const count = computed(() => ws.changes.length)
// Where the edits are: only in this browser, saved as a draft revision, or
// nothing pending (the runtime badge then tells what the server applies).
const state = computed(() => ws.savedDraftId
  ? { type: 'info' as const, label: t('changes.draftSaved', { id: ws.savedDraftId }) }
  : count.value
    ? { type: 'warning' as const, label: t('changes.unsavedCount', { count: count.value }, count.value) }
    : { type: 'default' as const, label: ws.baseId ? t('changes.inSyncWith', { id: ws.baseId }) : t('changes.none') })

function discard() {
  dialog.warning({
    title: t('changes.discardTitle'),
    content: t('changes.discardBody', { count: count.value, id: ws.baseId }, count.value),
    positiveText: t('changes.discardConfirm'),
    negativeText: t('common.cancel'),
    onPositiveClick: () => ws.discard(),
  })
}
</script>

<template>
  <n-space align="center" :size="10" :wrap="false">
    <n-popover trigger="click" placement="bottom-end" :width="360">
      <template #trigger>
        <n-button size="small" :type="state.type" secondary>{{ state.label }}</n-button>
      </template>
      <n-empty v-if="!count" :description="t('changes.inSync')" size="small" />
      <template v-else>
        <n-text depth="3" class="where">
          {{ ws.savedDraftId ? t('changes.whereDraft', { id: ws.savedDraftId }) : t('changes.whereLocal') }}
        </n-text>
        <n-list hoverable clickable :show-divider="false" class="list">
          <n-list-item v-for="c in ws.changes" :key="c.kind + c.name"
            @click="routeFor(c) && router.push(routeFor(c)!)">
            <div class="item">
              <n-space align="center" :size="8" :wrap="false">
                <n-tag size="small" :type="typeTag[c.type]" :bordered="false">{{ t(`changes.type.${c.type}`) }}</n-tag>
                <n-text depth="3">{{ t(`changes.kind.${c.kind}`) }}</n-text>
                <n-text strong class="mono">{{ displayName(c) }}</n-text>
              </n-space>
              <n-button v-if="can('admin:write')" size="tiny" quaternary circle :title="t('changes.revert')"
                :aria-label="t('changes.revert')" @click.stop="ws.revert(c)">
                <template #icon><n-icon><arrow-undo-outline /></n-icon></template>
              </n-button>
            </div>
          </n-list-item>
        </n-list>
        <n-space justify="end" style="margin-top: 8px">
          <n-button v-if="ws.savedDraftId" size="small" quaternary
            @click="router.push({ name: 'revisions', query: { id: ws.savedDraftId } })">{{ t('review.viewDraft') }}</n-button>
          <n-button size="small" quaternary type="error" @click="discard">{{ t('changes.discardAll') }}</n-button>
        </n-space>
      </template>
    </n-popover>
    <n-button v-if="can('admin:write')" size="small" type="primary" :disabled="!count"
      @click="router.push({ name: 'review' })">
      {{ t('changes.review') }}
    </n-button>
  </n-space>
</template>

<style scoped>
.list { max-height: 360px; overflow: auto; }
.where { display: block; font-size: 12px; margin-bottom: 6px; }
.item { display: flex; align-items: center; justify-content: space-between; gap: 8px; }
</style>
