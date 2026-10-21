<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  NAlert, NButton, NCheckbox, NCheckboxGroup, NDivider, NGrid, NGi, NInput, NModal, NPagination, NRadio,
  NRadioGroup, NSpace, NSwitch, NText, NTooltip,
} from 'naive-ui'
import FilterBuilder from './FilterBuilder.vue'
import GrantMatrixRow from './GrantMatrixRow.vue'
import { type Filter } from '@/lib/filter'
import { grantsByEntity, locateGrant, setActionOn, type GrantRow as Row } from '@/lib/grants'
import { byNamespace } from '@/lib/names'
import { EntityIndex, entityId } from '@/lib/entityRefs'
import { grantable as grantableOn, indexCapabilities, type CapabilityIndex } from '@/lib/capabilities'
import { run } from '@/api/client'
import { CapabilitiesQuery } from '@/api/ops'
import type { Action } from '@/gql/graphql'
import type { EntityInput, GrantInput } from '@/gql/schema'

// Edits a list of grants as an entity × action matrix. An entity usually has
// one grant; extra grants (different field or row scopes) appear as sub-rows.
const props = defineProps<{
  modelValue: GrantInput[]
  entities: EntityInput[]
  subjectKeys?: string[]
  readonly?: boolean
}>()
const emit = defineEmits<{ 'update:modelValue': [value: GrantInput[]] }>()
const { t } = useI18n()

const actions: Action[] = ['READ', 'AGGREGATE', 'CREATE', 'UPDATE', 'DELETE', 'EXECUTE']

const search = ref('')
// Only granted entities by default; a role or user without grants lists them all.
const onlyGranted = ref(props.modelValue.length > 0)

// Grants name entities by reference; they are matched by the entity it names.
const entityIndex = computed(() => new EntityIndex(props.entities))

// The grants of each entity, by entity ID, with their position in modelValue.
const grantsById = computed(() => grantsByEntity(props.modelValue, (ref) => entityIndex.value.idOf(ref)))

// The entities in order with their ID and namespace, kept while the
// entities stay: toggling a grant does not sort them again.
const ordered = computed(() => {
  const sorted = [...props.entities].sort((a, b) =>
    (a.datasource ?? '').localeCompare(b.datasource ?? '') || (a.schema ?? '').localeCompare(b.schema ?? '') ||
    a.name.localeCompare(b.name))
  return byNamespace(sorted).flatMap((group) =>
    group.items.map((entity) => ({ entity, id: entityId(entity), namespace: group.namespace })))
})
const shown = computed(() => {
  const q = search.value.trim().toLowerCase()
  if (!q) return ordered.value
  return ordered.value.filter(({ entity: e }) =>
    e.name.toLowerCase().includes(q) || (e.description ?? '').toLowerCase().includes(q))
})

// The listed entities, each with its rows (one per grant). A row that did
// not change is the same object as before, so its GrantMatrixRow is not
// rendered again.
let kept = new Map<string, Row>()
const listed = computed<Row[][]>(() => {
  const out: Row[][] = []
  const next = new Map<string, Row>()
  const row = (r: Row) => {
    const key = `${r.id}\u0000${r.sub}`
    const was = kept.get(key)
    const same = was && was.entity === r.entity && was.grant === r.grant && was.index === r.index &&
      was.namespace === r.namespace
    next.set(key, same ? was : r)
    return same ? was : r
  }
  for (const { entity, id, namespace } of shown.value) {
    const grants = grantsById.value.get(id) ?? []
    if (grants.length === 0) {
      if (!onlyGranted.value) out.push([row({ entity, id, grant: null, index: -1, sub: 0, namespace })])
      continue
    }
    out.push(grants.map(({ grant, index }, sub) => row({ entity, id, grant, index, sub, namespace })))
  }
  kept = next
  return out
})
const rows = computed(() => listed.value.flat())

// Thousands of entities render slowly, so the matrix shows one page of them;
// bulk toggles still act on every listed row.
const page = ref(1)
const pageSize = ref(50)
watch([search, onlyGranted], () => { page.value = 1 })
const pageCount = computed(() => Math.max(1, Math.ceil(listed.value.length / pageSize.value)))
const current = computed(() => Math.min(page.value, pageCount.value))
const pageRows = computed(() => {
  const start = (current.value - 1) * pageSize.value
  return listed.value.slice(start, start + pageSize.value).flat()
})
// Whether a namespace heading goes above the row: the first row of a page or
// of a namespace.
const headed = (i: number) => {
  const ns = pageRows.value[i].namespace
  return Boolean(ns) && (i === 0 || pageRows.value[i - 1].namespace !== ns)
}

const grantedCount = computed(() => grantsById.value.size)
const refOf = (row: Row) => entityIndex.value.shortName(row.id)

// Emits the grants with mutate applied to a copy of the list; mutate
// replaces a grant it changes (edit(list, i)) rather than changing it.
function emitWith(mutate: (list: GrantInput[]) => void) {
  const list = [...props.modelValue]
  mutate(list)
  emit('update:modelValue', list)
}
const edit = (list: GrantInput[], i: number) => (list[i] = { ...list[i] })

// What the routed datasource connections may do, as the running service read
// it. Denied cells cannot be newly granted; existing grants stay removable.
const caps = ref<CapabilityIndex>(new Map())
onMounted(async () => {
  try {
    caps.value = indexCapabilities((await run(CapabilitiesQuery, {})).capabilities)
  } catch {
    // Without a report every cell stays editable; the database decides.
  }
})
const grantable = (row: Row, action: Action) => grantableOn(caps.value, row.id, row.entity.kind, action)

function toggle(row: Row, action: Action, on: boolean) {
  emitWith((list) => {
    if (!row.grant) {
      if (on) list.push({ entity: refOf(row), actions: [action] })
      return
    }
    const g = edit(list, row.index)
    g.actions = on ? [...g.actions, action] : g.actions.filter((a) => a !== action)
    if (g.actions.length === 0) list.splice(row.index, 1)
  })
}

// Bulk toggles act on the rows currently listed, so searching first narrows
// them, e.g. search "order" and grant read on every match.
const columnEntities = (action: Action) => [...new Set(rows.value.filter((r) => grantable(r, action)).map((r) => r.id))]
// Each column's state, once per change rather than per checkbox prop read.
const columnStates = computed(() => new Map(actions.map((action) => {
  const entities = columnEntities(action)
  const on = entities.filter((id) =>
    (grantsById.value.get(id) ?? []).some(({ grant }) => grant.actions.includes(action))).length
  return [action, { checked: entities.length > 0 && on === entities.length, partial: on > 0 && on < entities.length,
    any: entities.length > 0 }]
})))
const columnState = (action: Action) => columnStates.value.get(action)!
function toggleColumn(action: Action, on: boolean) {
  emit('update:modelValue', setActionOn(props.modelValue, columnEntities(action), action, on, entityIndex.value))
}
const rowActions = (row: Row) => actions.filter((a) => grantable(row, a))
function toggleRow(row: Row, on: boolean) {
  emitWith((list) => {
    if (!on) {
      if (row.grant) list.splice(row.index, 1)
      return
    }
    if (row.grant) edit(list, row.index).actions = rowActions(row)
    else list.push({ entity: refOf(row), actions: rowActions(row) })
  })
}

function addGrant(row: Row) {
  emitWith((list) => list.push({ entity: refOf(row), actions: ['READ'] }))
}
function removeGrant(row: Row) {
  emitWith((list) => list.splice(row.index, 1))
}

const fieldNames = (e: EntityInput) => (e.fields ?? []).filter((f) => !f.exclude).map((f) => f.name)

// Field and row scope editors share one modal. `editing` keeps its content
// until the close animation ends so the dialog does not blank out.
const editing = ref<{ row: Row; mode: 'fields' | 'rows' } | null>(null)
const editorOpen = ref(false)
const draftRestricted = ref(false)
const draftRead = ref<string[]>([])
const draftWrite = ref<string[]>([])
const draftRows = ref<Filter | undefined>(undefined)
const fieldFilter = ref('')
// The grant edited was replaced or removed while the editor was open.
const editorGone = ref(false)

function openFields(row: Row) {
  draftRestricted.value = Boolean(row.grant?.fieldsRestricted)
  draftRead.value = row.grant?.fieldsRestricted ? [...(row.grant.readFields ?? [])] : fieldNames(row.entity)
  draftWrite.value = [...(row.grant?.writeFields ?? [])]
  fieldFilter.value = ''
  editorGone.value = false
  editing.value = { row, mode: 'fields' }
  editorOpen.value = true
}
function openRows(row: Row) {
  draftRows.value = (row.grant?.rows as Filter | undefined) ?? undefined
  editorGone.value = false
  editing.value = { row, mode: 'rows' }
  editorOpen.value = true
}
function saveEditor() {
  const e = editing.value
  if (!e) return
  // The grants may have changed since the editor opened: write the grant
  // edited, wherever it is now, and nothing when it is gone.
  const at = e.row.grant ? locateGrant(props.modelValue, e.row.grant) : -1
  if (at < 0) {
    editorGone.value = true
    return
  }
  emitWith((list) => {
    const g = edit(list, at)
    if (e.mode === 'fields') {
      g.fieldsRestricted = draftRestricted.value
      g.readFields = draftRestricted.value ? draftRead.value : []
      g.writeFields = draftRestricted.value ? draftWrite.value : []
    } else {
      g.rows = draftRows.value
    }
  })
  editorOpen.value = false
}

const editorTitle = computed(() => editing.value
  ? t(editing.value.mode === 'fields' ? 'grants.editorFields' : 'grants.editorRows', { entity: refOf(editing.value.row) })
  : '')
const editingFields = computed(() =>
  editing.value
    ? (editing.value.row.entity.fields ?? [])
        .filter((f) => !f.exclude && f.name.includes(fieldFilter.value))
        .map((f) => ({ name: f.name, description: f.description ?? '' }))
    : [],
)
// Bulk selection acts on the fields the filter shows (all of them without a filter).
const fieldTotal = computed(() => (editing.value?.row.entity.fields ?? []).filter((f) => !f.exclude).length)
function selectShown(list: string[]): string[] {
  return [...new Set([...list, ...editingFields.value.map((f) => f.name)])]
}
function clearShown(list: string[]): string[] {
  const shown = new Set(editingFields.value.map((f) => f.name))
  return list.filter((n) => !shown.has(n))
}
const editingWrites = computed(() =>
  Boolean(editing.value?.row.grant?.actions.some((a) => a === 'CREATE' || a === 'UPDATE')),
)
</script>

<template>
  <div>
    <div class="toolbar">
      <n-input v-model:value="search" size="small" :placeholder="t('grants.search')" clearable class="search" />
      <n-space align="center" :size="8" :wrap="false">
        <n-text depth="3" class="count">{{ t('grants.granted', { count: grantedCount, total: entities.length }) }}</n-text>
        <n-switch v-model:value="onlyGranted" size="small" />
        <n-text class="label">{{ t('grants.onlyGranted') }}</n-text>
      </n-space>
    </div>
    <div class="matrix">
      <table>
        <thead>
          <tr>
            <th class="name">{{ t('grants.entity') }}</th>
            <th class="act all">{{ t('grants.all') }}</th>
            <th v-for="a in actions" :key="a" class="act">
              <n-tooltip><template #trigger><span>{{ t(`grants.actions.${a}`) }}</span></template>{{ t(`grants.hints.${a}`) }}</n-tooltip>
              <div v-if="!readonly && columnState(a).any" class="col-toggle">
                <n-tooltip>
                  <template #trigger>
                    <n-checkbox size="small" :checked="columnState(a).checked" :indeterminate="columnState(a).partial"
                      :aria-label="t('grants.allColumn', { action: t(`grants.actions.${a}`) })"
                      @update:checked="(on: boolean) => toggleColumn(a, on)" />
                  </template>
                  {{ t('grants.allColumn', { action: t(`grants.actions.${a}`) }) }}
                </n-tooltip>
              </div>
            </th>
            <th class="scope">{{ t('grants.fieldScope') }}</th>
            <th class="scope">{{ t('grants.rowScope') }}</th>
            <th class="ops" />
          </tr>
        </thead>
        <tbody>
          <tr v-if="rows.length === 0"><td :colspan="actions.length + 5" class="empty">{{ t('grants.noMatch') }}</td></tr>
          <template v-for="(row, i) in pageRows" :key="row.id + row.index">
          <tr v-if="headed(i)" class="namespace">
            <td :colspan="actions.length + 5" class="mono">{{ row.namespace }}</td>
          </tr>
          <grant-matrix-row :row="row" :caps="caps" :readonly="readonly" @toggle="toggle" @toggle-row="toggleRow"
            @fields="openFields" @rows="openRows" @add="addGrant" @remove="removeGrant" />
          </template>
        </tbody>
      </table>
    </div>
    <n-pagination v-if="listed.length > 20" :page="current" :page-size="pageSize" :item-count="listed.length"
      show-size-picker :page-sizes="[20, 50, 100, 200]" class="pager"
      @update:page="(p: number) => (page = p)" @update:page-size="(n: number) => { pageSize = n; page = 1 }" />

    <n-modal :show="editorOpen" preset="card" class="editor" :mask-closable="false" :title="editorTitle"
      @update:show="(v: boolean) => (editorOpen = v)" @after-leave="editing = null">
      <n-alert v-if="editorGone" type="warning" class="gone">{{ t('grants.editorGone') }}</n-alert>
      <template v-if="editing?.mode === 'fields'">
        <n-radio-group v-model:value="draftRestricted">
          <n-space vertical :size="6">
            <n-radio :value="false">{{ t('grants.allVisible') }}</n-radio>
            <n-radio :value="true">{{ t('grants.onlySelected') }}</n-radio>
          </n-space>
        </n-radio-group>
        <template v-if="draftRestricted">
          <n-divider class="divider" />
          <n-input v-model:value="fieldFilter" size="small" :placeholder="t('grants.filterFields')" clearable class="field-filter" />
          <n-grid :cols="editingWrites ? 2 : 1" :x-gap="24" :y-gap="16">
            <n-gi>
              <div class="col-head">
                <n-text strong>{{ t('grants.readable') }}</n-text>
                <n-text depth="3" class="count">{{ t('grants.selectedCount', { n: draftRead.length, total: fieldTotal }) }}</n-text>
                <n-button size="tiny" quaternary @click="draftRead = selectShown(draftRead)">
                  {{ fieldFilter ? t('grants.selectShown') : t('grants.selectAll') }}
                </n-button>
                <n-button size="tiny" quaternary @click="draftRead = clearShown(draftRead)">
                  {{ fieldFilter ? t('grants.clearShown') : t('grants.clearAll') }}
                </n-button>
              </div>
              <n-checkbox-group v-model:value="draftRead" class="fieldlist">
                <n-checkbox v-for="f in editingFields" :key="f.name" :value="f.name">
                  <span class="mono">{{ f.name }}</span>
                  <n-text v-if="f.description" depth="3" class="field-desc">{{ f.description }}</n-text>
                </n-checkbox>
              </n-checkbox-group>
            </n-gi>
            <n-gi v-if="editingWrites">
              <div class="col-head">
                <n-text strong>{{ t('grants.writable') }}</n-text>
                <n-text depth="3" class="count">{{ t('grants.selectedCount', { n: draftWrite.length, total: fieldTotal }) }}</n-text>
                <n-button size="tiny" quaternary @click="draftWrite = selectShown(draftWrite)">
                  {{ fieldFilter ? t('grants.selectShown') : t('grants.selectAll') }}
                </n-button>
                <n-button size="tiny" quaternary @click="draftWrite = clearShown(draftWrite)">
                  {{ fieldFilter ? t('grants.clearShown') : t('grants.clearAll') }}
                </n-button>
                <n-button size="tiny" quaternary @click="draftWrite = [...draftRead]">{{ t('grants.copyReadable') }}</n-button>
              </div>
              <n-checkbox-group v-model:value="draftWrite" class="fieldlist">
                <n-checkbox v-for="f in editingFields" :key="f.name" :value="f.name">
                  <span class="mono">{{ f.name }}</span>
                </n-checkbox>
              </n-checkbox-group>
            </n-gi>
          </n-grid>
        </template>
      </template>
      <template v-else-if="editing">
        <filter-builder v-model="draftRows" :fields="fieldNames(editing.row.entity)" :subject-keys="subjectKeys" />
      </template>
      <template #footer>
        <n-space justify="end">
          <n-button @click="editorOpen = false">{{ t('common.cancel') }}</n-button>
          <n-button type="primary" @click="saveEditor">{{ t('common.confirm') }}</n-button>
        </n-space>
      </template>
    </n-modal>
  </div>
</template>

<style scoped>
.toolbar { display: flex; align-items: center; justify-content: space-between; gap: 12px; flex-wrap: wrap; margin-bottom: 10px; }
.search { width: 220px; }
.count, .label { font-size: 13px; white-space: nowrap; }
.matrix { overflow-x: auto; border: 1px solid rgba(128,128,128,.25); border-radius: 6px; }
table { border-collapse: collapse; width: 100%; font-size: 13px; }
th, td { padding: 8px 10px; border-bottom: 1px solid rgba(128,128,128,.16); text-align: left; vertical-align: middle; }
tbody tr:last-child :deep(td) { border-bottom: none; }
th { font-weight: 500; opacity: .75; white-space: nowrap; }
.act { text-align: center; width: 56px; }
.act.all { border-right: 1px solid rgba(128,128,128,.16); }
.col-toggle { margin-top: 4px; }
.name { min-width: 200px; }
.scope { white-space: nowrap; }
.ops { width: 70px; }
tr.namespace td { font-size: 12px; opacity: .7; background: rgba(128,128,128,.06); padding-top: 6px; padding-bottom: 6px; }
.pager { margin-top: 10px; justify-content: flex-end; }
.empty { text-align: center; opacity: .6; padding: 20px; }
.divider { margin: 12px 0; }
.gone { margin-bottom: 12px; }
.field-filter { margin-bottom: 10px; }
.fieldlist { display: flex; flex-direction: column; gap: 6px; margin-top: 8px; max-height: 360px; overflow: auto; }
.field-desc { margin-left: 6px; font-size: 12px; }
.col-head { display: flex; align-items: center; gap: 4px; flex-wrap: wrap; }
:global(.editor) { width: 720px; max-width: calc(100vw - 32px); }
</style>
