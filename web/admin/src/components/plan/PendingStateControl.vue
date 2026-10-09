<script setup lang="ts">
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { NButton, NInput, NList, NListItem, NModal, NPopover, NSpace, NTag, NText, useMessage } from 'naive-ui'
import { run } from '@/api/client'
import { PendingLogQuery, SetPendingStateMutation } from '@/api/ops'
import type { PendingStatePartsFragment } from '@/gql/graphql'
import { can, session } from '@/api/session'

// How one pending item is handled: who owns it, snoozed until when, or
// ignored for what reason; every change is logged. An item ignored or
// snoozed opens again when its situation changes (its key changes).
type State = PendingStatePartsFragment

const props = defineProps<{ itemKey: string; state?: State | null }>()
const emit = defineEmits<{ changed: [] }>()
const { t } = useI18n()
const message = useMessage()
const reason = ref('')
const ignoring = ref(false)
const logOpen = ref(false)
const log = ref<State[]>([])

async function set(status: string, extra: { until?: string; reason?: string; owner?: string } = {}) {
  try {
    await run(SetPendingStateMutation, { input: {
      key: props.itemKey, status, owner: extra.owner ?? props.state?.owner ?? null, reason: extra.reason ?? null,
      until: extra.until ?? null,
    } }, true)
    ignoring.value = false
    reason.value = ''
    emit('changed')
  } catch (e) {
    message.error(e instanceof Error ? e.message : String(e))
  }
}
/** Owns the item, keeping how it is handled. */
const take = () => set(props.state?.status ?? 'open', {
  owner: session.username ?? '', until: props.state?.until ?? undefined, reason: props.state?.reason ?? undefined,
})
const snooze = (days: number) => set('snoozed', { until: new Date(Date.now() + days * 86400000).toISOString() })
async function openLog() {
  log.value = (await run(PendingLogQuery, { key: props.itemKey })).pendingLog
  logOpen.value = true
}
const when = (at?: string | null) => (at ? new Date(at).toLocaleString() : '')
</script>

<template>
  <n-space :size="6" align="center" :wrap="false">
    <n-tag v-if="state?.status === 'ignored'" size="small" :bordered="false" :title="state.reason ?? ''">{{ t('pending.ignored') }}</n-tag>
    <n-tag v-else-if="state?.status === 'snoozed'" size="small" type="info" :bordered="false">
      {{ t('pending.snoozedUntil', { at: when(state.until) }) }}
    </n-tag>
    <n-tag v-if="state?.owner" size="small" :bordered="false">{{ t('pending.owner', { name: state.owner }) }}</n-tag>
    <template v-if="can('admin:write')">
      <n-button v-if="state?.owner !== session.username" size="tiny" @click="take">
        {{ t('pending.take') }}
      </n-button>
      <template v-if="state?.status === 'ignored' || state?.status === 'snoozed'">
        <n-button size="tiny" @click="set('open')">{{ t('pending.reopen') }}</n-button>
      </template>
      <template v-else>
        <n-popover trigger="click">
          <template #trigger><n-button size="tiny">{{ t('pending.snooze') }}</n-button></template>
          <n-space vertical :size="4">
            <n-button size="tiny" text @click="snooze(1)">{{ t('pending.days', { n: 1 }) }}</n-button>
            <n-button size="tiny" text @click="snooze(7)">{{ t('pending.days', { n: 7 }) }}</n-button>
            <n-button size="tiny" text @click="snooze(30)">{{ t('pending.days', { n: 30 }) }}</n-button>
          </n-space>
        </n-popover>
        <n-popover v-model:show="ignoring" trigger="click">
          <template #trigger><n-button size="tiny">{{ t('pending.ignore') }}</n-button></template>
          <n-space vertical :size="6" class="ignore">
            <n-input v-model:value="reason" type="textarea" :autosize="{ minRows: 2 }" :placeholder="t('pending.reason')" />
            <n-button size="tiny" type="primary" :disabled="!reason.trim()" @click="set('ignored', { reason })">{{ t('pending.ignore') }}</n-button>
          </n-space>
        </n-popover>
      </template>
    </template>
    <n-button size="tiny" text @click="openLog">{{ t('pending.log') }}</n-button>
  </n-space>
  <n-modal v-model:show="logOpen" preset="card" class="dialog" :title="t('pending.logTitle')">
    <n-text v-if="!log.length" depth="3">{{ t('pending.noLog') }}</n-text>
    <n-list v-else size="small">
      <n-list-item v-for="(e, i) in log" :key="i">
        <n-text strong>{{ t(`pending.status.${e.status}`) }}</n-text>
        <n-text depth="3"> · {{ e.updatedBy }} · {{ when(e.updatedAt) }}</n-text>
        <n-text v-if="e.owner" depth="3"> · {{ t('pending.owner', { name: e.owner }) }}</n-text>
        <n-text v-if="e.until" depth="3"> · {{ t('pending.snoozedUntil', { at: when(e.until) }) }}</n-text>
        <div v-if="e.reason" class="reason">{{ e.reason }}</div>
      </n-list-item>
    </n-list>
  </n-modal>
</template>

<style scoped>
.ignore { width: 260px; }
.reason { font-size: 13px; margin-top: 2px; }
</style>
