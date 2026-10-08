<script setup lang="ts">
import { computed, h, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { NAlert, NButton, NDataTable, NIcon, NSpace, NSwitch, NText, useThemeVars, type DataTableColumns } from 'naive-ui'
import { CheckmarkCircle, CloseCircle, HelpCircle } from '@vicons/ionicons5'
import { run } from '@/api/client'
import { VisibilityQuery } from '@/api/ops'
import { canonical } from '@/stores/workspace'
import { describeFilter, type Filter } from '@/lib/filter'
import type { Action, DraftInput, VisibilityQuery as VisibilityResult } from '@/gql/graphql'

type Simulation = VisibilityResult['visibility'][number]['actions'][number]['result']
interface Row { entity: string; cells: Partial<Record<Action, Simulation>>; read: Simulation | null }

// Shows what one principal can reach: every entity × action with the default
// projection; actions that apply to no entity get no column. With `live`, it
// recomputes shortly after the draft changes.
const props = defineProps<{
  who: string | null
  draft: DraftInput | null
  live?: boolean
  compact?: boolean
}>()
const { t } = useI18n()
const themeVars = useThemeVars()
const actions: Action[] = ['READ', 'AGGREGATE', 'CREATE', 'UPDATE', 'DELETE', 'EXECUTE']

const rows = ref<Row[]>([])
const loading = ref(false)
const error = ref('')
const onlyReachable = ref(Boolean(props.compact))
let seq = 0

async function refresh() {
  if (!props.who) {
    rows.value = []
    return
  }
  const mine = ++seq
  loading.value = true
  try {
    const data = await run(VisibilityQuery, { input: { user: props.who, draft: props.draft } })
    if (mine !== seq) return
    rows.value = data.visibility.map((v) => {
      const cells = Object.fromEntries(v.actions.map((a) => [a.action, a.result])) as Row['cells']
      return { entity: v.entity, cells, read: cells.READ ?? cells.EXECUTE ?? null }
    })
    error.value = ''
  } catch (e) {
    if (mine === seq) error.value = e instanceof Error ? e.message : String(e)
  } finally {
    if (mine === seq) loading.value = false
  }
}

let timer: ReturnType<typeof setTimeout> | undefined
watch(() => props.who, () => void refresh(), { immediate: true })
watch(() => canonical(props.draft), () => {
  clearTimeout(timer)
  timer = setTimeout(() => void refresh(), props.live ? 400 : 0)
})
onBeforeUnmount(() => clearTimeout(timer))

const reachable = (r: Row) => Object.values(r.cells).some((c) => c?.allowed)
const shown = computed(() => (onlyReachable.value ? rows.value.filter(reachable) : rows.value))
const reachableCount = computed(() => rows.value.filter(reachable).length)

function mark(cell?: Simulation) {
  if (!cell) return h(NText, { depth: 3 }, () => '—')
  const v = themeVars.value
  const ambiguous = Boolean(cell.fieldScopes?.length)
  const [icon, color] = ambiguous
    ? [HelpCircle, v.warningColor]
    : cell.allowed ? [CheckmarkCircle, v.successColor] : [CloseCircle, v.textColorDisabled]
  return h(NIcon, { color, size: 18 }, () => h(icon))
}

const columns = computed<DataTableColumns<Row>>(() => [
  { title: t('simulate.entity'), key: 'entity', minWidth: 120, render: (r) => h('span', { class: 'mono' }, r.entity) },
  ...actions.filter((a) => rows.value.some((r) => r.cells[a])).map((a) => ({
    title: t(`grants.actions.${a}`), key: a, width: 72, align: 'center' as const,
    render: (r: Row) => mark(r.cells[a]),
  })),
  {
    title: t('simulate.visibleFields'), key: 'fields', minWidth: 160,
    render: (r) => r.read?.fieldScopes?.length ? h(NText, { type: 'warning' }, () => t('simulate.needsFields'))
      : r.read?.allowed ? h(NText, null, () => r.read!.fields.join(', ')) : '',
  },
  {
    title: t('simulate.rows'), key: 'rows', minWidth: 120,
    render: (r) => (r.read?.allowed ? describeFilter(r.read.rowFilter as Filter) : ''),
  },
])

defineExpose({ refresh })
</script>

<template>
  <n-space vertical :size="10">
    <div class="bar">
      <n-space align="center" :size="10">
        <n-button size="small" :loading="loading" :disabled="!who" @click="refresh">{{ t('simulate.recompute') }}</n-button>
        <n-text v-if="rows.length" depth="3">{{ t('simulate.reachable', { n: reachableCount, total: rows.length }) }}</n-text>
        <n-text v-if="live" depth="3" class="hint">{{ t('visibility.live') }}</n-text>
      </n-space>
      <n-space align="center" :size="8" :wrap="false">
        <n-switch v-model:value="onlyReachable" size="small" />
        <n-text class="hint">{{ t('visibility.onlyReachable') }}</n-text>
      </n-space>
    </div>
    <n-alert v-if="error" type="error" :show-icon="false">{{ error }}</n-alert>
    <n-data-table v-else-if="rows.length" :columns="columns" :data="shown" size="small" :loading="loading"
      :row-key="(r: Row) => r.entity" />
  </n-space>
</template>

<style scoped>
.bar { display: flex; justify-content: space-between; align-items: center; gap: 12px; flex-wrap: wrap; }
.hint { font-size: 12px; }
</style>
