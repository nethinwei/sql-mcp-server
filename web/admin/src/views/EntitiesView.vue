<script setup lang="ts">
import { computed, h, ref, watchEffect } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { NButton, NDataTable, NEmpty, NInput, NSelect, NSpace, NTag, NText, type DataTableColumns } from 'naive-ui'
import { useWorkspace } from '@/stores/workspace'
import type { EntityInput } from '@/gql/graphql'
import { loadTableComments, tableComments } from '@/lib/tableComments'
import { byNamespace, sourceIfRenamed } from '@/lib/names'
import { entityId } from '@/lib/entityRefs'

const { t } = useI18n()
const ws = useWorkspace()
const router = useRouter()
const search = ref('')
const datasource = ref<string | null>(null)

watchEffect(() => { void loadTableComments(ws.entities) })
const description = (e: EntityInput) => e.description || tableComments(e)?.description || ''

// A row is an entity, or the heading of the namespace (datasource · schema)
// the entities below it are named in, so the namespace is shown once.
type Row = EntityInput | { namespace: string }
const isHeading = (r: Row): r is { namespace: string } => 'namespace' in r

const rows = computed<Row[]>(() => {
  const q = search.value.trim().toLowerCase()
  const shown = ws.entities.filter((e) =>
    (!datasource.value || (e.datasource ?? 'default') === datasource.value) &&
    (!q || e.name.toLowerCase().includes(q) || description(e).toLowerCase().includes(q)))
  return byNamespace(shown).flatMap((g) => (g.namespace ? [{ namespace: g.namespace }, ...g.items] : g.items))
})

const entityColumns = computed<DataTableColumns<EntityInput>>(() => [
  {
    title: t('entities.entity'), key: 'name', minWidth: 200,
    render: (e) => h('div', { class: 'cell-name' }, [
      h('span', { class: 'mono strong' }, e.name),
      // The table it reads, only when it is named differently.
      sourceIfRenamed(e) ? h(NText, { depth: 3, class: 'mono' }, () => `← ${sourceIfRenamed(e)}`) : null,
      e.kind === 'procedure' ? h(NTag, { size: 'tiny', bordered: false }, () => t('entities.procedure')) : null,
    ]),
  },
  {
    title: t('entities.description'), key: 'description', ellipsis: { tooltip: true },
    // A database comment (the runtime default) is shown in grey.
    render: (e) => e.description || h(NText, { depth: 3 }, () => description(e)),
  },
  { title: t('entities.fields'), key: 'fields', width: 80, render: (e) => (e.fields ?? []).filter((f) => !f.exclude).length },
  {
    title: t('entities.whoCanAccess'), key: 'access', width: 280,
    render: (e) => {
      const a = ws.accessTo(entityId(e))
      const tags = [
        ...a.roles.map((r) => h(NTag, { size: 'small', type: 'info', bordered: false }, () => r)),
        ...a.users.map((u) => h(NTag, { size: 'small', bordered: false }, () => t('entities.userTag', { name: u }))),
      ]
      if (e.legacyAccess) tags.push(h(NTag, { size: 'small', bordered: false }, () => t('entities.legacyTag')))
      return tags.length ? h(NSpace, { size: 4 }, () => tags) : h(NText, { type: 'warning' }, () => t('entities.nobody'))
    },
  },
])
const columns = computed<DataTableColumns<Row>>(() => entityColumns.value.map((c, i) => ({
  ...c,
  colSpan: (r: Row) => (isHeading(r) ? (i === 0 ? entityColumns.value.length : 0) : 1),
  render: (r: Row, index: number) => (isHeading(r)
    ? (i === 0 ? h(NText, { depth: 2, class: 'mono namespace' }, () => r.namespace) : null)
    : (c as { render: (e: EntityInput, index: number) => unknown }).render(r, index)),
})) as DataTableColumns<Row>)

const datasourceOptions = computed(() => ws.datasources.map((d) => ({ label: d.name, value: d.name })))
</script>

<template>
  <n-space vertical :size="12">
    <div class="toolbar">
      <n-space :wrap="false">
        <n-input v-model:value="search" :placeholder="t('entities.search')" clearable size="small" class="search" />
        <n-select v-model:value="datasource" :options="datasourceOptions" clearable :placeholder="t('entities.allDatasources')"
          size="small" class="ds" />
      </n-space>
      <n-button size="small" @click="router.push({ name: 'datasources' })">{{ t('entities.importFromDb') }}</n-button>
    </div>
    <n-empty v-if="!ws.entities.length" :description="t('entities.empty')">
      <template #extra><n-button type="primary" @click="router.push({ name: 'datasources' })">{{ t('entities.goImport') }}</n-button></template>
    </n-empty>
    <n-data-table v-else :columns="columns" :data="rows" size="small"
      :row-key="(r: Row) => (isHeading(r) ? `ns:${r.namespace}` : entityId(r))"
      :row-props="(r: Row) => (isHeading(r) ? { class: 'heading' }
        : { class: 'clickable', onClick: () => router.push({ name: 'entity', params: { id: entityId(r) } }) })" />
  </n-space>
</template>

<style scoped>
.toolbar { display: flex; justify-content: space-between; gap: 12px; flex-wrap: wrap; }
.search { width: 240px; }
.ds { width: 170px; }
:deep(.clickable) { cursor: pointer; }
:deep(.cell-name) { display: flex; align-items: center; gap: 6px; }
:deep(.strong) { font-weight: 600; }
:deep(.heading td) { background: var(--n-th-color); }
:deep(.namespace) { font-size: 12px; }
</style>
