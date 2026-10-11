<script setup lang="ts">
import { computed, h } from 'vue'
import { useI18n } from 'vue-i18n'
import { NDataTable, NTag, NText, type DataTableColumns } from 'naive-ui'
import type { Table } from '@/lib/schemaScans'

// One scanned table in the database's own terms: a column per row with the
// keys it is part of, then the indexes, like the column view of a database
// client.
const props = defineProps<{ table: Table }>()
const { t } = useI18n()

type Column = Table['columns'][number]
type Index = Table['indexes'][number]

interface KeyTag {
  label: string
  type: 'default' | 'info' | 'success' | 'warning'
  title: string
}

// The primary key and foreign keys are shown on the columns they cover: a
// table has one primary key, and each column of a foreign key references its
// own column. The tooltip says what a foreign key writes on delete or update.
const keys = computed(() => {
  const tb = props.table
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
})

const dash = () => h(NText, { depth: 3 }, () => '—')

const columnColumns = computed<DataTableColumns<Column>>(() => [
  { title: '#', key: 'index', width: 44, render: (_, i) => h(NText, { depth: 3 }, () => i + 1) },
  {
    title: t('datasources.columnName'), key: 'name', minWidth: 140,
    render: (c) => h('span', { class: 'mono' + (c.primaryKey ? ' strong' : '') }, c.name),
  },
  { title: t('datasources.columnType'), key: 'type', minWidth: 140, render: (c) => h(NText, { depth: 2, class: 'mono' }, () => c.type) },
  { title: t('datasources.notNull'), key: 'nullable', width: 72, align: 'center', render: (c) => (c.nullable ? dash() : '✓') },
  {
    title: t('datasources.keys'), key: 'keys', minWidth: 160,
    render: (c) => h('div', { class: 'keys' }, (keys.value.get(c.name) ?? []).map((k) =>
      h(NTag, { size: 'small', bordered: false, type: k.type, title: k.title }, () => k.label))),
  },
  { title: t('datasources.description'), key: 'description', ellipsis: { tooltip: true }, render: (c) => c.description || dash() },
])

// A unique index says when it cannot identify a row.
function uniqueness(ix: Index) {
  if (ix.primary) return t('datasources.primaryKey')
  if (!ix.unique) return dash()
  const reason = props.table.keys.find((k) => k.name === ix.name)?.reason
  return reason
    ? h(NText, { depth: 3 }, () => t('datasources.uniqueNotIdentity', { reason: t(`datasources.keyReason.${reason}`) }))
    : t('datasources.unique')
}

const indexColumns = computed<DataTableColumns<Index>>(() => [
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
])
</script>

<template>
  <div class="detail">
    <n-data-table size="small" :bordered="false" :columns="columnColumns" :data="table.columns" :row-key="(c: Column) => c.name" />
    <n-data-table v-if="table.indexes.length" class="indexes" size="small" :bordered="false" :columns="indexColumns"
      :data="table.indexes" :row-key="(ix: Index) => ix.name" />
    <n-text v-if="table.sideEffects" type="warning" class="notes">{{ t('datasources.sideEffects') }}</n-text>
  </div>
</template>

<style scoped>
.detail { margin: 2px 0 2px 36px; }
.indexes { margin-top: 12px; }
.notes { display: block; margin: 6px 0; font-size: 12px; }
:deep(.keys) { display: flex; flex-wrap: wrap; gap: 4px; }
:deep(.strong) { font-weight: 600; }
</style>
