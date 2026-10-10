<script setup lang="ts">
import { computed, h, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import {
  NAlert, NBadge, NButton, NCard, NCollapse, NCollapseItem, NDataTable, NInput, NRadioButton, NRadioGroup,
  NSwitch, NTag, NText, NTooltip, useThemeVars, type DataTableColumns, type DataTableRowKey,
} from 'naive-ui'
import { CatalogQuery, CatalogRefsQuery } from '@/api/ops'
import type { CatalogFilter, CatalogQuery as CatalogResult, TableStatus } from '@/gql/graphql'
import { useWorkspace } from '@/stores/workspace'
import { workspace } from '@/lib/workspaceSync'
import { can } from '@/api/session'
import { scans } from '@/lib/schemaScans'
import { patterns, scopeOf, type Plan } from '@/lib/plan'
import { planDraft } from '@/lib/planDraft'
import {
  checkPage, confirm, emptySelection, filterKey, hiddenSelected, isSelected, needsConfirm, planOf, selectMatched,
  selectedCount, withTotal, type Selection,
} from '@/lib/selection'
import DatasourceSummary from '@/components/catalog/DatasourceSummary.vue'
import SchemaPanel from '@/components/catalog/SchemaPanel.vue'
import CatalogRowDetail from '@/components/catalog/CatalogRowDetail.vue'
import RelationshipsPopover from '@/components/catalog/RelationshipsPopover.vue'
import ImportDialog from '@/components/catalog/ImportDialog.vue'
import SyncFieldsDialog from '@/components/catalog/SyncFieldsDialog.vue'
import SelectionDrawer from '@/components/catalog/SelectionDrawer.vue'
import PlanPreviewDialog from '@/components/plan/PlanPreviewDialog.vue'

// Finding and importing tables across every datasource: the server keeps
// what scans found and searches it a page at a time, so hundreds of schemas
// are one table to filter. Tables are picked across pages, or every table a
// filter matches is selected as a snapshot of the filter (with the tables
// unpicked from it); the selection is imported, or handed to the plan page,
// as a plan that is previewed before it enters the workspace.
type Row = CatalogResult['catalog']['tables'][number]

const { t } = useI18n()
const ws = useWorkspace()
const router = useRouter()
const route = useRoute()
const writable = computed(() => can('admin:write'))
const theme = useThemeVars()

const datasource = ref<string | null>(null)
const schemaText = ref('')
const search = ref('')
// The pending page links here with a status or drift filter set.
const status = ref<TableStatus | 'ALL'>((['NEW', 'WORKSPACE', 'PUBLISHED'] as const)
  .find((s) => s === route.query.status) ?? 'ALL')
const drift = ref(route.query.drift === '1')
const page = reactive({ page: 1, pageSize: 50 })

const filter = computed<CatalogFilter>(() => ({
  datasources: datasource.value ? [datasource.value] : undefined,
  schemas: patterns(schemaText.value).length ? patterns(schemaText.value) : undefined,
  search: search.value.trim() || undefined,
  status: status.value === 'ALL' ? undefined : [status.value],
  drift: drift.value || undefined,
}))

const result = ref<CatalogResult['catalog'] | null>(null)
/** The filter the shown rows answer. */
const resultFor = ref('')
const loading = ref(false)
const error = ref('')
let seq = 0
let timer: ReturnType<typeof setTimeout> | undefined

async function load() {
  if (!writable.value) return
  const mine = ++seq
  const key = filterKey(filter.value)
  loading.value = true
  try {
    const { catalog } = await workspace.read(CatalogQuery, { input: {
      filter: filter.value, offset: (page.page - 1) * page.pageSize, limit: page.pageSize,
    } })
    if (mine !== seq) return
    result.value = catalog
    resultFor.value = key + page.page
    // A filter selection follows what its filter matches now.
    if (selection.value.mode === 'filter' && filterKey(selection.value.filter) === key) {
      selection.value = withTotal(selection.value, catalog.total)
    }
    error.value = ''
  } catch (e) {
    if (mine === seq) error.value = e instanceof Error ? e.message : String(e)
  } finally {
    if (mine === seq) loading.value = false
  }
}

const reload = () => {
  clearTimeout(timer)
  timer = setTimeout(load, 250)
}
watch(filter, () => {
  page.page = 1
  reload()
}, { deep: true })
watch(() => [page.page, page.pageSize], reload)
// New scans and workspace edits refresh the rows in place.
watch(() => [scans.version, ws.contextKey], reload, { immediate: true })

/**
 * The rows shown, only while they answer the filter and page on screen: the
 * rows of an older filter are never selected by checking a page.
 */
const current = computed(() => resultFor.value === filterKey(filter.value) + page.page)
const rows = computed(() => (current.value ? result.value?.tables ?? [] : []))

// Selection.
const selection = ref<Selection>(emptySelection())
const count = computed(() => selectedCount(selection.value))
const sameFilter = computed(() => selection.value.mode === 'filter' && filterKey(selection.value.filter) === filterKey(filter.value))
const seenElsewhere = computed(() => selection.value.mode === 'filter' && !sameFilter.value)
const pageChecked = computed(() => rows.value.filter((r) => isSelected(selection.value, filter.value, r.ref)).map((r) => r.ref))
const pageFull = computed(() => rows.value.length > 0 && pageChecked.value.length === rows.value.length)

function onCheck(keys: DataTableRowKey[]) {
  selection.value = checkPage(selection.value, filter.value, rows.value.map((r) => r.ref), new Set(keys.map(String)))
}
/** Reads what a filter selection matches now, wherever the page is. */
async function recount() {
  const s = selection.value
  if (s.mode !== 'filter') return
  const { catalog } = await workspace.read(CatalogQuery, { input: { filter: s.filter, offset: 0, limit: 0 } })
  selection.value = withTotal(selection.value, catalog.total)
}
const toConfirm = computed(() => needsConfirm(selection.value))

async function act(fn: () => void) {
  await recount()
  if (!toConfirm.value) fn()
}

function selectAllMatched() {
  if (current.value && result.value) selection.value = selectMatched(filter.value, result.value.total)
}
function clearSelection() {
  selection.value = emptySelection()
}

/**
 * Every ref the filter matches against the workspace (statuses depend on
 * it), read for tables picked one by one.
 */
const matched = ref<{ key: string; refs: string[] } | null>(null)
let matchedSeq = 0
const matchedKey = () => `${filterKey(filter.value)}\u0000${ws.contextKey}`
watch([() => (selection.value.mode === 'refs' ? selection.value.refs.size : 0), filter, () => ws.contextKey], async ([n]) => {
  const key = matchedKey()
  if (!n || matched.value?.key === key) return
  const mine = ++matchedSeq
  try {
    const { catalogRefs } = await workspace.read(CatalogRefsQuery, { filter: filter.value })
    if (mine === matchedSeq) matched.value = { key, refs: catalogRefs }
  } catch {
    if (mine === matchedSeq) matched.value = null
  }
}, { deep: true })
const hidden = computed(() => hiddenSelected(selection.value,
  matched.value?.key === matchedKey() ? matched.value.refs : null))

function describe(f: CatalogFilter) {
  const parts = [
    f.datasources?.length ? f.datasources.join(',') : t('catalog.allDatasources'),
    f.schemas?.length ? `${t('catalog.schema')} ${f.schemas.join(',')}` : '',
    f.search ? `“${f.search}”` : '',
    f.status?.length ? f.status.map((s) => t(`catalog.status.${s.toLowerCase()}`)).join('/') : '',
    f.drift ? t('catalog.driftShort') : '',
  ]
  return parts.filter(Boolean).join(' · ')
}
const selectionFilter = computed(() => (selection.value.mode === 'filter' ? describe(selection.value.filter) : ''))
function backToSelection() {
  const s = selection.value
  if (s.mode !== 'filter') return
  datasource.value = s.filter.datasources?.[0] ?? null
  schemaText.value = (s.filter.schemas ?? []).join(', ')
  search.value = s.filter.search ?? ''
  status.value = (s.filter.status?.[0] as TableStatus | undefined) ?? 'ALL'
  drift.value = !!s.filter.drift
}

const statusTag: Record<TableStatus, { type: 'default' | 'info' | 'success'; key: string }> = {
  NEW: { type: 'default', key: 'catalog.status.new' },
  WORKSPACE: { type: 'info', key: 'catalog.status.workspace' },
  PUBLISHED: { type: 'success', key: 'catalog.status.published' },
}

// Field sync of a configured table whose fields differ from its columns.
const syncing = reactive({ show: false, ref: null as string | null, entity: null as string | null })
function openSync(r: Row) {
  Object.assign(syncing, { show: true, ref: r.ref, entity: r.entity ?? null })
}

function statusCell(r: Row) {
  const s = statusTag[r.status]
  const tags = [h(NTag, { size: 'small', type: s.type, bordered: false }, () => t(s.key))]
  if (r.added.length || r.missing.length) {
    const label = `+${r.added.length} −${r.missing.length}`
    tags.push(h(NTooltip, null, {
      trigger: () => writable.value
        ? h(NButton, { size: 'tiny', type: 'warning', secondary: true, onClick: () => openSync(r),
          'aria-label': t('catalog.syncLabel', { ref: r.ref }) }, () => label)
        : h(NTag, { size: 'small', type: 'warning', bordered: false }, () => label),
      default: () => t('catalog.driftHint', { added: r.added.join(', ') || '—', missing: r.missing.join(', ') || '—' }),
    }))
  }
  if (!r.hasKey) tags.push(h(NTag, { size: 'small', bordered: false, title: t('catalog.noKeyHint') }, () => t('catalog.noKey')))
  return h('div', { class: 'tags' }, tags)
}

const columns = computed<DataTableColumns<Row>>(() => [
  { type: 'selection', disabled: () => seenElsewhere.value },
  { type: 'expand', renderExpand: (r) => h(CatalogRowDetail, { tableRef: r.ref }) },
  {
    title: t('datasources.table'), key: 'table', minWidth: 220, ellipsis: { tooltip: true },
    render: (r) => h('span', { class: 'mono' }, [
      datasource.value ? null : h(NText, { depth: 3 }, () => `${r.datasource}.`),
      h(NText, { depth: 2 }, () => (r.schema ? `${r.schema}.` : '')),
      h('span', { class: 'strong' }, r.table),
    ]),
  },
  { title: t('datasources.description'), key: 'description', ellipsis: { tooltip: true } },
  { title: t('datasources.columns'), key: 'columns', width: 64 },
  {
    title: t('catalog.links'), key: 'rels', width: 100,
    render: (r) => h(RelationshipsPopover, { tableRef: r.ref, outgoing: r.foreignKeys, incoming: r.referencedBy }),
  },
  { title: t('datasources.status'), key: 'status', width: 240, render: statusCell },
])

const coverage = computed(() => (result.value?.coverage ?? []).filter((c) => !datasource.value || c.datasource === datasource.value))
const scannedSchemas = computed(() => coverage.value.reduce((n, c) => n + c.scanned, 0))
const unscannedSchemas = computed(() => coverage.value.reduce((n, c) => n + c.unscanned.length, 0))
const oldestScan = computed(() => coverage.value.map((c) => c.oldestScan).filter(Boolean).sort()[0] ?? null)
const counts = computed(() => result.value?.counts)
const panel = ref<string[]>([])
watch(datasource, (ds) => { panel.value = ds && !scannedSchemas.value ? ['schemas'] : [] })

// Dialogs: import options, then the plan preview.
const importing = ref(false)
const previewing = ref(false)
const reviewing = ref(false)
const plan = ref<Plan | null>(null)
const applied = ref<{ added: number; changed: number } | null>(null)
const selectionPlan = computed(() => planOf(selection.value))

function previewPlan(p: Plan) {
  plan.value = p
  previewing.value = true
}

function onApplied(r: { added: string[]; changed: number }) {
  applied.value = { added: r.added.length, changed: r.changed }
  clearSelection()
}

/** Sync the fields of every table the filter matches whose columns differ. */
function syncAll() {
  previewPlan({ scope: { ...scopeOf(filter.value), drift: true }, import: false, sync: { add: true, remove: true } })
}

function toPlanPage(withSelection: boolean) {
  planDraft.value = withSelection ? selectionPlan.value : planOf(selectMatched(filter.value, 0))
  void router.push({ name: 'plans' })
}
</script>

<template>
  <div class="page">
    <datasource-summary :selected="datasource" :coverage="result?.coverage ?? []" @select="datasource = $event" />
    <n-text depth="3" class="meta">{{ t('datasources.cliOnly') }}</n-text>
    <n-alert v-if="!writable" type="info" :show-icon="false">{{ t('catalog.readOnly') }}</n-alert>
    <n-alert v-if="applied" type="success" closable @close="applied = null">
      {{ t('catalog.applied', { added: applied.added, changed: applied.changed }) }}
      <div class="alert-actions">
        <n-button size="small" @click="router.push({ name: 'entities' })">{{ t('catalog.nextEntities') }}</n-button>
        <n-button size="small" @click="router.push({ name: 'roles' })">{{ t('catalog.nextRoles') }}</n-button>
        <n-button size="small" type="primary" @click="router.push({ name: 'review' })">{{ t('catalog.nextReview') }}</n-button>
      </div>
    </n-alert>

    <n-card v-if="datasource" size="small">
      <n-collapse v-model:expanded-names="panel">
        <n-collapse-item :title="t('catalog.schemasOf', { name: datasource })" name="schemas">
          <schema-panel :datasource="datasource" />
        </n-collapse-item>
      </n-collapse>
    </n-card>

    <n-card v-if="writable" size="small" :title="t('catalog.title')">
      <template #header-extra>
        <n-button size="small" @click="toPlanPage(false)">{{ t('catalog.planFromFilter') }}</n-button>
      </template>
      <div class="toolbar">
        <div class="group">
          <n-input v-model:value="schemaText" size="small" clearable class="search" :placeholder="t('catalog.schemaPatterns')"
            :input-props="{ 'aria-label': t('catalog.schemaPatterns') }" />
          <n-input v-model:value="search" size="small" clearable class="search" :placeholder="t('datasources.searchTables')"
            :input-props="{ 'aria-label': t('datasources.searchTables') }" />
        </div>
        <div class="group">
          <n-radio-group v-model:value="status" size="small" :aria-label="t('datasources.status')">
            <n-radio-button value="ALL">{{ t('catalog.all') }} {{ counts?.all ?? '' }}</n-radio-button>
            <n-radio-button value="NEW">{{ t('catalog.status.new') }} {{ counts?.new ?? '' }}</n-radio-button>
            <n-radio-button value="WORKSPACE">{{ t('catalog.status.workspace') }} {{ counts?.workspace ?? '' }}</n-radio-button>
            <n-radio-button value="PUBLISHED">{{ t('catalog.status.published') }} {{ counts?.published ?? '' }}</n-radio-button>
          </n-radio-group>
          <label class="switch">
            <n-switch v-model:value="drift" size="small" />
            <n-text class="meta">{{ t('catalog.driftOnly', { n: counts?.drift ?? 0 }) }}</n-text>
          </label>
          <n-button size="small" :disabled="!counts?.drift" @click="syncAll">
            {{ t('catalog.syncAll', { n: counts?.drift ?? 0 }) }}
          </n-button>
        </div>
      </div>
      <n-text depth="3" class="meta scope">
        {{ t('catalog.scope', { scanned: scannedSchemas }) }}<template v-if="oldestScan">
          · {{ t('catalog.oldestScan', { at: new Date(oldestScan).toLocaleString() }) }}</template><template v-if="unscannedSchemas">
          · <n-button text size="tiny" type="warning" :disabled="!datasource" @click="panel = ['schemas']">
            {{ t('catalog.unscannedLeft', { n: unscannedSchemas }) }}</n-button>
          <template v-if="!datasource">{{ t('catalog.pickDatasource') }}</template>
        </template>
      </n-text>
      <n-alert v-if="error" type="error" :show-icon="false" class="gap">{{ error }}</n-alert>
      <n-alert v-else-if="result && !scannedSchemas" type="info" :show-icon="false" class="gap">{{ t('catalog.nothingScanned') }}</n-alert>
      <div v-if="toConfirm && selection.mode === 'filter'" class="notice warn" role="alert">
        <n-text>{{ t('catalog.countChanged', { from: selection.confirmed, to: selection.total }) }}</n-text>
        <n-button text type="primary" size="small" @click="selection = confirm(selection)">{{ t('catalog.confirmCount') }}</n-button>
      </div>
      <div v-if="seenElsewhere" class="notice" role="status">
        <n-text>{{ t('catalog.selectionElsewhere', { n: count, filter: selectionFilter }) }}</n-text>
        <n-button text type="primary" size="small" @click="backToSelection">{{ t('catalog.backToSelection') }}</n-button>
      </div>
      <div v-else-if="selection.mode === 'filter'" class="notice" role="status">
        <n-text>{{ t('catalog.allMatchedSelected', { n: count }) }}</n-text>
        <n-button text type="primary" size="small" @click="clearSelection">{{ t('catalog.clearSelection') }}</n-button>
      </div>
      <div v-else-if="pageFull && (result?.total ?? 0) > rows.length" class="notice" role="status">
        <n-text>{{ t('catalog.pageSelected', { n: rows.length }) }}</n-text>
        <n-button text type="primary" size="small" :disabled="!current || loading" @click="selectAllMatched">
          {{ t('catalog.selectAllMatched', { n: result?.total ?? 0 }) }}
        </n-button>
      </div>
      <n-data-table remote size="small" :columns="columns" :data="rows" :row-key="(r: Row) => r.ref" :loading="loading || !current"
        :checked-row-keys="pageChecked" @update:checked-row-keys="onCheck"
        :pagination="{ page: page.page, pageSize: page.pageSize, itemCount: result?.total ?? 0, showSizePicker: true,
          pageSizes: [20, 50, 100, 200], onUpdatePage: (p: number) => (page.page = p),
          onUpdatePageSize: (s: number) => { page.pageSize = s; page.page = 1 } }" />
    </n-card>

    <div v-if="count" class="selection" role="region" :aria-label="t('catalog.selected', { n: count })"
      :style="{ background: theme.cardColor, border: `1px solid ${theme.borderColor}`, boxShadow: theme.boxShadow2 }">
      <div class="group">
        <n-badge :value="count" :max="999999" type="info" />
        <n-text>{{ t('catalog.selected', { n: count }) }}</n-text>
        <n-text v-if="hidden.length" type="warning">{{ t('catalog.hiddenSelected', { n: hidden.length }) }}</n-text>
        <n-button text size="small" type="primary" @click="reviewing = true">{{ t('catalog.viewSelected') }}</n-button>
        <n-button text size="small" @click="clearSelection">{{ t('catalog.clearSelection') }}</n-button>
      </div>
      <div class="group">
        <n-text v-if="toConfirm" type="warning" class="meta">{{ t('catalog.confirmFirst') }}</n-text>
        <n-button size="small" :disabled="toConfirm" @click="act(() => toPlanPage(true))">{{ t('catalog.planFromSelection') }}</n-button>
        <n-button size="small" type="primary" :disabled="toConfirm" @click="act(() => (importing = true))">
          {{ t('catalog.importSelected', { n: count }) }}
        </n-button>
      </div>
    </div>

    <import-dialog v-model:show="importing" :base="selectionPlan" :count="count" @plan="previewPlan" />
    <plan-preview-dialog v-model:show="previewing" :plan="plan" @applied="onApplied" />
    <sync-fields-dialog v-model:show="syncing.show" :table-ref="syncing.ref" :entity="syncing.entity" />
    <selection-drawer v-model:show="reviewing" :selection="selection" :describe="selectionFilter"
      @update="(s) => (selection = s)" />
  </div>
</template>

<style scoped>
.page { display: flex; flex-direction: column; gap: 16px; padding-bottom: 64px; }
.toolbar { display: flex; justify-content: space-between; align-items: center; gap: 12px; flex-wrap: wrap; margin-bottom: 8px; }
.group { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.switch { display: inline-flex; align-items: center; gap: 6px; cursor: pointer; }
.search { width: 220px; }
.meta { font-size: 12px; }
.scope { display: block; margin-bottom: 8px; }
.gap { margin-bottom: 8px; }
.notice.warn { background: rgba(240, 160, 32, .14); }
.notice { display: flex; gap: 8px; align-items: center; justify-content: center; padding: 6px; margin-bottom: 6px;
  background: rgba(47, 111, 237, .08); border-radius: 4px; font-size: 13px; }
.selection {
  position: sticky; bottom: 0; z-index: 5; display: flex; justify-content: space-between; align-items: center; gap: 12px;
  flex-wrap: wrap; padding: 10px 16px; border-radius: 6px;
}
:deep(.tags) { display: flex; flex-wrap: wrap; gap: 4px; align-items: center; }
:deep(.strong) { font-weight: 600; }
</style>
