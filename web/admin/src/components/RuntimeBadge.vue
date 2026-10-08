<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { NPopover, NTag, NText } from 'naive-ui'
import { useStatus } from '@/stores/status'

// The revision the server actually serves, next to the published one.
const { t, locale } = useI18n()
const status = useStatus()

const state = computed(() => {
  const server = status.server
  const applied = server?.appliedRevision
  const published = status.publishedId
  if (!server || !applied) return null
  if (server.pending) {
    return server.pending.restartRequired
      ? { type: 'warning' as const, label: t('runtime.pendingRestart', { id: server.pending.id }),
          hint: t('runtime.restartHint', { applied }) }
      : { type: 'error' as const, label: t('runtime.failed', { id: server.pending.id }),
          hint: `${t('runtime.failedHint', { applied })} ${server.pending.error}` }
  }
  if (published && published !== applied) {
    return server.watching
      ? { type: 'info' as const, label: t('runtime.loading', { id: published }), hint: t('runtime.loadingHint') }
      : { type: 'warning' as const, label: t('runtime.notWatching', { id: published }), hint: t('runtime.notWatchingHint') }
  }
  return { type: 'success' as const, label: t('runtime.serving', { id: applied }), hint: '' }
})
const publishedAt = computed(() =>
  status.published?.publishedAt ? new Date(status.published.publishedAt).toLocaleString(locale.value) : '')
</script>

<template>
  <n-popover v-if="state" trigger="hover" placement="bottom-end" :width="320">
    <template #trigger>
      <n-tag size="small" :type="state.type" :bordered="false" round class="badge">
        <span class="dot" :class="state.type" />{{ state.label }}
      </n-tag>
    </template>
    <div class="rows">
      <div class="row">
        <n-text depth="3">{{ t('runtime.published') }}</n-text>
        <span>#{{ status.publishedId }} · {{ status.published?.author }} · {{ publishedAt }}</span>
      </div>
      <div class="row"><n-text depth="3">{{ t('runtime.applied') }}</n-text><span>#{{ status.server?.appliedRevision }}</span></div>
      <div class="row">
        <n-text depth="3">{{ t('runtime.watch') }}</n-text>
        <span>{{ status.server?.watching ? t('runtime.watchOn') : t('runtime.watchOff') }}</span>
      </div>
      <n-text v-if="state.hint" :type="state.type === 'success' ? 'default' : state.type" class="hint">{{ state.hint }}</n-text>
    </div>
  </n-popover>
</template>

<style scoped>
.badge { cursor: default; }
.dot { display: inline-block; width: 6px; height: 6px; border-radius: 50%; margin-right: 6px; vertical-align: middle; background: currentColor; }
.rows { display: flex; flex-direction: column; gap: 6px; font-size: 13px; }
.row { display: flex; justify-content: space-between; gap: 12px; }
.hint { font-size: 12px; margin-top: 4px; word-break: break-word; }
</style>
