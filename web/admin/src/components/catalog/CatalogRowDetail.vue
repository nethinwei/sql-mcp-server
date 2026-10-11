<script setup lang="ts">
import { onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { NAlert, NSpin } from 'naive-ui'
import { CatalogTableQuery } from '@/api/ops'
import { workspace } from '@/lib/workspaceSync'
import { createResource } from '@/lib/asyncResource'
import TableDetail from './TableDetail.vue'

// The structure of a table, read when its row is expanded: the catalog lists
// only summaries.
const props = defineProps<{ tableRef: string }>()
const { t } = useI18n()
const resource = createResource(async (ref: string) =>
  (await workspace.read(CatalogTableQuery, { ref })).catalogTable)
onMounted(() => resource.load(props.tableRef))
</script>

<template>
  <n-alert v-if="resource.state.status === 'error'" type="error" :show-icon="false">{{ resource.state.error }}</n-alert>
  <n-alert v-else-if="resource.state.status === 'notFound'" type="warning" :show-icon="false">
    {{ t('catalog.tableNotFound', { ref: tableRef }) }}
  </n-alert>
  <table-detail v-else-if="resource.state.status === 'ok' && resource.state.data" :table="resource.state.data" />
  <n-spin v-else size="small" class="wait" />
</template>

<style scoped>
.wait { margin: 12px 36px; }
</style>
