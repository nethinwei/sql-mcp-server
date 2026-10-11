<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { NButton, NDrawer, NDrawerContent, NInput, NPagination, NTag, NText } from 'naive-ui'
import type { PlanReportPartsFragment } from '@/gql/graphql'
import { toEntityInput, useWorkspace, type Proposal } from '@/stores/workspace'
import { fieldDiffs, grantDiffs, type GrantView } from '@/lib/proposalDiff'
import { download, toCSV } from '@/lib/selection'
import { entityId } from '@/lib/entityRefs'

// The entities a change of a plan applies to, each with what it changes:
// grants before and after for a grant, fields before and after otherwise.
type Change = PlanReportPartsFragment['changes'][number]

const props = defineProps<{ change: Change | null; proposal: Proposal | null }>()
const show = defineModel<boolean>('show', { required: true })
const { t } = useI18n()
const ws = useWorkspace()
const search = ref('')
const page = ref(1)
const pageSize = 20
watch(() => props.change, () => { search.value = ''; page.value = 1 })

const matching = computed(() => {
  const q = search.value.trim().toLowerCase()
  return (props.change?.entities ?? []).filter((id) => !q || id.toLowerCase().includes(q))
})
const shown = computed(() => matching.value.slice((page.value - 1) * pageSize, page.value * pageSize))

const grants = computed(() => (props.change?.kind === 'grant' && props.proposal
  ? new Map(grantDiffs(props.change.principal ?? '', shown.value, ws.entityIndex, ws, props.proposal).map((d) => [d.entity, d]))
  : new Map()))
const proposed = computed(() => new Map((props.proposal?.entities ?? []).map(toEntityInput).map((e) => [entityId(e), e])))
const after = (id: string) => proposed.value.get(id)
const fields = (id: string) => fieldDiffs(ws.entity(id), after(id))

const actions = (g: GrantView) => g.actions.map((a) => t(`grants.actions.${a.toUpperCase()}`)).join('、')
const scope = (g: GrantView) => [
  g.readFields ? t('plan.drillFields', { n: g.readFields.length }) : t('entity.allFields'),
  g.rows ? t('plan.drillRows') : '',
].filter(Boolean).join(' · ')
const fieldText = (f: { exclude?: boolean | null; mask?: string | null } | null) => (!f ? t('plan.drillNone')
  : f.exclude ? t('entity.hidden') : f.mask ? t(`entity.masks.${f.mask}`, f.mask) : t('plan.drillVisible'))

function exportAll() {
  download('plan-change.csv', toCSV([['entity'], ...matching.value.map((id) => [id])]))
}
</script>

<template>
  <n-drawer v-model:show="show" :width="640" placement="right">
    <n-drawer-content :title="t('plan.drillTitle', { n: change?.entities.length ?? 0 })" closable>
      <div class="bar">
        <n-input v-model:value="search" size="small" clearable class="find" :placeholder="t('plan.findEntity')" @update:value="page = 1" />
        <n-button size="small" text type="primary" @click="exportAll">{{ t('plan.export') }}</n-button>
      </div>
      <div v-for="id in shown" :key="id" class="entity">
        <div class="mono strong">{{ ws.entityIndex.shortName(id) || id }}</div>
        <template v-if="change?.kind === 'grant'">
          <div class="pair">
            <n-tag size="small" :bordered="false">{{ t('plan.drillBefore') }}</n-tag>
            <n-text v-if="!grants.get(id)?.before.length" depth="3">{{ t('plan.drillNone') }}</n-text>
            <span v-for="(g, i) in grants.get(id)?.before ?? []" :key="i" class="grant">{{ actions(g) }} <n-text depth="3">{{ scope(g) }}</n-text></span>
          </div>
          <div class="pair">
            <n-tag size="small" type="success" :bordered="false">{{ t('plan.drillAfter') }}</n-tag>
            <span v-for="(g, i) in grants.get(id)?.after ?? []" :key="i" class="grant">{{ actions(g) }} <n-text depth="3">{{ scope(g) }}</n-text></span>
          </div>
        </template>
        <template v-else-if="change?.kind !== 'import' && change?.kind !== 'createRole'">
          <div v-for="d in fields(id)" :key="d.field" class="pair mono">
            {{ d.field }}: <n-text depth="3">{{ fieldText(d.before) }}</n-text> → {{ fieldText(d.after) }}
          </div>
        </template>
        <n-text v-else-if="change?.kind === 'import'" depth="3">{{ t('plan.drillImported', { n: after(id)?.fields?.length ?? 0 }) }}</n-text>
      </div>
      <n-pagination v-if="matching.length > pageSize" v-model:page="page" :page-size="pageSize" :item-count="matching.length" />
    </n-drawer-content>
  </n-drawer>
</template>

<style scoped>
.bar { display: flex; justify-content: space-between; align-items: center; margin-bottom: 8px; }
.find { width: 260px; }
.entity { padding: 8px 0; border-bottom: 1px solid rgba(128, 128, 128, .2); }
.strong { font-weight: 600; margin-bottom: 4px; }
.pair { display: flex; flex-wrap: wrap; gap: 6px; align-items: center; font-size: 13px; margin: 2px 0; }
.grant { margin-right: 8px; }
</style>
