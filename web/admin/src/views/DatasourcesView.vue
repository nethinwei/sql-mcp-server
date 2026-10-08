<script setup lang="ts">
import { computed, h, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  NAlert, NButton, NCard, NCheckbox, NCheckboxGroup, NDataTable, NEmpty, NForm, NFormItem, NGrid, NGi, NInput,
  NModal, NSelect, NSpace, NSwitch, NTag, NText, NTooltip, useMessage, type DataTableColumns, type DataTableRowKey,
} from 'naive-ui'
import { useWorkspace, toEntityInput } from '@/stores/workspace'
import { run } from '@/api/client'
import { SchemaImportQuery } from '@/api/ops'
import type { Action, SchemaImportQuery as SchemaImportResult } from '@/gql/graphql'
import { nameError } from '@/lib/names'
import { readsTable, uniqueCandidates } from '@/lib/importer'
import { can } from '@/api/session'

type Table = SchemaImportResult['schemaImport']['tables'][number]
type Status = { label: string; type: 'success' | 'info' | 'warning' | 'default'; importable: boolean; drift: boolean }

const { t } = useI18n()
const ws = useWorkspace()
const message = useMessage()
// Each datasource keeps its own scan: schemas, results, search and selection,
// so switching back and forth loses nothing, and a scan that returns after
// switching fills in its own datasource only.
interface Scan {
  schemas: string
  tables: Table[]
  defaultSchema: string | null
  scanned: boolean
  scanning: boolean
  checked: DataTableRowKey[]
  search: string
  /** Bumped per request; a response for an older request is dropped. */
  seq: number
}
const scans = reactive<Record<string, Scan>>({})
function scanOf(ds: string): Scan {
  scans[ds] ??= {
    schemas: '', tables: [], defaultSchema: null, scanned: false, scanning: false, checked: [], search: '', seq: 0,
  }
  return scans[ds]
}
const selectedDs = ref<string | null>(ws.datasources[0]?.name ?? null)
if (selectedDs.value) scanOf(selectedDs.value)
const cur = computed(() => (selectedDs.value ? scans[selectedDs.value] : undefined))
const entityCount = (ds: string) => {
  const n = ws.entities.filter((e) => (e.datasource ?? 'default') === ds).length
  return t('datasources.entityCount', { count: n }, n)
}

function selectDatasource(name: string) {
  scanOf(name)
  selectedDs.value = name
}

async function scan() {
  const ds = selectedDs.value
  if (!ds) return
  const s = scanOf(ds)
  const mine = ++s.seq
  s.scanning = true
  try {
    const list = s.schemas.split(',').map((x) => x.trim()).filter(Boolean)
    const data = await run(SchemaImportQuery, { datasource: ds, schemas: list.length ? list : null })
    if (mine !== s.seq) return
    s.tables = data.schemaImport.tables
    s.defaultSchema = data.schemaImport.defaultSchema ?? null
    s.scanned = true
    s.checked = []
  } catch (e) {
    if (mine === s.seq) message.error(e instanceof Error ? e.message : String(e))
  } finally {
    if (mine === s.seq) s.scanning = false
  }
}

const key = (tb: Table) => `${tb.schema}.${tb.table}`
const inWorkspace = (tb: Table) =>
  ws.entities.find((e) => readsTable(e, selectedDs.value ?? '', tb, cur.value?.tables ?? [], cur.value?.defaultSchema))

/** Status of a table against the workspace (not just the published config). */
function status(tb: Table): Status {
  const e = inWorkspace(tb)
  if (!e) return { label: t('datasources.state.new'), type: 'default', importable: true, drift: false }
  const declared = new Set((e.fields ?? []).map((f) => f.name))
  const added = tb.columns.filter((c) => !declared.has(c.name)).length
  const missing = [...declared].filter((n) => !tb.columns.some((c) => c.name === n)).length
  if (added || missing) {
    return { label: t('datasources.state.drift', { added, missing }), type: 'warning', importable: false, drift: true }
  }
  return tb.status === 'NEW'
    ? { label: t('datasources.state.inWorkspace'), type: 'info', importable: false, drift: false }
    : { label: t('datasources.state.configured'), type: 'success', importable: false, drift: false }
}

const visible = computed(() => {
  const q = (cur.value?.search ?? '').trim().toLowerCase()
  return (cur.value?.tables ?? []).filter((tb) => !q || tb.table.toLowerCase().includes(q) || tb.description.toLowerCase().includes(q))
})

type Column = Table['columns'][number]

// The expanded table lists one column per row, like the column view of a
// database client.
const columnDetail = computed<DataTableColumns<Column>>(() => [
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
    title: t('datasources.primaryKey'), key: 'primaryKey', width: 72, align: 'center',
    render: (c) => (c.primaryKey ? h(NTag, { size: 'small', type: 'info', bordered: false }, () => 'PK') : null),
  },
  {
    title: t('datasources.description'), key: 'description', ellipsis: { tooltip: true },
    render: (c) => c.description || h(NText, { depth: 3 }, () => '—'),
  },
])

const columns = computed<DataTableColumns<Table>>(() => [
  { type: 'selection', disabled: (tb) => !status(tb).importable },
  {
    type: 'expand',
    renderExpand: (tb) =>
      h(NDataTable, {
        class: 'cols', size: 'small', bordered: false, columns: columnDetail.value, data: tb.columns,
        rowKey: (c: Column) => c.name,
      }),
  },
  {
    title: t('datasources.table'), key: 'table', minWidth: 160,
    render: (tb) => h('div', [
      h('span', { class: 'mono strong' }, tb.table),
      h(NText, { depth: 3, class: 'schema' }, () => tb.schema),
    ]),
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
  const chosen = s.tables.filter((tb) => s.checked.includes(key(tb)))
  const candidates = uniqueCandidates(chosen.map((tb) => toEntityInput(tb.candidate)), ws.entities.map((e) => e.name))
  const added = candidates.map((e) => e.name)
  const dropped = ws.importEntities(candidates, grantRoles.value, grantActions.value)
  s.checked = []
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
  entity: string
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
    entity: e.name,
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
  message.success(t('datasources.synced', { name: plan.entity, added: added.length, removed: syncRemove.value.length }))
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
          <div class="ds-dsn mono">{{ d.dsn }}</div>
          <n-text depth="3" class="ds-count">
            {{ entityCount(d.name) }}
          </n-text>
        </n-card>
      </n-gi>
    </n-grid>
    <n-alert type="default" :show-icon="false">{{ t('datasources.cliOnly') }}</n-alert>

    <n-card v-if="selectedDs && cur" size="small" :title="t('datasources.importTitle', { name: selectedDs })">
      <template #header-extra>
        <n-space :wrap="false">
          <n-input v-model:value="cur.schemas" size="small" :placeholder="t('datasources.schemas')" class="schemas" />
          <n-button size="small" type="primary" :loading="cur.scanning" :disabled="!can('admin:write')" @click="scan">
            {{ cur.scanned ? t('datasources.rescan') : t('datasources.scan') }}
          </n-button>
        </n-space>
      </template>
      <n-empty v-if="!cur.scanned" :description="t('datasources.scanHint')" />
      <template v-else>
        <div class="toolbar">
          <n-input v-model:value="cur.search" size="small" :placeholder="t('datasources.searchTables')" clearable class="search" />
          <n-button type="primary" size="small" :disabled="!cur.checked.length" @click="openImport">
            {{ t('datasources.addSelected', { count: cur.checked.length }) }}
          </n-button>
        </div>
        <n-data-table v-model:checked-row-keys="cur.checked" :columns="columns" :data="visible" :row-key="key"
          size="small" :max-height="560" />
      </template>
    </n-card>

    <n-modal v-model:show="syncOpen" preset="card" class="dialog" :title="t('datasources.syncTitle', { name: syncing?.entity ?? '' })">
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
                  {{ t(`datasources.ref.${r.kind}`, { name: r.name }) }}
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
      :title="t('datasources.importDialog', { count: cur?.checked.length ?? 0 }, cur?.checked.length ?? 0)">
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
.ds { cursor: pointer; height: 100%; }
.ds.active { border-color: #2f6fed; box-shadow: 0 0 0 1px #2f6fed inset; }
.ds-head { display: flex; align-items: center; gap: 8px; margin-bottom: 4px; }
.ds-dsn { font-size: 12px; opacity: .65; word-break: break-all; margin-bottom: 4px; }
.ds-count { font-size: 12px; }
.schemas { width: 220px; }
.toolbar { display: flex; justify-content: space-between; align-items: center; gap: 12px; flex-wrap: wrap; margin-bottom: 10px; }
.search { width: 240px; }
.grant-hint { font-size: 13px; }
.sync-list { display: flex; flex-direction: column; gap: 6px; margin-top: 6px; max-height: 260px; overflow: auto; }
.sync-meta { margin-left: 6px; font-size: 12px; }
.sync-switch { display: flex; align-items: center; gap: 4px; margin-top: 8px; }
.ref { margin-left: 6px; }
:deep(.cols) { margin: 2px 0 2px 36px; width: auto; }
:deep(.strong) { font-weight: 600; }
:deep(.schema) { margin-left: 6px; font-size: 12px; }
</style>
