<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { NAlert, NButton, NCheckbox, NModal, NSpace, NSpin, NTag, NText } from 'naive-ui'
import type { EntityInput } from '@/gql/schema'
import { useStatus } from '@/stores/status'
import { batchState, scansOf, startBatch, sync } from '@/lib/schemaScans'
import { schemaScans, syncPlan, syncScope, type SchemaScan } from '@/lib/entitySync'
import type { Plan } from '@/lib/plan'

// The first step of an entity sync: the schemas its entities read, with
// their last scans. Those scanned over an hour ago (or never) are scanned
// again first by default, as columns, keys and gone tables are judged from
// the last scan; the scan can be skipped. Then the plan is previewed.
const props = defineProps<{ entities: EntityInput[] }>()
const show = defineModel<boolean>('show', { required: true })
const emit = defineEmits<{ plan: [plan: Plan] }>()
const { t } = useI18n()
const status = useStatus()

const scope = computed(() => syncScope(props.entities,
  new Set((status.server?.unreachableDatasources ?? []).map((d) => d.datasource))))
const loading = ref(false)
const rows = ref<SchemaScan[]>([])
const checked = ref<Set<string>>(new Set())
const scanning = ref(false)
const error = ref('')
const key = (s: SchemaScan) => `${s.datasource}\u0000${s.schema}`

watch(show, async (open) => {
  scanning.value = false
  error.value = ''
  if (!open) return
  loading.value = true
  try {
    const datasources = Object.keys(scope.value.schemas)
    await Promise.all(datasources.map((ds) => sync(ds)))
    const lists = Object.fromEntries(datasources.map((ds) => [ds, scansOf(ds)]))
    rows.value = schemaScans(scope.value, lists, Date.now())
    checked.value = new Set(rows.value.filter((r) => r.stale && r.schema).map(key))
  } finally {
    loading.value = false
  }
})

function toggle(s: SchemaScan, on: boolean) {
  const next = new Set(checked.value)
  if (on) next.add(key(s))
  else next.delete(key(s))
  checked.value = next
}

const selected = computed(() => rows.value.filter((r) => checked.value.has(key(r))))
const stateOf = (s: SchemaScan) => batchState(scansOf(s.datasource), s.schema)?.state ?? null
// The scan started here is over once no batch of its datasources runs.
const running = computed(() => scanning.value &&
  [...new Set(selected.value.map((s) => s.datasource))].some((ds) => scansOf(ds).batches.some((b) => b.state === 'running')))
watch(running, (now, before) => {
  if (before && !now && scanning.value) next()
})

async function scanThenPreview() {
  if (!selected.value.length) return next()
  error.value = ''
  scanning.value = true
  try {
    const by = new Map<string, string[]>()
    for (const s of selected.value) by.set(s.datasource, [...(by.get(s.datasource) ?? []), s.schema])
    await Promise.all([...by].map(([ds, schemas]) => startBatch(ds, schemas)))
    if (!running.value) next() // the batches finished before they were followed
  } catch (e) {
    scanning.value = false
    error.value = e instanceof Error ? e.message : String(e)
  }
}

function next() {
  scanning.value = false
  show.value = false
  emit('plan', syncPlan(scope.value.ids))
}
const when = (at: string | null) => (at ? new Date(at).toLocaleString() : t('entities.sync.neverScanned'))
const unreachable = computed(() => Object.entries(scope.value.unreachable))
</script>

<template>
  <n-modal v-model:show="show" preset="card" class="dialog" style="width: 680px" :title="t('entities.sync.title')">
    <n-spin :show="loading">
      <div class="stack" style="gap: 12px">
        <n-text>{{ t('entities.sync.intro', { n: scope.ids.length }) }}</n-text>
        <n-alert v-if="unreachable.length" type="warning" :show-icon="false">
          {{ t('entities.sync.unreachable', {
            list: unreachable.map(([ds, ids]) => `${ds}（${ids.length}）`).join('、') }) }}
        </n-alert>
        <n-text v-if="scope.procedures" depth="3">{{ t('entities.sync.procedures', { n: scope.procedures }) }}</n-text>
        <n-alert v-if="error" type="error" :show-icon="false">{{ error }}</n-alert>
        <div v-if="rows.length" class="stack" style="gap: 6px">
          <n-text strong>{{ t('entities.sync.schemas') }}</n-text>
          <div v-for="s in rows" :key="key(s)" class="row">
            <n-checkbox :checked="checked.has(key(s))" :disabled="scanning || !s.schema"
              @update:checked="(v: boolean) => toggle(s, v)">
              <span class="mono">{{ s.datasource }} · {{ s.schema || t('entities.sync.defaultSchema') }}</span>
            </n-checkbox>
            <n-text depth="3" class="meta">{{ when(s.scannedAt) }}</n-text>
            <n-tag v-if="s.stale" size="small" type="warning" :bordered="false">{{ t('entities.sync.stale') }}</n-tag>
            <n-tag v-if="scanning && checked.has(key(s)) && stateOf(s)" size="small" :bordered="false">
              {{ t(`entities.sync.state.${stateOf(s)}`, stateOf(s) ?? '') }}</n-tag>
          </div>
        </div>
        <n-text depth="3">{{ t('entities.sync.hint') }}</n-text>
      </div>
    </n-spin>
    <template #footer>
      <n-space justify="end">
        <n-button @click="show = false">{{ t('common.cancel') }}</n-button>
        <n-button :disabled="!scope.ids.length || scanning" @click="next">{{ t('entities.sync.skipScan') }}</n-button>
        <n-button type="primary" :disabled="!scope.ids.length" :loading="scanning" @click="scanThenPreview">
          {{ selected.length ? t('entities.sync.scanThenPreview', { n: selected.length }) : t('entities.sync.preview') }}
        </n-button>
      </n-space>
    </template>
  </n-modal>
</template>

<style scoped>
.stack { display: flex; flex-direction: column; }
.row { display: flex; align-items: center; gap: 8px; }
.meta { font-size: 12px; }
</style>
