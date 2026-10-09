<script setup lang="ts">
import { computed, h, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  NAlert, NButton, NDataTable, NInput, NProgress, NTag, NText, NTooltip, useMessage, type DataTableColumns,
} from 'naive-ui'
import {
  batchState, cancelBatch, followBatches, retryBatch, scansOf, startBatch, sync, type SchemaNode,
} from '@/lib/schemaScans'
import { can } from '@/api/session'
import { patterns } from '@/lib/plan'

// The schemas of one datasource and what was scanned of each: never, failed,
// fresh or stale. Scanning runs on the server in batches, a few schemas at a
// time; a batch goes on when the console is closed, shows its progress here,
// can be stopped, and what failed can be scanned again on its own.
const props = defineProps<{ datasource: string }>()
const { t } = useI18n()
const message = useMessage()
const s = computed(() => scansOf(props.datasource))
const filter = ref('')
const staleAfter = 7 * 24 * 3600 * 1000

watch(() => props.datasource, (ds) => {
  filter.value = ''
  if (!can('admin:write')) return
  void sync(ds)
  void followBatches(ds)
}, { immediate: true })

const shown = computed(() => {
  const ps = patterns(filter.value).map((p) => new RegExp('^' + p.replace(/[.+^${}()|[\]\\]/g, '\\$&')
    .replace(/\*/g, '.*').replace(/\?/g, '.') + '$', 'i'))
  return s.value.schemas.filter((n) => !ps.length || ps.some((re) => re.test(n.name)))
})
const busy = (n: SchemaNode) => ['pending', 'running'].includes(batchState(s.value, n.name)?.state ?? '')
const unscanned = computed(() => shown.value.filter((n) => !n.scannedAt && !busy(n)))
const staleOnes = computed(() => shown.value.filter((n) => isStale(n) && !busy(n)))
const runningBatch = computed(() => s.value.batches.find((b) => b.state === 'running') ?? null)
const lastBatch = computed(() => s.value.batches[0] ?? null)
const when = (at: string | null) => (at ? new Date(at).toLocaleString() : '')
const isStale = (n: SchemaNode) => !!n.scannedAt && Date.now() - new Date(n.scannedAt).getTime() > staleAfter
const writable = computed(() => can('admin:write'))

async function act(fn: () => Promise<unknown>) {
  try {
    await fn()
  } catch (e) {
    message.error(e instanceof Error ? e.message : String(e))
  }
}
const scan = (names: string[]) => act(() => startBatch(props.datasource, names))

function state(n: SchemaNode) {
  const b = batchState(s.value, n.name)
  if (b?.state === 'running') return h(NTag, { size: 'small', type: 'info', bordered: false }, () => t('datasources.scanning'))
  if (b?.state === 'pending') return h(NTag, { size: 'small', bordered: false }, () => t('catalog.queued'))
  const tags = []
  if (b?.state === 'failed') {
    tags.push(h(NTooltip, null, {
      trigger: () => h(NTag, { size: 'small', type: 'error', bordered: false }, () => t('catalog.scanFailed')),
      default: () => b.error,
    }))
  }
  if (!n.scannedAt) tags.push(h(NText, { depth: 3 }, () => t('datasources.notScanned')))
  else {
    tags.push(h(NText, { depth: 2 }, () => t('datasources.scannedMeta', { count: n.tables ?? 0, at: when(n.scannedAt) })))
    if (isStale(n)) tags.push(h(NTag, { size: 'small', type: 'warning', bordered: false }, () => t('catalog.stale')))
  }
  return h('div', { class: 'tags' }, tags)
}

function action(n: SchemaNode) {
  const label = batchState(s.value, n.name)?.state === 'failed' ? t('datasources.retry')
    : n.scannedAt ? t('datasources.rescan') : t('datasources.scan')
  return h(NTooltip, { disabled: writable.value }, {
    trigger: () => h(NButton, { size: 'tiny', disabled: !writable.value || busy(n), onClick: () => scan([n.name]) },
      () => label),
    default: () => t('catalog.needsWrite'),
  })
}

const columns = computed<DataTableColumns<SchemaNode>>(() => [
  {
    title: t('catalog.schema'), key: 'name',
    render: (n) => h('span', null, [
      h('span', { class: 'mono strong' }, n.name || t('datasources.defaultSchema')),
      n.name && n.name === s.value.defaultSchema
        ? h(NTag, { size: 'tiny', bordered: false, style: 'margin-left: 8px' }, () => t('datasources.defaultSchema'))
        : null,
    ]),
  },
  { title: t('datasources.status'), key: 'state', render: state },
  { title: '', key: 'action', width: 110, render: action },
])
</script>

<template>
  <div>
    <n-alert v-if="s.error" type="error" :show-icon="false" class="gap">
      {{ s.listed ? t('catalog.listStale', { error: s.error }) : s.error }}
      <n-button size="tiny" text type="primary" @click="sync(datasource)">{{ t('datasources.retry') }}</n-button>
    </n-alert>
    <div v-if="runningBatch" class="batch" role="status" aria-live="polite">
      <n-progress type="line" class="bar" :show-indicator="false"
        :percentage="Math.round(((runningBatch.total - runningBatch.pending) / runningBatch.total) * 100)" />
      <n-text class="meta">
        {{ t('catalog.batchProgress', { done: runningBatch.done, failed: runningBatch.failed, total: runningBatch.total,
          by: runningBatch.startedBy }) }}
      </n-text>
      <n-button size="small" @click="act(() => cancelBatch(datasource, runningBatch!.id))">{{ t('catalog.stopQueue') }}</n-button>
    </div>
    <div v-else-if="lastBatch && (lastBatch.failed || lastBatch.canceled)" class="batch" role="status">
      <n-text type="warning" class="meta">
        {{ t('catalog.batchEnded', { done: lastBatch.done, failed: lastBatch.failed, canceled: lastBatch.canceled }) }}
      </n-text>
      <n-button size="small" :disabled="!writable" @click="act(() => retryBatch(datasource, lastBatch!.id))">
        {{ t('catalog.retryFailed', { n: lastBatch.failed + lastBatch.canceled }) }}
      </n-button>
    </div>
    <div class="toolbar">
      <n-input v-model:value="filter" size="small" clearable class="search" :placeholder="t('catalog.schemaPatterns')"
        :input-props="{ 'aria-label': t('catalog.schemaPatterns') }" />
      <div class="group">
        <n-button size="small" type="primary" :disabled="!writable || !unscanned.length"
          @click="scan(unscanned.map((n) => n.name))">
          {{ t('catalog.scanUnscanned', { n: unscanned.length }) }}
        </n-button>
        <n-button size="small" :disabled="!writable || !staleOnes.length" @click="scan(staleOnes.map((n) => n.name))">
          {{ t('catalog.rescanStale', { n: staleOnes.length }) }}
        </n-button>
        <n-button size="small" :loading="s.syncing" :disabled="!writable" @click="sync(datasource, true)">
          {{ t('datasources.refreshSchemas') }}
        </n-button>
      </div>
    </div>
    <n-data-table size="small" :columns="columns" :data="shown" :row-key="(n: SchemaNode) => n.name"
      :pagination="{ pageSize: 10 }" :loading="!s.listed && s.syncing" />
  </div>
</template>

<style scoped>
.toolbar { display: flex; justify-content: space-between; align-items: center; gap: 12px; flex-wrap: wrap; margin-bottom: 8px; }
.group { display: flex; gap: 8px; flex-wrap: wrap; }
.search { width: 260px; }
.batch { display: flex; align-items: center; gap: 12px; margin-bottom: 8px; flex-wrap: wrap; }
.bar { width: 160px; }
.meta { font-size: 12px; }
.gap { margin-bottom: 8px; }
:deep(.strong) { font-weight: 600; }
:deep(.tags) { display: flex; gap: 6px; align-items: center; flex-wrap: wrap; }
</style>
