<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  NButton, NCheckbox, NCheckboxGroup, NDivider, NGrid, NGi, NIcon, NInput, NModal, NRadio,
  NRadioGroup, NSpace, NSwitch, NTag, NText, NTooltip,
} from 'naive-ui'
import { AddOutline, TrashOutline } from '@vicons/ionicons5'
import FilterBuilder from './FilterBuilder.vue'
import { describeFilter, type Filter } from '@/lib/filter'
import { setActionOn } from '@/lib/grants'
import type { Action, EntityInput, GrantInput } from '@/gql/graphql'

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
const onlyGranted = ref(false)

interface Row {
  entity: EntityInput
  grant: GrantInput | null
  index: number // position in modelValue, -1 when ungranted
  sub: number // 0 for the first grant of an entity
}

const rows = computed<Row[]>(() => {
  const q = search.value.trim().toLowerCase()
  const out: Row[] = []
  const sorted = [...props.entities].sort((a, b) => a.name.localeCompare(b.name))
  for (const entity of sorted) {
    if (q && !entity.name.toLowerCase().includes(q) && !(entity.description ?? '').toLowerCase().includes(q)) continue
    const grants = props.modelValue.map((g, index) => ({ g, index })).filter(({ g }) => g.entity === entity.name)
    if (grants.length === 0) {
      if (!onlyGranted.value) out.push({ entity, grant: null, index: -1, sub: 0 })
      continue
    }
    grants.forEach(({ g, index }, sub) => out.push({ entity, grant: g, index, sub }))
  }
  return out
})

const grantedCount = computed(() => new Set(props.modelValue.map((g) => g.entity)).size)

function emitWith(mutate: (list: GrantInput[]) => void) {
  const list = props.modelValue.map((g) => ({ ...g, actions: [...g.actions] }))
  mutate(list)
  emit('update:modelValue', list)
}

function applicable(row: Row, action: Action) {
  const isProcedure = row.entity.kind === 'procedure'
  return action === 'EXECUTE' ? isProcedure : !isProcedure
}

function toggle(row: Row, action: Action, on: boolean) {
  emitWith((list) => {
    if (!row.grant) {
      if (on) list.push({ entity: row.entity.name, actions: [action] })
      return
    }
    const g = list[row.index]
    g.actions = on ? [...g.actions, action] : g.actions.filter((a) => a !== action)
    if (g.actions.length === 0) list.splice(row.index, 1)
  })
}

// Bulk toggles act on the rows currently listed, so searching first narrows
// them, e.g. search "order" and grant read on every match.
function columnState(action: Action) {
  const entities = [...new Set(rows.value.filter((r) => applicable(r, action)).map((r) => r.entity.name))]
  const on = entities.filter((name) =>
    props.modelValue.some((g) => g.entity === name && g.actions.includes(action))).length
  return { checked: entities.length > 0 && on === entities.length, partial: on > 0 && on < entities.length, any: entities.length > 0 }
}
function toggleColumn(action: Action, on: boolean) {
  const entities = [...new Set(rows.value.filter((r) => applicable(r, action)).map((r) => r.entity.name))]
  emit('update:modelValue', setActionOn(props.modelValue, entities, action, on))
}
const rowActions = (row: Row) => actions.filter((a) => applicable(row, a))
function rowState(row: Row) {
  const n = rowActions(row).filter((a) => row.grant?.actions.includes(a)).length
  return { checked: n > 0 && n === rowActions(row).length, partial: n > 0 && n < rowActions(row).length }
}
function toggleRow(row: Row, on: boolean) {
  emitWith((list) => {
    if (!on) {
      if (row.grant) list.splice(row.index, 1)
      return
    }
    if (row.grant) list[row.index].actions = rowActions(row)
    else list.push({ entity: row.entity.name, actions: rowActions(row) })
  })
}

function addGrant(row: Row) {
  emitWith((list) => list.push({ entity: row.entity.name, actions: ['READ'] }))
}
function removeGrant(row: Row) {
  emitWith((list) => list.splice(row.index, 1))
}

const fieldNames = (e: EntityInput) => (e.fields ?? []).filter((f) => !f.exclude).map((f) => f.name)

function fieldsLabel(row: Row) {
  if (!row.grant?.fieldsRestricted) return t('grants.allFields')
  const writes = row.grant.actions.some((a) => a === 'CREATE' || a === 'UPDATE')
  return t('grants.readCount', { n: row.grant.readFields?.length ?? 0, total: fieldNames(row.entity).length }) +
    (writes ? t('grants.writeCount', { n: row.grant.writeFields?.length ?? 0 }) : '')
}

// Field and row scope editors share one modal. `editing` keeps its content
// until the close animation ends so the dialog does not blank out.
const editing = ref<{ row: Row; mode: 'fields' | 'rows' } | null>(null)
const editorOpen = ref(false)
const draftRestricted = ref(false)
const draftRead = ref<string[]>([])
const draftWrite = ref<string[]>([])
const draftRows = ref<Filter | undefined>(undefined)
const fieldFilter = ref('')

function openFields(row: Row) {
  draftRestricted.value = Boolean(row.grant?.fieldsRestricted)
  draftRead.value = row.grant?.fieldsRestricted ? [...(row.grant.readFields ?? [])] : fieldNames(row.entity)
  draftWrite.value = [...(row.grant?.writeFields ?? [])]
  fieldFilter.value = ''
  editing.value = { row, mode: 'fields' }
  editorOpen.value = true
}
function openRows(row: Row) {
  draftRows.value = (row.grant?.rows as Filter | undefined) ?? undefined
  editing.value = { row, mode: 'rows' }
  editorOpen.value = true
}
function saveEditor() {
  const e = editing.value
  if (!e) return
  emitWith((list) => {
    const g = list[e.row.index]
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
  ? t(editing.value.mode === 'fields' ? 'grants.editorFields' : 'grants.editorRows', { entity: editing.value.row.entity.name })
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
          <tr v-for="row in rows" :key="row.entity.name + row.index" :class="{ granted: row.grant, sub: row.sub > 0 }">
            <td class="name">
              <template v-if="row.sub === 0">
                <router-link :to="{ name: 'entity', params: { name: row.entity.name } }" class="entity mono">
                  {{ row.entity.name }}
                </router-link>
                <div v-if="row.entity.description" class="desc" :title="row.entity.description">
                  {{ row.entity.description }}
                </div>
              </template>
              <n-tag v-else size="tiny" :bordered="false">{{ t('grants.another', { n: row.sub + 1 }) }}</n-tag>
            </td>
            <td class="act all">
              <n-checkbox :disabled="readonly" :checked="rowState(row).checked" :indeterminate="rowState(row).partial"
                :aria-label="t('grants.allRow')" :title="t('grants.allRow')"
                @update:checked="(on: boolean) => toggleRow(row, on)" />
            </td>
            <td v-for="a in actions" :key="a" class="act">
              <n-checkbox v-if="applicable(row, a)" :disabled="readonly"
                :checked="row.grant?.actions.includes(a) ?? false"
                @update:checked="(on: boolean) => toggle(row, a, on)" />
            </td>
            <td class="scope">
              <n-button v-if="row.grant" size="tiny" :type="row.grant.fieldsRestricted ? 'info' : 'default'"
                secondary :disabled="readonly" @click="openFields(row)">
                {{ fieldsLabel(row) }}
              </n-button>
            </td>
            <td class="scope">
              <n-button v-if="row.grant" size="tiny" :type="row.grant.rows ? 'info' : 'default'" secondary
                :disabled="readonly" :title="describeFilter(row.grant.rows as Filter)" @click="openRows(row)">
                <span class="rows-text">{{ describeFilter(row.grant.rows as Filter) }}</span>
              </n-button>
            </td>
            <td class="ops">
              <n-space v-if="row.grant && !readonly" :size="2" :wrap="false">
                <n-tooltip>
                  <template #trigger>
                    <n-button size="tiny" quaternary circle :aria-label="t('grants.addAnother')" @click="addGrant(row)">
                      <template #icon><n-icon><add-outline /></n-icon></template>
                    </n-button>
                  </template>
                  {{ t('grants.addAnother') }}
                </n-tooltip>
                <n-button size="tiny" quaternary circle type="error" :aria-label="t('common.remove')" @click="removeGrant(row)">
                  <template #icon><n-icon><trash-outline /></n-icon></template>
                </n-button>
              </n-space>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <n-modal :show="editorOpen" preset="card" class="editor" :mask-closable="false" :title="editorTitle"
      @update:show="(v: boolean) => (editorOpen = v)" @after-leave="editing = null">
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
          <n-grid :cols="editingWrites ? 2 : 1" :x-gap="24">
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
tbody tr:last-child td { border-bottom: none; }
th { font-weight: 500; opacity: .75; white-space: nowrap; }
.act { text-align: center; width: 56px; }
.act.all { border-right: 1px solid rgba(128,128,128,.16); }
.col-toggle { margin-top: 4px; }
.name { min-width: 200px; }
.scope { white-space: nowrap; }
.ops { width: 70px; }
tr.granted td.name .entity { font-weight: 600; }
.entity { color: inherit; text-decoration: none; }
.entity:hover { color: #2f6fed; }
.desc { font-size: 12px; opacity: .6; max-width: 280px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; margin-top: 2px; }
.rows-text { display: inline-block; max-width: 220px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; vertical-align: bottom; }
.empty { text-align: center; opacity: .6; padding: 20px; }
.divider { margin: 12px 0; }
.field-filter { margin-bottom: 10px; }
.fieldlist { display: flex; flex-direction: column; gap: 6px; margin-top: 8px; max-height: 360px; overflow: auto; }
.field-desc { margin-left: 6px; font-size: 12px; }
.col-head { display: flex; align-items: center; gap: 4px; flex-wrap: wrap; }
:global(.editor) { width: 720px; max-width: calc(100vw - 32px); }
</style>
