<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { NButton, NPopover, NSpin, NText } from 'naive-ui'
import { run } from '@/api/client'
import { CatalogLinksQuery } from '@/api/ops'
import type { CatalogLinksQuery as Links } from '@/gql/graphql'
import { useWorkspace } from '@/stores/workspace'
import { createResource } from '@/lib/asyncResource'

// The foreign keys of a table, both ways, counted as the list shows them and
// listed, from the same scanned catalog, when asked for.
const props = defineProps<{ tableRef: string; outgoing: number; incoming: number }>()
const { t } = useI18n()
const ws = useWorkspace()
const resource = createResource(async (ref: string) =>
  (await run(CatalogLinksQuery, { ref, draft: ws.computeDraft })).catalogLinks)
const links = computed<Links['catalogLinks'] | null>(() => resource.state.data)

function load(open: boolean) {
  if (open && resource.state.status !== 'ok' && resource.state.status !== 'loading') void resource.load(props.tableRef)
}

const pairs = (l: { columns: string[]; refColumns: string[] }) => l.columns.map((c, i) => `${c} → ${l.refColumns[i] ?? ''}`).join(', ')
</script>

<template>
  <n-text v-if="!outgoing && !incoming" depth="3">—</n-text>
  <n-popover v-else trigger="click" placement="bottom" @update:show="load">
    <template #trigger>
      <n-button text size="small" type="primary" :aria-label="t('catalog.linksLabel', { out: outgoing, in: incoming })">
        <span v-if="outgoing">↗{{ outgoing }}</span><span v-if="outgoing && incoming">&nbsp;</span><span v-if="incoming">↙{{ incoming }}</span>
      </n-button>
    </template>
    <n-text v-if="resource.state.status === 'error'" type="error">{{ resource.state.error }}</n-text>
    <n-spin v-else-if="!links" size="small" />
    <div v-else class="links">
      <template v-if="links.outgoing.length">
        <n-text strong>{{ t('catalog.linksOut', { n: links.outgoing.length }) }}</n-text>
        <div v-for="l in links.outgoing" :key="'o' + l.name + l.ref" class="mono line">
          {{ l.ref }} <n-text depth="3">({{ pairs(l) }})</n-text>
          <n-text v-if="!l.entity" depth="3"> · {{ t('catalog.noEntity') }}</n-text>
        </div>
      </template>
      <template v-if="links.incoming.length">
        <n-text strong>{{ t('catalog.linksIn', { n: links.incoming.length }) }}</n-text>
        <div v-for="l in links.incoming" :key="'i' + l.name + l.ref" class="mono line">
          {{ l.ref }} <n-text depth="3">({{ pairs(l) }})</n-text>
          <n-text v-if="!l.entity" depth="3"> · {{ t('catalog.noEntity') }}</n-text>
        </div>
      </template>
    </div>
  </n-popover>
</template>

<style scoped>
.links { max-width: 520px; max-height: 300px; overflow: auto; line-height: 1.8; }
.line { padding-left: 8px; }
</style>
