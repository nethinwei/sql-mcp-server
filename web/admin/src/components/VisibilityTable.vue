<script setup lang="ts">
import { computed, h, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  NAlert, NButton, NDataTable, NIcon, NInput, NSpace, NSwitch, NTag, NText, NTooltip, useThemeVars,
  type DataTableColumns,
} from 'naive-ui'
import { CheckmarkCircle, CloseCircle, HelpCircle } from '@vicons/ionicons5'
import { run } from '@/api/client'
import { VisibilityPageQuery } from '@/api/ops'
import { workspace } from '@/lib/workspaceSync'
import { describeFilter, type Filter } from '@/lib/filter'
import { EntityIndex } from '@/lib/entityRefs'
import type { Action, VisibilityPageQuery as VisibilityResult } from '@/gql/graphql'
import type { EntityInput } from '@/gql/schema'

type Page = VisibilityResult['visibilityPage']
type Simulation = Page['items'][number]['actions'][number]['result']
interface Row { entity: string; cells: Partial<Record<Action, Simulation>>; read: Simulation | null }

// Shows what one principal can reach: every entity × action with the default
// projection, a page at a time (the server evaluates, filters and counts every
// entity); actions that apply to no entity get no column. A cell is
// allowed, needs fields (the fields are split across grants, so a call must
// select fields within one scope) or denied; the first two count as
// reachable. Clicking a field scope emits `pick` to simulate that call. With
// `live`, it recomputes shortly after what is simulated changes (context).
const props = defineProps<{
  who: string | null
  // The revision simulated; null: the workspace.
  revision: string | null
  // The entities simulated, to name them.
  entities: EntityInput[]
  // Changes whenever what is simulated does.
  context: string
  live?: boolean
  compact?: boolean
}>()
const emit = defineEmits<{ pick: [entity: string, action: Action, fields: string[]] }>()
const { t } = useI18n()
const themeVars = useThemeVars()
const actions: Action[] = ['READ', 'AGGREGATE', 'CREATE', 'UPDATE', 'DELETE', 'EXECUTE']

// Rows are entity IDs, shown by their shortest reference.
const entityIndex = computed(() => new EntityIndex(props.entities))
const result = ref<Page | null>(null)
const loading = ref(false)
const error = ref('')
const onlyReachable = ref(Boolean(props.compact))
const search = ref('')
const query = ref('') // search, applied a moment after typing stops
const page = ref(1)
const pageSize = ref(50)
let seq = 0

const rows = computed<Row[]>(() => (result.value?.items ?? []).map((v) => {
  const cells = Object.fromEntries(v.actions.map((a) => [a.action, a.result])) as Row['cells']
  return { entity: v.entity, cells, read: cells.READ ?? cells.EXECUTE ?? null }
}))

async function refresh() {
  if (!props.who) {
    result.value = null
    return
  }
  const mine = ++seq
  loading.value = true
  try {
    const vars = {
      input: { user: props.who, revision: props.revision },
      filter: { search: query.value.trim() || null, reachableOnly: onlyReachable.value },
      offset: (page.value - 1) * pageSize.value, limit: pageSize.value,
    }
    const data = props.revision === null
      ? await workspace.read(VisibilityPageQuery, vars)
      : await run(VisibilityPageQuery, vars)
    if (mine !== seq) return
    result.value = data.visibilityPage
    error.value = ''
  } catch (e) {
    if (mine === seq) error.value = e instanceof Error ? e.message : String(e)
  } finally {
    if (mine === seq) loading.value = false
  }
}

let timer: ReturnType<typeof setTimeout> | undefined
let typing: ReturnType<typeof setTimeout> | undefined
watch(search, (q) => {
  clearTimeout(typing)
  typing = setTimeout(() => { query.value = q }, 300)
})
// A new filter starts at its first page; this runs before the refresh below.
watch([query, onlyReachable], () => { page.value = 1 })
watch(() => [props.who, query.value, onlyReachable.value, page.value, pageSize.value], () => void refresh(),
  { immediate: true })
watch(() => props.context, () => {
  clearTimeout(timer)
  timer = setTimeout(() => void refresh(), props.live ? 400 : 0)
})
onBeforeUnmount(() => { clearTimeout(timer); clearTimeout(typing) })

type State = 'allowed' | 'needsFields' | 'denied'
const scopesOf = (c?: Simulation | null) => (c?.fieldScopes ?? []).filter((s) => s.length > 0)
const stateOf = (c?: Simulation | null): State =>
  c?.allowed ? 'allowed' : scopesOf(c).length ? 'needsFields' : 'denied'
// Past the last page (what is simulated changed): the last page.
const pageCount = computed(() => Math.max(1, Math.ceil((result.value?.matched ?? 0) / pageSize.value)))
watch(pageCount, (n) => { if (page.value > n) page.value = n })

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
  { title: t('simulate.entity'), key: 'entity', minWidth: 120, render: (r) => h('span', { class: 'mono', title: r.entity }, entityIndex.value.shortName(r.entity)) },
  ...actions.filter((a) => result.value?.actions.includes(a)).map((a) => ({
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
        <n-text v-if="result?.total" depth="3">
          {{ t('simulate.reachable', { n: result.reachable, total: result.total }) }}<template v-if="result.needsFields">
            {{ t('visibility.needsFieldsCount', { n: result.needsFields }) }}</template>
        </n-text>
        <n-text v-if="live" depth="3" class="hint">{{ t('visibility.live') }}</n-text>
      </n-space>
      <n-space align="center" :size="8" :wrap="false">
        <n-input v-if="result?.total" v-model:value="search" size="small" clearable :placeholder="t('entities.search')"
          class="search" />
        <n-switch v-model:value="onlyReachable" size="small" />
        <n-text class="hint">{{ t('visibility.onlyReachable') }}</n-text>
      </n-space>
    </div>
    <n-text v-if="result?.total" depth="3" class="hint">{{ t('visibility.legend') }}</n-text>
    <n-alert v-if="error" type="error" :show-icon="false">{{ error }}</n-alert>
    <n-data-table v-else-if="result?.total" remote :columns="columns" :data="rows" size="small" :loading="loading"
      :pagination="{ page, pageSize, itemCount: result.matched, showSizePicker: true, pageSizes: [20, 50, 100, 200],
        onUpdatePage: (p: number) => (page = p), onUpdatePageSize: (n: number) => { pageSize = n; page = 1 } }"
      :row-key="(r: Row) => r.entity" />
  </n-space>
</template>

<style scoped>
.bar { display: flex; justify-content: space-between; align-items: center; gap: 12px; flex-wrap: wrap; }
.hint { font-size: 12px; }
.search { width: 200px; }
:deep(.pickable) { cursor: pointer; }
</style>
