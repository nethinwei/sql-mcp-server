<script setup lang="ts">
import { computed, h, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import {
  NAlert, NButton, NCard, NDataTable, NEmpty, NGi, NGrid, NSpin, NStatistic, NSwitch, NTag, NText, useMessage,
  type DataTableColumns,
} from 'naive-ui'
import { PendingQuery } from '@/api/ops'
import { workspace } from '@/lib/workspaceSync'
import { useWorkspace } from '@/stores/workspace'
import type { PendingQuery as PendingResult } from '@/gql/graphql'
import { can } from '@/api/session'
import { loadPlan, type Plan } from '@/lib/plan'
import PlanReport from '@/components/plan/PlanReport.vue'
import PlanPreviewDialog from '@/components/plan/PlanPreviewDialog.vue'
import PendingStateControl from '@/components/plan/PendingStateControl.vue'

// What waits for an administrator, gathered in one place: recurring
// templates whose rules now cover new tables or cannot be applied, tables
// whose fields differ from their columns, tables not imported and schemas
// never scanned. Each item can be owned, snoozed or ignored with a reason;
// a template's proposal is reviewed before it enters the workspace, never
// applied on its own.
type Pending = PendingResult['pending']
type DriftTable = Pending['driftTables'][number]
type GoneTable = Pending['goneTables'][number]

const { t } = useI18n()
const router = useRouter()
const pending = ref<Pending | null>(null)
const error = ref('')
const loading = ref(false)
const showHandled = ref(false)

async function load() {
  loading.value = true
  try {
    pending.value = (await workspace.read(PendingQuery, {})).pending
    error.value = ''
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}
onMounted(() => {
  if (can('admin:write')) void load()
})

const open = (s?: { status: string } | null) => !s || s.status === 'open'
const plans = computed(() => (pending.value?.plans ?? []).filter((p) => showHandled.value || open(p.state)))
const drift = computed(() => (pending.value?.driftTables ?? []).filter((d) => showHandled.value || open(d.state)))
const gone = computed(() => (pending.value?.goneTables ?? []).filter((d) => showHandled.value || open(d.state)))
const handled = computed(() => [
  ...(pending.value?.plans ?? []), ...(pending.value?.driftTables ?? []), ...(pending.value?.goneTables ?? []),
].filter((x) => !open(x.state)).length)

// Entities whose tables are gone are removed from the workspace; the server
// removes what names them as it takes the edit, which the next read writes.
const ws = useWorkspace()
const message = useMessage()
const goneChecked = ref<string[]>([])
async function removeGone() {
  const ids = goneChecked.value.filter((id) => ws.entity(id))
  ws.removeEntities(ids)
  goneChecked.value = []
  message.success(t('overview.removedMissing', { n: ids.length }))
  await load()
}
const goneColumns = computed<DataTableColumns<GoneTable>>(() => [
  { type: 'selection' },
  { title: t('entities.entity'), key: 'entity', render: (d) => h('span', { class: 'mono' }, ws.entityIndex.shortName(d.entity)) },
  { title: t('datasources.table'), key: 'ref', render: (d) => h('span', { class: 'mono' }, d.ref) },
  {
    title: '', key: 'scanned', width: 110,
    render: (d) => h(NTag, { size: 'small', bordered: false, type: d.scanned ? 'default' : 'warning' },
      () => t(d.scanned ? 'pending.goneScanned' : 'pending.goneRuntime')),
  },
  {
    title: '', key: 'state', width: 360,
    render: (d) => h(PendingStateControl, { itemKey: d.key, state: d.state, onChanged: load }),
  },
])

const plan = ref<Plan | null>(null)
const previewing = ref(false)
function review(p: Plan) {
  plan.value = loadPlan(p)
  previewing.value = true
}

const driftColumns = computed<DataTableColumns<DriftTable>>(() => [
  { title: t('datasources.table'), key: 'ref', render: (d) => h('span', { class: 'mono' }, d.ref) },
  {
    title: t('pending.differences'), key: 'diff',
    render: (d) => h(NText, { depth: 2 }, () => t('catalog.driftHint', {
      added: d.added.join(', ') || '—', missing: d.missing.join(', ') || '—',
    })),
  },
  {
    title: '', key: 'state', width: 360,
    render: (d) => h(PendingStateControl, { itemKey: d.key, state: d.state, onChanged: load }),
  },
])
const nothing = computed(() => pending.value && !plans.value.length && !drift.value.length && !gone.value.length &&
  !pending.value.newTables &&
  !pending.value.unscannedSchemas && !pending.value.unlistedDatasources.length)
</script>

<template>
  <n-alert v-if="!can('admin:write')" type="info" :show-icon="false">{{ t('catalog.readOnly') }}</n-alert>
  <n-spin v-else :show="loading">
    <div class="stack" style="gap: 16px">
      <n-alert v-if="error" type="error" :show-icon="false">{{ error }}</n-alert>
      <n-grid v-if="pending" :cols="3" :x-gap="12" :y-gap="12" responsive="screen" item-responsive>
        <n-gi span="3 m:1">
          <n-card size="small">
            <n-statistic :label="t('pending.drift')" :value="pending.drift" />
            <n-button text size="small" type="primary" @click="router.push({ name: 'datasources', query: { drift: '1' } })">
              {{ t('pending.open') }}
            </n-button>
          </n-card>
        </n-gi>
        <n-gi span="3 m:1">
          <n-card size="small">
            <n-statistic :label="t('pending.newTables')" :value="pending.newTables" />
            <n-button text size="small" type="primary" @click="router.push({ name: 'datasources', query: { status: 'NEW' } })">
              {{ t('pending.open') }}
            </n-button>
          </n-card>
        </n-gi>
        <n-gi span="3 m:1">
          <n-card size="small">
            <n-statistic :label="t('pending.unscanned')" :value="pending.unscannedSchemas" />
            <n-button text size="small" type="primary" @click="router.push({ name: 'datasources' })">{{ t('pending.open') }}</n-button>
          </n-card>
        </n-gi>
      </n-grid>
      <n-alert v-if="pending?.unlistedDatasources.length" type="warning" :show-icon="false">
        {{ t('pending.unlisted', { list: pending.unlistedDatasources.join('、') }) }}
        <n-button text type="primary" @click="router.push({ name: 'datasources' })">{{ t('pending.toScan') }}</n-button>
      </n-alert>
      <label v-if="handled" class="switch">
        <n-switch v-model:value="showHandled" size="small" />
        <n-text class="meta">{{ t('pending.showHandled', { n: handled }) }}</n-text>
      </label>
      <n-empty v-if="nothing" :description="t('pending.nothing')" />
      <n-card v-for="p in plans" :key="p.key" size="small">
        <template #header>
          {{ p.template.name }} <n-tag size="tiny" type="info" :bordered="false">{{ t('plan.recurring') }}</n-tag>
        </template>
        <template #header-extra>
          <div class="group">
            <pending-state-control :item-key="p.key" :state="p.state" @changed="load" />
            <n-button size="small" @click="router.push({ name: 'plans', query: { template: p.template.name } })">
              {{ t('pending.edit') }}
            </n-button>
            <n-button v-if="!p.error" size="small" type="primary" @click="review(p.template.plan as Plan)">
              {{ t('pending.review') }}
            </n-button>
          </div>
        </template>
        <n-alert v-if="p.error" type="error" :show-icon="false">{{ t('pending.failed', { error: p.error }) }}</n-alert>
        <plan-report v-else :report="p.report" />
      </n-card>
      <n-card v-if="gone.length || pending?.moreGoneTables" size="small"
        :title="t('pending.goneTables', { n: gone.length })">
        <template #header-extra>
          <n-button size="small" type="primary" :disabled="!goneChecked.length" @click="removeGone">
            {{ t('pending.removeSelected', { n: goneChecked.length }) }}
          </n-button>
        </template>
        <n-text depth="3" class="meta">{{ t('pending.goneHint') }}</n-text>
        <n-data-table v-model:checked-row-keys="goneChecked" size="small" :columns="goneColumns" :data="gone"
          :row-key="(d: GoneTable) => d.entity" :pagination="{ pageSize: 20 }" />
        <n-text v-if="pending?.moreGoneTables" depth="3" class="meta">
          {{ t('pending.moreTables', { n: pending.moreGoneTables }) }}
        </n-text>
      </n-card>
      <n-card v-if="drift.length || pending?.moreDriftTables" size="small"
        :title="t('pending.driftTables', { n: drift.length })">
        <n-data-table size="small" :columns="driftColumns" :data="drift" :row-key="(d: DriftTable) => d.key"
          :pagination="{ pageSize: 20 }" />
        <n-text v-if="pending?.moreDriftTables" depth="3" class="meta">
          {{ t('pending.moreTables', { n: pending.moreDriftTables }) }}
        </n-text>
      </n-card>
      <n-alert v-if="pending && !plans.length && !nothing" type="default" :show-icon="false">
        {{ t('pending.noTemplates') }}
        <n-button text type="primary" @click="router.push({ name: 'plans' })">{{ t('pending.toPlans') }}</n-button>
      </n-alert>
    </div>
  </n-spin>
  <plan-preview-dialog v-model:show="previewing" :plan="plan" review @applied="load" />
</template>

<style scoped>
.stack { display: flex; flex-direction: column; }
.group { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; }
.switch { display: inline-flex; gap: 6px; align-items: center; cursor: pointer; }
.meta { font-size: 12px; }
</style>
