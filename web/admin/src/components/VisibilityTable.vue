<script setup lang="ts">
import { computed, h, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  NAlert, NButton, NDataTable, NIcon, NSpace, NSwitch, NTag, NText, NTooltip, useThemeVars, type DataTableColumns,
} from 'naive-ui'
import { CheckmarkCircle, CloseCircle, HelpCircle } from '@vicons/ionicons5'
import { run } from '@/api/client'
import { VisibilityQuery } from '@/api/ops'
import { canonical } from '@/stores/workspace'
import { describeFilter, type Filter } from '@/lib/filter'
import type { Action, DraftInput, VisibilityQuery as VisibilityResult } from '@/gql/graphql'

type Simulation = VisibilityResult['visibility'][number]['actions'][number]['result']
interface Row { entity: string; cells: Partial<Record<Action, Simulation>>; read: Simulation | null }

// Shows what one principal can reach: every entity × action with the default
// projection; actions that apply to no entity get no column. A cell is
// allowed, needs fields (the fields are split across grants, so a call must
// select fields within one scope) or denied; the first two count as
// reachable. Clicking a field scope emits `pick` to simulate that call. With
// `live`, it recomputes shortly after the draft changes.
const props = defineProps<{
  who: string | null
  draft: DraftInput | null
  live?: boolean
  compact?: boolean
}>()
const emit = defineEmits<{ pick: [entity: string, action: Action, fields: string[]] }>()
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

type State = 'allowed' | 'needsFields' | 'denied'
const scopesOf = (c?: Simulation | null) => (c?.fieldScopes ?? []).filter((s) => s.length > 0)
const stateOf = (c?: Simulation | null): State =>
  c?.allowed ? 'allowed' : scopesOf(c).length ? 'needsFields' : 'denied'
const reachable = (r: Row) => Object.values(r.cells).some((c) => stateOf(c) !== 'denied')
const shown = computed(() => (onlyReachable.value ? rows.value.filter(reachable) : rows.value))
const reachableCount = computed(() => rows.value.filter(reachable).length)
const needsFieldsCount = computed(() =>
  rows.value.filter((r) => reachable(r) && !Object.values(r.cells).some((c) => c?.allowed)).length)

function mark(r: Row, action: Action) {
  const cell = r.cells[action]
  if (!cell) return h(NText, { depth: 3 }, () => '—')
  const v = themeVars.value
  const state = stateOf(cell)
  const [icon, color] = state === 'needsFields' ? [HelpCircle, v.warningColor]
    : state === 'allowed' ? [CheckmarkCircle, v.successColor] : [CloseCircle, v.textColorDisabled]
  const node = h(NIcon, {
    color, size: 18, class: state === 'needsFields' ? 'pickable' : undefined,
    onClick: state === 'needsFields' ? () => emit('pick', r.entity, action, scopesOf(cell)[0]) : undefined,
  }, () => h(icon))
  return h(NTooltip, null, { trigger: () => node, default: () => t(`visibility.state.${state}`) })
}

/** The field scopes of a needs-fields cell, each a click away from a simulation. */
function scopeTags(r: Row) {
  const action: Action = r.cells.READ ? 'READ' : 'EXECUTE'
  return h(NSpace, { size: 4 }, () => scopesOf(r.read).map((scope) => h(NTag, {
    size: 'small', type: 'warning', bordered: false, class: 'pickable',
    onClick: () => emit('pick', r.entity, action, scope),
  }, () => scope.join(', '))))
}

const columns = computed<DataTableColumns<Row>>(() => [
  { title: t('simulate.entity'), key: 'entity', minWidth: 120, render: (r) => h('span', { class: 'mono' }, r.entity) },
  ...actions.filter((a) => rows.value.some((r) => r.cells[a])).map((a) => ({
    title: t(`grants.actions.${a}`), key: a, width: 72, align: 'center' as const,
    render: (r: Row) => mark(r, a),
  })),
  {
    title: t('simulate.visibleFields'), key: 'fields', minWidth: 160,
    render: (r) => stateOf(r.read) === 'needsFields' ? scopeTags(r)
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
        <n-text v-if="rows.length" depth="3">
          {{ t('simulate.reachable', { n: reachableCount, total: rows.length }) }}<template v-if="needsFieldsCount">
            {{ t('visibility.needsFieldsCount', { n: needsFieldsCount }) }}</template>
        </n-text>
        <n-text v-if="live" depth="3" class="hint">{{ t('visibility.live') }}</n-text>
      </n-space>
      <n-space align="center" :size="8" :wrap="false">
        <n-switch v-model:value="onlyReachable" size="small" />
        <n-text class="hint">{{ t('visibility.onlyReachable') }}</n-text>
      </n-space>
    </div>
    <n-text v-if="rows.length" depth="3" class="hint">{{ t('visibility.legend') }}</n-text>
    <n-alert v-if="error" type="error" :show-icon="false">{{ error }}</n-alert>
    <n-data-table v-else-if="rows.length" :columns="columns" :data="shown" size="small" :loading="loading"
      :row-key="(r: Row) => r.entity" />
  </n-space>
</template>

<style scoped>
.bar { display: flex; justify-content: space-between; align-items: center; gap: 12px; flex-wrap: wrap; }
.hint { font-size: 12px; }
:deep(.pickable) { cursor: pointer; }
</style>
