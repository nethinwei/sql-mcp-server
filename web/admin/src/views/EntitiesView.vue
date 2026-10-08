<script setup lang="ts">
import { computed, h, ref, watchEffect } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { NButton, NDataTable, NEmpty, NInput, NSelect, NSpace, NTag, NText, type DataTableColumns } from 'naive-ui'
import { useWorkspace } from '@/stores/workspace'
import type { EntityInput } from '@/gql/graphql'
import { loadTableComments, tableComments } from '@/lib/tableComments'

const { t } = useI18n()
const ws = useWorkspace()
const router = useRouter()
const search = ref('')
const datasource = ref<string | null>(null)

watchEffect(() => { void loadTableComments(ws.entities) })
const description = (e: EntityInput) => e.description || tableComments(e)?.description || ''

const rows = computed(() => {
  const q = search.value.trim().toLowerCase()
  return ws.entities.filter((e) =>
    (!datasource.value || (e.datasource ?? 'default') === datasource.value) &&
    (!q || e.name.toLowerCase().includes(q) || description(e).toLowerCase().includes(q)))
})

const columns = computed<DataTableColumns<EntityInput>>(() => [
  {
    title: t('entities.entity'), key: 'name', minWidth: 160,
    render: (e) => h('div', { class: 'cell-name' }, [
      h('span', { class: 'mono strong' }, e.name),
      e.kind === 'procedure' ? h(NTag, { size: 'tiny', bordered: false }, () => t('entities.procedure')) : null,
    ]),
  },
  {
    title: t('entities.description'), key: 'description', ellipsis: { tooltip: true },
    // A database comment (the runtime default) is shown in grey.
    render: (e) => e.description || h(NText, { depth: 3 }, () => description(e)),
  },
  { title: t('entities.datasource'), key: 'datasource', width: 120, render: (e) => e.datasource ?? 'default' },
  { title: t('entities.fields'), key: 'fields', width: 80, render: (e) => (e.fields ?? []).filter((f) => !f.exclude).length },
  {
    title: t('entities.whoCanAccess'), key: 'access', width: 280,
    render: (e) => {
      const a = ws.accessTo(e.name)
      const tags = [
        ...a.roles.map((r) => h(NTag, { size: 'small', type: 'info', bordered: false }, () => r)),
        ...a.users.map((u) => h(NTag, { size: 'small', bordered: false }, () => t('entities.userTag', { name: u }))),
      ]
      if (e.legacyAccess) tags.push(h(NTag, { size: 'small', bordered: false }, () => t('entities.legacyTag')))
      return tags.length ? h(NSpace, { size: 4 }, () => tags) : h(NText, { type: 'warning' }, () => t('entities.nobody'))
    },
  },
])

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
    <n-data-table v-else :columns="columns" :data="rows" :row-key="(e: EntityInput) => e.name" size="small"
      :row-props="(e: EntityInput) => ({ class: 'clickable', onClick: () => router.push({ name: 'entity', params: { name: e.name } }) })" />
  </n-space>
</template>

<style scoped>
.toolbar { display: flex; justify-content: space-between; gap: 12px; flex-wrap: wrap; }
.search { width: 240px; }
.ds { width: 170px; }
:deep(.clickable) { cursor: pointer; }
:deep(.cell-name) { display: flex; align-items: center; gap: 6px; }
:deep(.strong) { font-weight: 600; }
</style>
