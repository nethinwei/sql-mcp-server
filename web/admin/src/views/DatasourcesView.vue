<script setup lang="ts">
import { computed, h, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  NAlert, NButton, NCard, NCheckbox, NCheckboxGroup, NCollapse, NCollapseItem, NDataTable, NEmpty, NForm, NFormItem,
  NGrid, NGi, NInput, NModal, NSelect, NSpace, NSpin, NSwitch, NTag, NText, NTooltip, useMessage, type DataTableColumns,
} from 'naive-ui'
import { useWorkspace, toEntityInput } from '@/stores/workspace'
import type { Action } from '@/gql/graphql'
import { nameError } from '@/lib/names'
import { entityByTable, tableKey as key, uniqueCandidates } from '@/lib/importer'
import { entityId } from '@/lib/entityRefs'
import {
  cancelScan, scanSchema, scans, scansOf, sync, tablesBySchema, type SchemaNode, type Table,
} from '@/lib/schemaScans'
import { can } from '@/api/session'

type Status = { label: string; type: 'success' | 'info' | 'warning' | 'default'; importable: boolean; drift: boolean }

const { t } = useI18n()
const ws = useWorkspace()
const message = useMessage()
const selectedDs = ref<string | null>(ws.datasources[0]?.name ?? null)
const cur = computed(() => (selectedDs.value ? scans[selectedDs.value] : undefined))
const entityCount = (ds: string) => {
  const n = ws.entities.filter((e) => (e.datasource ?? 'default') === ds).length
  return t('datasources.entityCount', { count: n }, n)
}

function selectDatasource(name: string) {
  selectedDs.value = name
}
const when = (at: string | null) => (at ? new Date(at).toLocaleString() : '')

// Selecting a datasource reads its schemas and what was scanned of them; a
// schema never scanned is scanned when it is opened, and again on rescan.
watch(selectedDs, (ds) => {
  if (!ds) return
  scansOf(ds)
  if (can('admin:write')) void sync(ds)
}, { immediate: true })

function onExpand(names: (string | number)[]) {
  const s = cur.value
  const ds = selectedDs.value
  if (!s || !ds) return
  for (const name of names) {
    const n = s.schemas.find((x) => x.name === name)
    if (n && !s.expanded.includes(n.name) && !n.scannedAt && !n.scanning) void scanSchema(ds, n)
  }
  s.expanded = names.map(String)
}

function rescan(n: SchemaNode) {
  if (!selectedDs.value) return
  if (!cur.value?.expanded.includes(n.name)) cur.value?.expanded.push(n.name)
  void scanSchema(selectedDs.value, n)
}

const schemaFilter = ref('')
const search = ref('')
const shownSchemas = computed(() => {
  const q = schemaFilter.value.trim().toLowerCase()
  return (cur.value?.schemas ?? []).filter((n) => !q || n.name.toLowerCase().includes(q))
})
const bySchema = computed(() => tablesBySchema(cur.value?.result ?? null))
const visible = (n: SchemaNode) => {
  const q = search.value.trim().toLowerCase()
  return (bySchema.value.get(n.name) ?? []).filter((tb) => !q || tb.table.toLowerCase().includes(q) || tb.description.toLowerCase().includes(q))
}
// Importing waits for the sync after the last scan: a scan may rename the
// candidates of other schemas, and after a failed sync they may be stale.
const blocked = computed(() => {
  const s = cur.value
  return !s || s.syncing || !!s.error || s.schemas.some((n) => n.scanning)
})
const checkedCount = computed(() => (cur.value?.schemas ?? []).reduce((sum, n) => sum + n.checked.length, 0))
const pagination = { pageSize: 50, showSizePicker: true, pageSizes: [20, 50, 100] }

// Which workspace entity reads each scanned table, resolved once for all of
// them rather than per row and render.
const owners = computed(() => {
  const s = cur.value
  return entityByTable(ws.entities, selectedDs.value ?? '', s?.result?.tables ?? [], s?.defaultSchema)
})
const inWorkspace = (tb: Table) => owners.value.get(key(tb))

/** Status of a table against the workspace (not just the published config). */
function statusOf(tb: Table): Status {
  const e = inWorkspace(tb)
  if (!e) return { label: t('datasources.state.new'), type: 'default', importable: true, drift: false }
  const declared = new Set((e.fields ?? []).map((f) => f.name))
  const columns = new Set(tb.columns.map((c) => c.name))
  const added = tb.columns.filter((c) => !declared.has(c.name)).length
  const missing = [...declared].filter((n) => !columns.has(n)).length
  if (added || missing) {
    return { label: t('datasources.state.drift', { added, missing }), type: 'warning', importable: false, drift: true }
  }
  return tb.status === 'NEW'
    ? { label: t('datasources.state.inWorkspace'), type: 'info', importable: false, drift: false }
    : { label: t('datasources.state.configured'), type: 'success', importable: false, drift: false }
}
const statuses = computed(() => {
  const out = new Map<string, Status>()
  for (const tb of cur.value?.result?.tables ?? []) out.set(key(tb), statusOf(tb))
  return out
})
const status = (tb: Table) => statuses.value.get(key(tb)) ?? statusOf(tb)

type Column = Table['columns'][number]

interface KeyTag {
  label: string
  type: 'default' | 'info' | 'success' | 'warning'
  title: string
}

// The primary key and foreign keys are shown on the columns they cover, like
// the column view of a database client: a table has one primary key, and each
// column of a foreign key references its own column, so the tags cannot be
// mixed up. The tooltip says what a foreign key writes on delete or update.
function columnKeys(tb: Table): Map<string, KeyTag[]> {
  const out = new Map<string, KeyTag[]>()
  const add = (column: string, tag: KeyTag) => out.set(column, [...(out.get(column) ?? []), tag])
  const pk = tb.keys.find((k) => k.primary)?.columns ?? []
  pk.forEach((column, i) => add(column, {
    label: pk.length > 1 ? `PK ${i + 1}/${pk.length}` : 'PK',
    type: 'info',
    title: pk.length > 1 ? t('datasources.compositeKey', { cols: pk.join(', '), n: i + 1 }) : t('datasources.primaryKey'),
  }))
  for (const fk of tb.foreignKeys) {
    const ref = fk.refSchema === tb.schema ? fk.refTable : `${fk.refSchema}.${fk.refTable}`
    const actions = [['onDelete', fk.onDelete], ['onUpdate', fk.onUpdate]].filter(([, a]) => a)
      .map(([on, a]) => t(`datasources.${on}`, { action: t(`datasources.fkAction.${a}`) }))
    fk.columns.forEach((column, i) => add(column, {
      label: `FK → ${ref}.${fk.refColumns[i]}`,
      type: actions.length ? 'warning' : 'default',
      title: [`${t('datasources.foreignKey')} ${fk.name}`, ...actions].join('\n'),
    }))
  }
  return out
}

type Index = Table['indexes'][number]

// The indexes are a table of their own under the columns, one index per row
// in the database's own terms (structure, key parts, predicate): a table may
// have several over overlapping columns, so they are not shown on the
// columns. A unique index says when it cannot identify a row.
function indexes(tb: Table) {
  if (!tb.indexes.length) return null
  const dash = () => h(NText, { depth: 3 }, () => '—')
  const uniqueness = (ix: Index) => {
    if (ix.primary) return t('datasources.primaryKey')
    if (!ix.unique) return dash()
    const reason = tb.keys.find((k) => k.name === ix.name)?.reason
    return reason
      ? h(NText, { depth: 3 }, () => t('datasources.uniqueNotIdentity', { reason: t(`datasources.keyReason.${reason}`) }))
      : t('datasources.unique')
  }
  const columns: DataTableColumns<Index> = [
    { title: t('datasources.index'), key: 'name', minWidth: 140, render: (ix) => h('span', { class: 'mono' }, ix.name) },
    { title: t('datasources.indexMethod'), key: 'method', width: 90, render: (ix) => h('span', { class: 'mono' }, ix.method) },
    {
      title: t('datasources.keyColumns'), key: 'parts', minWidth: 160,
      render: (ix) => (ix.parts.length
        ? h('span', { class: 'mono' }, ix.parts.map((p) => p || t('datasources.expression')).join(', '))
        : h(NText, { depth: 3 }, () => t('datasources.partsNotReported'))),
    },
    { title: t('datasources.uniqueness'), key: 'unique', minWidth: 120, render: uniqueness },
    {
      title: t('datasources.indexWhere'), key: 'where', ellipsis: { tooltip: true },
      render: (ix) => (ix.where ? h('span', { class: 'mono' }, ix.where) : dash()),
    },
  ]
  return h(NDataTable, { class: 'cols', size: 'small', bordered: false, columns, data: tb.indexes, rowKey: (ix: Index) => ix.name })
}

// The expanded table lists one column per row, like the column view of a
// database client, with the keys each column is part of.
function columnDetail(tb: Table): DataTableColumns<Column> {
  const keys = columnKeys(tb)
  return [
    { title: '#', key: 'index', width: 44, render: (_, i) => h(NText, { depth: 3 }, () => i + 1) },
    {
      title: t('datasources.columnName'), key: 'name', minWidth: 140,
      render: (c) => h('span', { class: 'mono' + (c.primaryKey ? ' strong' : '') }, c.name),
    },
    {
      title: t('datasources.columnType'), key: 'type', minWidth: 140,
      render: (c) => h(NText, { depth: 2, class: 'mono' }, () => c.type),
    },
    {
      title: t('datasources.notNull'), key: 'nullable', width: 72, align: 'center',
      render: (c) => (c.nullable ? h(NText, { depth: 3 }, () => '—') : '✓'),
    },
    {
      title: t('datasources.keys'), key: 'keys', minWidth: 160,
      render: (c) => h('div', { class: 'keys' }, (keys.get(c.name) ?? []).map((k) =>
        h(NTag, { size: 'small', bordered: false, type: k.type, title: k.title }, () => k.label))),
    },
    {
      title: t('datasources.description'), key: 'description', ellipsis: { tooltip: true },
      render: (c) => c.description || h(NText, { depth: 3 }, () => '—'),
    },
  ]
}

const columns = computed<DataTableColumns<Table>>(() => [
  { type: 'selection', disabled: (tb) => !status(tb).importable },
  {
    type: 'expand',
    renderExpand: (tb) => h('div', [
      h(NDataTable, {
        class: 'cols', size: 'small', bordered: false, columns: columnDetail(tb), data: tb.columns,
        rowKey: (c: Column) => c.name,
      }),
      indexes(tb),
      tb.sideEffects ? h(NText, { type: 'warning', class: 'notes' }, () => t('datasources.sideEffects')) : null,
    ]),
  },
  {
    title: t('datasources.table'), key: 'table', minWidth: 160,
    render: (tb) => h('span', { class: 'mono strong' }, tb.table),
  },
  { title: t('datasources.description'), key: 'description', ellipsis: { tooltip: true } },
  { title: t('datasources.columns'), key: 'cols', width: 70, render: (tb) => tb.columns.length },
  {
    title: t('datasources.relationships'), key: 'rels', width: 170,
    render: (tb) => tb.candidate.relationships.length
      ? h(NTooltip, null, {
          trigger: () => h(NText, { class: 'mono' }, () => tb.candidate.relationships.map((r) => r.target).join(', ')),
          default: () => tb.candidate.relationships.map((r) => `${r.cardinality} → ${r.target}`).join('\n'),
        })
      : h(NText, { depth: 3 }, () => '—'),
  },
  {
    title: t('datasources.status'), key: 'status', width: 170,
    render: (tb) => {
      const s = status(tb)
      return h(NTag, { size: 'small', type: s.type, bordered: false }, () => s.label)
    },
  },
  {
    title: '', key: 'action', width: 110,
    render: (tb) => status(tb).drift && can('admin:write')
      ? h(NButton, { size: 'tiny', onClick: () => openSync(tb) }, () => t('datasources.syncFields'))
      : null,
  },
])

// Import dialog: optionally grant the new entities to existing or new roles.
const importing = ref(false)
const grantRoles = ref<string[]>([])
const grantActions = ref<Action[]>(['READ'])
const grantable: Action[] = ['READ', 'AGGREGATE', 'CREATE', 'UPDATE', 'DELETE']
const roleOptions = computed(() => ws.roles.map((r) => ({ label: r.name, value: r.name })))
const roleError = computed(() => grantRoles.value
  .filter((r) => !ws.role(r))
  .map((r) => nameError(r, ws.roles.map((x) => x.name)))
  .find(Boolean) ?? null)

function openImport() {
  grantRoles.value = []
  grantActions.value = ['READ']
  importing.value = true
}

function addSelected() {
  const s = cur.value
  if (!s) return
  const checked = new Set(s.schemas.flatMap((n) => n.checked.map(String)))
  const chosen = (s.result?.tables ?? []).filter((tb) => checked.has(key(tb)))
  const candidates = uniqueCandidates(chosen.map((tb) => toEntityInput(tb.candidate)), ws.entities.map(entityId))
  const added = candidates.map((e) => e.name)
  const dropped = ws.importEntities(candidates, grantRoles.value, grantActions.value)
  for (const n of s.schemas) n.checked = []
  importing.value = false
  const note = dropped ? t('datasources.droppedNote', { dropped }) : ''
  message.success(grantRoles.value.length
    ? t('datasources.addedGranted', { count: added.length, roles: grantRoles.value.join(', ') }, added.length) + note
    : dropped
      ? t('datasources.addedDropped', { count: chosen.length, dropped }, chosen.length)
      : t('datasources.added', { count: chosen.length }, chosen.length))
}

// Field sync: a preview of the columns to add and the fields to remove, with
// what references each removed field, applied item by item.
interface SyncPlan {
  entity: string // the entity's ID
  added: Table['columns']
  removed: string[]
}
const syncing = ref<SyncPlan | null>(null)
const syncOpen = ref(false)
const syncAdd = ref<string[]>([])
const syncRemove = ref<string[]>([])
const hideNew = ref(false)

function openSync(tb: Table) {
  const e = inWorkspace(tb)
  if (!e) return
  const declared = new Set((e.fields ?? []).map((f) => f.name))
  const plan: SyncPlan = {
    entity: entityId(e),
    added: tb.columns.filter((c) => !declared.has(c.name)),
    removed: [...declared].filter((n) => !tb.columns.some((c) => c.name === n)),
  }
  syncing.value = plan
  syncAdd.value = plan.added.map((c) => c.name)
  syncRemove.value = [...plan.removed]
  hideNew.value = false
  syncOpen.value = true
}

/** References of a field to remove; grant field lists follow automatically. */
const references = (field: string) => (syncing.value ? ws.fieldReferences(syncing.value.entity, field) : [])

function applySync() {
  const plan = syncing.value
  if (!plan) return
  const added = plan.added.filter((c) => syncAdd.value.includes(c.name))
    .map((c) => (hideNew.value ? { name: c.name, exclude: true } : { name: c.name }))
  ws.syncFields(plan.entity, added, syncRemove.value)
  syncOpen.value = false
  message.success(t('datasources.synced', { name: ws.entityIndex.shortName(plan.entity), added: added.length, removed: syncRemove.value.length }))
}
</script>

<template>
  <n-space vertical :size="16">
    <n-grid :cols="4" :x-gap="12" :y-gap="12" responsive="screen" item-responsive>
      <n-gi v-for="d in ws.datasources" :key="d.name" span="4 m:2 l:1">
        <n-card size="small" hoverable class="ds" :class="{ active: selectedDs === d.name }" @click="selectDatasource(d.name)">
          <div class="ds-head">
            <n-text strong>{{ d.name }}</n-text>
            <n-tag size="tiny" :bordered="false">{{ d.driver }}</n-tag>
          </div>
          <div v-if="d.connections.length <= 1" class="ds-dsn mono">{{ d.dsn }}</div>
          <template v-else>
            <div v-for="c in d.connections" :key="c.name" class="ds-dsn mono" :title="c.dsn">
              {{ c.name }}<template v-if="c.role !== 'primary' || c.pooler"> ({{ [c.role, c.pooler].filter(Boolean).join(', ') }})</template>: {{ c.dsn }}
            </div>
            <n-text depth="3" class="ds-count">
              {{ t('datasources.routing', d.routing) }}<template v-if="d.readAfterWrite"> ·
                {{ t('datasources.readAfterWrite', { d: d.readAfterWrite }) }}</template>
            </n-text>
          </template>
          <n-text depth="3" class="ds-count">
            {{ entityCount(d.name) }}
          </n-text>
        </n-card>
      </n-gi>
    </n-grid>
    <n-alert type="default" :show-icon="false">{{ t('datasources.cliOnly') }}</n-alert>

    <n-card v-if="selectedDs && cur" size="small" :title="t('datasources.importTitle', { name: selectedDs })">
      <template #header-extra>
        <n-button size="small" :loading="cur.syncing" :disabled="!can('admin:write')"
          @click="sync(selectedDs, true)">
          {{ t('datasources.refreshSchemas') }}
        </n-button>
      </template>
      <n-alert v-if="cur.error" type="error" :show-icon="false">
        {{ cur.listed ? t('datasources.stale', { error: cur.error }) : cur.error }}
        <n-button size="tiny" text type="primary" @click="sync(selectedDs)">{{ t('datasources.retry') }}</n-button>
      </n-alert>
      <n-empty v-if="!cur.listed && !cur.error" :description="t(can('admin:write') ? 'datasources.listing' : 'datasources.scanHint')" />
      <template v-else-if="cur.listed">
        <div class="toolbar">
          <n-space :size="8">
            <n-input v-model:value="schemaFilter" size="small" :placeholder="t('datasources.filterSchemas')" clearable class="search" />
            <n-input v-model:value="search" size="small" :placeholder="t('datasources.searchTables')" clearable class="search" />
          </n-space>
          <n-button type="primary" size="small" :disabled="!checkedCount || blocked" @click="openImport">
            {{ t('datasources.addSelected', { count: checkedCount }) }}
          </n-button>
        </div>
        <n-collapse :expanded-names="cur.expanded" @update:expanded-names="onExpand">
          <n-collapse-item v-for="n in shownSchemas" :key="n.name" :name="n.name">
            <template #header>
              <span class="mono strong">{{ n.name || t('datasources.defaultSchema') }}</span>
              <n-tag v-if="n.name && n.name === cur.defaultSchema" size="tiny" :bordered="false" class="schema-tag">
                {{ t('datasources.defaultSchema') }}
              </n-tag>
            </template>
            <template #header-extra>
              <n-space :size="8" align="center" :wrap="false">
                <n-text depth="3" class="schema-meta">
                  {{ n.scannedAt ? t('datasources.scannedMeta', { count: n.tables ?? 0, at: when(n.scannedAt) }) : t('datasources.notScanned') }}
                </n-text>
                <n-button v-if="n.scanning" size="tiny" @click.stop="cancelScan(n)">{{ t('datasources.cancelScan') }}</n-button>
                <n-button v-else size="tiny" :disabled="!can('admin:write')" @click.stop="rescan(n)">
                  {{ n.scannedAt ? t('datasources.rescan') : t('datasources.scan') }}
                </n-button>
              </n-space>
            </template>
            <n-alert v-if="n.error" type="error" :show-icon="false">{{ n.error }}</n-alert>
            <n-spin :show="n.scanning">
              <n-empty v-if="!n.scannedAt" :description="t(n.scanning ? 'datasources.scanning' : 'datasources.scanHint')" />
              <n-data-table v-else v-model:checked-row-keys="n.checked" :columns="columns" :data="visible(n)" :row-key="key"
                size="small" :pagination="pagination" />
            </n-spin>
          </n-collapse-item>
        </n-collapse>
      </template>
    </n-card>

    <n-modal v-model:show="syncOpen" preset="card" class="dialog" :title="t('datasources.syncTitle', { name: syncing ? ws.entityIndex.shortName(syncing.entity) : '' })">
      <template v-if="syncing">
        <n-space vertical :size="14">
          <div v-if="syncing.added.length">
            <n-text strong>{{ t('datasources.syncAdded', { n: syncing.added.length }) }}</n-text>
            <n-checkbox-group v-model:value="syncAdd" class="sync-list">
              <n-checkbox v-for="c in syncing.added" :key="c.name" :value="c.name">
                <span class="mono">{{ c.name }}</span>
                <n-text depth="3" class="sync-meta">{{ c.type }}{{ c.description ? ' · ' + c.description : '' }}</n-text>
              </n-checkbox>
            </n-checkbox-group>
            <div class="sync-switch">
              <n-switch v-model:value="hideNew" size="small" />
              <n-text class="sync-meta">{{ t('datasources.syncHideNew') }}</n-text>
            </div>
          </div>
          <div v-if="syncing.removed.length">
            <n-text strong>{{ t('datasources.syncRemoved', { n: syncing.removed.length }) }}</n-text>
            <n-checkbox-group v-model:value="syncRemove" class="sync-list">
              <n-checkbox v-for="f in syncing.removed" :key="f" :value="f">
                <span class="mono">{{ f }}</span>
                <n-tag v-for="r in references(f)" :key="r.kind + r.name" size="tiny" :bordered="false" class="ref"
                  :type="r.kind === 'role' || r.kind === 'user' ? 'default' : 'warning'">
                  {{ t(`datasources.ref.${r.kind}`, { name: r.kind === 'role' || r.kind === 'user' ? r.name : ws.entityIndex.shortName(r.name) }) }}
                </n-tag>
              </n-checkbox>
            </n-checkbox-group>
            <n-text depth="3" class="sync-meta">{{ t('datasources.syncRefsHint') }}</n-text>
          </div>
        </n-space>
      </template>
      <template #footer>
        <n-space justify="end">
          <n-button @click="syncOpen = false">{{ t('common.cancel') }}</n-button>
          <n-button type="primary" :disabled="!syncAdd.length && !syncRemove.length" @click="applySync">
            {{ t('datasources.syncApply', { added: syncAdd.length, removed: syncRemove.length }) }}
          </n-button>
        </n-space>
      </template>
    </n-modal>

    <n-modal v-model:show="importing" preset="card" class="dialog"
      :title="t('datasources.importDialog', { count: checkedCount }, checkedCount)">
      <n-form label-placement="top">
        <n-form-item :label="t('datasources.grantRoles')" :feedback="roleError ?? ''"
          :validation-status="roleError ? 'error' : undefined">
          <n-select v-model:value="grantRoles" multiple filterable tag :options="roleOptions"
            :placeholder="t('datasources.grantRolesPlaceholder')" />
        </n-form-item>
        <n-form-item v-if="grantRoles.length" :label="t('datasources.grantActions')">
          <n-checkbox-group v-model:value="grantActions">
            <n-space>
              <n-checkbox v-for="a in grantable" :key="a" :value="a" :label="t(`grants.actions.${a}`)" />
            </n-space>
          </n-checkbox-group>
        </n-form-item>
      </n-form>
      <n-text v-if="!grantRoles.length" depth="3" class="grant-hint">{{ t('datasources.grantHint') }}</n-text>
      <template #footer>
        <n-space justify="end">
          <n-button @click="importing = false">{{ t('common.cancel') }}</n-button>
          <n-button type="primary" :disabled="Boolean(roleError) || (grantRoles.length > 0 && !grantActions.length)"
            @click="addSelected">{{ t('common.confirm') }}</n-button>
        </n-space>
      </template>
    </n-modal>
  </n-space>
</template>

<style scoped>
.keys { display: flex; flex-wrap: wrap; gap: 4px; }
.notes { display: block; margin: 6px 0 6px 36px; font-size: 12px; }
.ds { cursor: pointer; height: 100%; }
.ds.active { border-color: #2f6fed; box-shadow: 0 0 0 1px #2f6fed inset; }
.ds-head { display: flex; align-items: center; gap: 8px; margin-bottom: 4px; }
.ds-dsn { font-size: 12px; opacity: .65; word-break: break-all; margin-bottom: 4px; }
.ds-count { display: block; font-size: 12px; }
.schema-tag { margin-left: 8px; }
.schema-meta { font-size: 12px; }
.toolbar { display: flex; justify-content: space-between; align-items: center; gap: 12px; flex-wrap: wrap; margin-bottom: 10px; }
.search { width: 240px; }
.grant-hint { font-size: 13px; }
.sync-list { display: flex; flex-direction: column; gap: 6px; margin-top: 6px; max-height: 260px; overflow: auto; }
.sync-meta { margin-left: 6px; font-size: 12px; }
.sync-switch { display: flex; align-items: center; gap: 4px; margin-top: 8px; }
.ref { margin-left: 6px; }
:deep(.cols) { margin: 2px 0 2px 36px; width: auto; }
:deep(.cols + .cols) { margin-top: 12px; }
:deep(.strong) { font-weight: 600; }
:deep(.schema) { margin-left: 6px; font-size: 12px; }
</style>
