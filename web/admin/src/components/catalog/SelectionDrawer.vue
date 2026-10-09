<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { NButton, NDrawer, NDrawerContent, NEmpty, NInput, NList, NListItem, NPagination, NTag, NText } from 'naive-ui'
import { run } from '@/api/client'
import { CatalogRefsQuery } from '@/api/ops'
import { useWorkspace } from '@/stores/workspace'
import { download, selectedCount, toCSV, type Selection } from '@/lib/selection'

// The selection in full, to review before acting on it: the tables picked
// one by one, or the filter selected and the tables unpicked from it, a page
// at a time, searched and exported.
const props = defineProps<{ selection: Selection; describe: string }>()
const emit = defineEmits<{ update: [s: Selection] }>()
const show = defineModel<boolean>('show', { required: true })
const { t } = useI18n()
const ws = useWorkspace()

const search = ref('')
const page = ref(1)
const pageSize = 50
watch(show, () => { search.value = ''; page.value = 1 })

const listed = computed(() => [...(props.selection.mode === 'refs' ? props.selection.refs : props.selection.excluded)].sort())
const matching = computed(() => {
  const q = search.value.trim().toLowerCase()
  return listed.value.filter((r) => !q || r.toLowerCase().includes(q))
})
const shown = computed(() => matching.value.slice((page.value - 1) * pageSize, page.value * pageSize))

function drop(ref: string) {
  const s = props.selection
  if (s.mode === 'refs') emit('update', { mode: 'refs', refs: new Set([...s.refs].filter((r) => r !== ref)) })
  else emit('update', { ...s, excluded: new Set([...s.excluded].filter((r) => r !== ref)) })
}

const exporting = ref(false)
async function exportAll() {
  const s = props.selection
  let refs = listed.value
  if (s.mode === 'filter') {
    exporting.value = true
    try {
      refs = (await run(CatalogRefsQuery, { filter: s.filter, draft: ws.computeDraft })).catalogRefs
        .filter((r) => !s.excluded.has(r))
    } finally {
      exporting.value = false
    }
  }
  download('selected-tables.csv', toCSV([['table'], ...refs.map((r) => [r])]))
}
</script>

<template>
  <n-drawer v-model:show="show" :width="560" placement="right">
    <n-drawer-content :title="t('catalog.selectedTitle', { n: selectedCount(selection) })" closable>
      <div v-if="selection.mode === 'filter'" class="filter">
        <n-text>{{ t('catalog.selectedFilter', { n: selection.total }) }}</n-text>
        <n-tag size="small" :bordered="false" class="mono">{{ describe }}</n-tag>
        <n-text depth="3" class="block">{{ t('catalog.selectedExcluded', { n: selection.excluded.size }) }}</n-text>
      </div>
      <div class="bar">
        <n-input v-model:value="search" size="small" clearable class="find" :placeholder="t('catalog.findSelected')"
          @update:value="page = 1" />
        <n-button size="small" text type="primary" :loading="exporting" @click="exportAll">{{ t('plan.export') }}</n-button>
      </div>
      <n-empty v-if="!matching.length" size="small" />
      <n-list v-else size="small">
        <n-list-item v-for="r in shown" :key="r">
          <div class="item">
            <span class="mono">{{ r }}</span>
            <n-button text size="tiny" @click="drop(r)">
              {{ selection.mode === 'refs' ? t('catalog.unselect') : t('catalog.reselect') }}
            </n-button>
          </div>
        </n-list-item>
      </n-list>
      <n-pagination v-if="matching.length > pageSize" v-model:page="page" :page-size="pageSize" :item-count="matching.length" />
    </n-drawer-content>
  </n-drawer>
</template>

<style scoped>
.filter { display: flex; flex-direction: column; gap: 4px; margin-bottom: 12px; }
.block { display: block; }
.bar { display: flex; justify-content: space-between; align-items: center; margin-bottom: 8px; }
.find { width: 280px; }
.item { display: flex; justify-content: space-between; align-items: center; gap: 8px; font-size: 13px; }
</style>
