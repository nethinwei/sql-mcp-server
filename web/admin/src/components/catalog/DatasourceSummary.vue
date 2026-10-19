<script setup lang="ts">
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { NButton, NCard, NGi, NGrid, NTag, NText } from 'naive-ui'
import { useWorkspace } from '@/stores/workspace'
import { useStatus } from '@/stores/status'
import { run } from '@/api/client'
import { ProviderCapabilitiesQuery } from '@/api/ops'
import {
  emptyProviderCapabilities, issueText, issuesOf, limitsOf, type ProviderCapabilities,
} from '@/lib/providerCapabilities'
import type { CatalogQuery } from '@/gql/graphql'

// One card per datasource: what it is, how much of it was scanned and how
// many entities read it. The connections are folded away: with hundreds of
// databases the first screen is for finding tables, not reading DSNs.
type Coverage = CatalogQuery['catalog']['coverage'][number]

defineProps<{ selected: string | null; coverage: Coverage[] }>()
const emit = defineEmits<{ select: [name: string | null] }>()
const { t } = useI18n()
const ws = useWorkspace()
const open = ref<Record<string, boolean>>({})

const entities = (ds: string) => ws.entities.filter((e) => (e.datasource ?? 'default') === ds).length

// What the serving connections guarantee on the database side, as the
// serving process judged them; edits not yet applied are judged on review.
// The count of statement timeouts the datasource relies on but no serving
// connection enforces shows on the card itself; the details fold away with
// the DSNs. A newly applied revision reloads them.
const caps = ref<ProviderCapabilities>(emptyProviderCapabilities)
const status = useStatus()
watch(() => status.server?.appliedRevision, async () => {
  try {
    caps.value = (await run(ProviderCapabilitiesQuery, {})).providerCapabilities
  } catch {
    // Without a report the cards show no guarantees.
  }
}, { immediate: true })
const strength = (s: string) => t(`datasources.strength.${s}`)
const issueLines = (ds: string) => issuesOf(caps.value, ds).map(issueText)
const limitsText = (ds: string, connection: string) => {
  const l = limitsOf(caps.value, ds, connection)
  return l && t('datasources.limits', { read: strength(l.read), write: strength(l.write), unkeyed: strength(l.unkeyed) })
}
const connectionNames = (d: { name: string; connections: { name: string }[] }) =>
  d.connections.length ? d.connections.map((c) => c.name) : caps.value.connections
    .filter((c) => c.datasource === d.name).map((c) => c.connection)
</script>

<template>
  <n-grid :cols="4" :x-gap="12" :y-gap="12" responsive="screen" item-responsive>
    <n-gi v-for="d in ws.datasources" :key="d.name" span="4 m:2 l:1">
      <n-card size="small" hoverable class="ds" :class="{ active: selected === d.name }" role="button" tabindex="0"
        :aria-pressed="selected === d.name" :aria-label="t('catalog.pickDatasourceLabel', { name: d.name })"
        @click="emit('select', selected === d.name ? null : d.name)"
        @keydown.enter.prevent="emit('select', selected === d.name ? null : d.name)"
        @keydown.space.prevent="emit('select', selected === d.name ? null : d.name)">
        <div class="head">
          <n-text strong>{{ d.name }}</n-text>
          <n-tag size="tiny" :bordered="false">{{ d.driver }}</n-tag>
        </div>
        <n-text depth="3" class="line">
          <template v-for="c in coverage.filter((x) => x.datasource === d.name)" :key="c.datasource">
            {{ c.listed == null ? t('catalog.scannedOnly', { scanned: c.scanned })
              : t('catalog.scannedOf', { scanned: c.scanned, listed: c.listed }) }} ·
          </template>
          {{ t('datasources.entityCount', { count: entities(d.name) }, entities(d.name)) }}
        </n-text>
        <n-text v-if="issuesOf(caps, d.name).length" type="warning" class="line">
          {{ t('datasources.limitIssues', { n: issuesOf(caps, d.name).length }) }}
        </n-text>
        <n-button text size="tiny" class="toggle" :aria-expanded="!!open[d.name]" @click.stop="open[d.name] = !open[d.name]"
          @keydown.enter.stop @keydown.space.stop>
          {{ open[d.name] ? t('catalog.hideConnections') : t('catalog.showConnections', { n: d.connections.length || 1 }) }}
        </n-button>
        <template v-if="open[d.name]">
          <div v-if="d.connections.length <= 1" class="dsn mono" :title="d.dsn">{{ d.dsn }}</div>
          <template v-else>
            <div v-for="c in d.connections" :key="c.name" class="dsn mono" :title="c.dsn">
              {{ c.name }}<template v-if="c.role !== 'primary' || c.pooler"> ({{ [c.role, c.pooler].filter(Boolean).join(', ') }})</template>: {{ c.dsn }}
            </div>
            <n-text depth="3" class="line">
              {{ t('datasources.routing', d.routing) }}<template v-if="d.readAfterWrite"> ·
                {{ t('datasources.readAfterWrite', { d: d.readAfterWrite }) }}</template>
            </n-text>
          </template>
          <n-text v-if="d.sqlEscape" depth="3" class="line">{{ t('datasources.sqlEscape', { c: d.sqlEscape.connection }) }}</n-text>
          <n-text v-for="line in issueLines(d.name)" :key="line" type="warning" class="line">{{ line }}</n-text>
          <template v-for="c in connectionNames(d)" :key="c">
            <n-text v-if="limitsText(d.name, c)" depth="3" class="line">
              <template v-if="connectionNames(d).length > 1">{{ c }}: </template>{{ limitsText(d.name, c) }}
            </n-text>
          </template>
        </template>
      </n-card>
    </n-gi>
  </n-grid>
</template>

<style scoped>
.ds { cursor: pointer; height: 100%; }
.ds.active { border-color: #2f6fed; box-shadow: 0 0 0 1px #2f6fed inset; }
.ds:focus-visible { outline: 2px solid #2f6fed; outline-offset: 2px; }
.head { display: flex; align-items: center; gap: 8px; margin-bottom: 4px; }
.line { display: block; font-size: 12px; }
.toggle { margin-top: 4px; font-size: 12px; }
.dsn { font-size: 12px; opacity: .65; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; margin-top: 4px; }
</style>
