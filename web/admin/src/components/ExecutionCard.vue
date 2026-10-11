<script setup lang="ts">
import { computed, h } from 'vue'
import { useI18n } from 'vue-i18n'
import { NCard, NDataTable, NProgress, NSpace, NTag, NText, type DataTableColumns } from 'naive-ui'
import { useStatus } from '@/stores/status'

// What the serving process runs on its execution bus: each lane's work
// against its limits, the shared IO slots and the background tasks.
const { t } = useI18n()
const status = useStatus()

const execution = computed(() => status.server?.execution ?? null)
type Lane = NonNullable<typeof execution.value>['lanes'][number]

const percent = (used: number, size: number) => (size > 0 ? Math.min(100, Math.round((used / size) * 100)) : 0)

const columns = computed<DataTableColumns<Lane>>(() => [
  { title: t('execution.lane'), key: 'name', render: (l) => t(`execution.lanes.${l.name}`) },
  {
    title: t('execution.running'), key: 'inflight',
    render: (l) => h(NProgress, {
      type: 'line', percentage: percent(l.inflight, l.maxConcurrent), showIndicator: false, height: 6,
      status: l.inflight >= l.maxConcurrent ? 'warning' : 'default',
    }),
  },
  { title: t('execution.inflight'), key: 'limit', render: (l) => `${l.inflight} / ${l.maxConcurrent}` },
  { title: t('execution.queued'), key: 'queued', render: (l) => (l.name === 'serve' ? '—' : `${l.queued} / ${l.maxQueue}`) },
  {
    title: t('execution.rejected'), key: 'rejected',
    render: (l) => (l.rejected > 0 ? h(NTag, { size: 'small', type: 'warning', bordered: false }, () => l.rejected) : '0'),
  },
  { title: t('execution.completed'), key: 'completed' },
])
</script>

<template>
  <n-card v-if="execution" :title="t('execution.title')" size="small">
    <n-space vertical :size="12">
      <div class="slots">
        <div v-for="pool in (['io', 'aux'] as const)" :key="pool" class="slot">
          <n-text depth="3">{{ t(`execution.${pool}`) }}</n-text>
          <n-progress type="line" :percentage="percent(execution[pool].used, execution[pool].size)" :show-indicator="false" :height="6" />
          <span class="num">{{ execution[pool].used }} / {{ execution[pool].size }}</span>
        </div>
      </div>
      <n-data-table size="small" :columns="columns" :data="execution.lanes" :bordered="false" :row-key="(l: Lane) => l.name" />
      <div class="tasks">
        <n-text depth="3">{{ t('execution.tasks') }}</n-text>
        <n-tag v-for="task in execution.tasks" :key="task.name" size="small" :bordered="false">
          {{ task.name }} × {{ task.running }}
        </n-tag>
        <n-text v-if="!execution.tasks.length" depth="3">{{ t('execution.noTasks') }}</n-text>
      </div>
    </n-space>
  </n-card>
</template>

<style scoped>
.slots { display: grid; grid-template-columns: 1fr 1fr; gap: 16px; }
.slot { display: grid; grid-template-columns: auto 1fr auto; align-items: center; gap: 8px; font-size: 13px; }
.num { font-variant-numeric: tabular-nums; }
.tasks { display: flex; flex-wrap: wrap; align-items: center; gap: 6px; font-size: 13px; }
</style>
