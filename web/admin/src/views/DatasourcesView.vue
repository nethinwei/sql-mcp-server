<script setup lang="ts">
import { computed, h, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  NAlert, NButton, NCard, NCheckbox, NCheckboxGroup, NDataTable, NEmpty, NForm, NFormItem, NGrid, NGi, NInput,
  NModal, NSelect, NSpace, NTag, NText, NTooltip, useMessage, type DataTableColumns, type DataTableRowKey,
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
const selectedDs = ref<string | null>(ws.datasources[0]?.name ?? null)
const schemas = ref('')
const tables = ref<Table[]>([])
const scanning = ref(false)
const scanned = ref(false)
const checked = ref<DataTableRowKey[]>([])
const search = ref('')
const entityCount = (ds: string) => {
  const n = ws.entities.filter((e) => (e.datasource ?? 'default') === ds).length
  return t('datasources.entityCount', { count: n }, n)
}

function selectDatasource(name: string) {
  selectedDs.value = name
  scanned.value = false
  tables.value = []
  checked.value = []
}

async function scan() {
  if (!selectedDs.value) return
  scanning.value = true
  try {
    const list = schemas.value.split(',').map((s) => s.trim()).filter(Boolean)
    const data = await run(SchemaImportQuery, { datasource: selectedDs.value, schemas: list.length ? list : null })
    tables.value = data.schemaImport.tables
    scanned.value = true
    checked.value = []
  } catch (e) {
    message.error(e instanceof Error ? e.message : String(e))
  } finally {
    scanning.value = false
  }
}

const key = (tb: Table) => `${tb.schema}.${tb.table}`
const inWorkspace = (tb: Table) =>
  ws.entities.find((e) => readsTable(e, selectedDs.value ?? '', tb, tables.value))

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
  const q = search.value.trim().toLowerCase()
  return tables.value.filter((tb) => !q || tb.table.toLowerCase().includes(q) || tb.description.toLowerCase().includes(q))
})

const columns = computed<DataTableColumns<Table>>(() => [
  { type: 'selection', disabled: (tb) => !status(tb).importable },
  {
    type: 'expand',
    renderExpand: (tb) =>
      h('div', { class: 'cols' }, tb.columns.map((c) =>
        h('div', { class: 'col' }, [
          h('span', { class: 'mono' }, c.name),
          h(NText, { depth: 3, class: 'col-meta' }, () =>
            ` ${c.type}${c.nullable ? '' : ' · ' + t('datasources.notNull')}${c.primaryKey ? ' · ' + t('datasources.primaryKey') : ''}`),
          c.description ? h(NText, { class: 'col-desc' }, () => c.description) : null,
        ]))),
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
      ? h(NButton, { size: 'tiny', onClick: () => syncFields(tb) }, () => t('datasources.syncFields'))
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
  const chosen = tables.value.filter((tb) => checked.value.includes(key(tb)))
  const candidates = uniqueCandidates(chosen.map((tb) => toEntityInput(tb.candidate)), ws.entities.map((e) => e.name))
  const added = candidates.map((e) => e.name)
  const dropped = ws.importEntities(candidates, grantRoles.value, grantActions.value)
  checked.value = []
  importing.value = false
  const note = dropped ? t('datasources.droppedNote', { dropped }) : ''
  message.success(grantRoles.value.length
    ? t('datasources.addedGranted', { count: added.length, roles: grantRoles.value.join(', ') }, added.length) + note
    : dropped
      ? t('datasources.addedDropped', { count: chosen.length, dropped }, chosen.length)
      : t('datasources.added', { count: chosen.length }, chosen.length))
}

function syncFields(tb: Table) {
  const e = inWorkspace(tb)
  if (!e) return
  const existing = new Map((e.fields ?? []).map((f) => [f.name, f]))
  const fields = tb.columns.map((c) => existing.get(c.name) ?? { name: c.name, description: c.description })
  ws.upsertEntity({ ...e, fields })
  message.success(t('datasources.synced', { name: e.name }))
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

    <n-card v-if="selectedDs" size="small" :title="t('datasources.importTitle', { name: selectedDs })">
      <template #header-extra>
        <n-space :wrap="false">
          <n-input v-model:value="schemas" size="small" :placeholder="t('datasources.schemas')" class="schemas" />
          <n-button size="small" type="primary" :loading="scanning" :disabled="!can('admin:write')" @click="scan">
            {{ scanned ? t('datasources.rescan') : t('datasources.scan') }}
          </n-button>
        </n-space>
      </template>
      <n-empty v-if="!scanned" :description="t('datasources.scanHint')" />
      <template v-else>
        <div class="toolbar">
          <n-input v-model:value="search" size="small" :placeholder="t('datasources.searchTables')" clearable class="search" />
          <n-button type="primary" size="small" :disabled="!checked.length" @click="openImport">
            {{ t('datasources.addSelected', { count: checked.length }) }}
          </n-button>
        </div>
        <n-data-table v-model:checked-row-keys="checked" :columns="columns" :data="visible" :row-key="key"
          size="small" :max-height="560" />
      </template>
    </n-card>

    <n-modal v-model:show="importing" preset="card" class="dialog"
      :title="t('datasources.importDialog', { count: checked.length }, checked.length)">
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
:deep(.cols) { display: grid; grid-template-columns: repeat(auto-fill, minmax(280px, 1fr)); gap: 4px 16px; padding: 4px 0; }
:deep(.col-meta) { font-size: 12px; }
:deep(.col-desc) { margin-left: 8px; font-size: 12px; }
:deep(.strong) { font-weight: 600; }
:deep(.schema) { margin-left: 6px; font-size: 12px; }
</style>
