<script setup lang="ts">
import { computed, nextTick, onMounted, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { useI18n } from 'vue-i18n'
import {
  NAlert, NButton, NCard, NForm, NFormItem, NRadioButton, NRadioGroup, NResult, NSelect,
  NSpace, NTabPane, NTabs, NTag, NText, useMessage,
} from 'naive-ui'
import VisibilityTable from '@/components/VisibilityTable.vue'
import { canonical, useWorkspace } from '@/stores/workspace'
import { useStatus } from '@/stores/status'
import { run } from '@/api/client'
import { SimulateQuery } from '@/api/ops'
import { describeFilter, type Filter } from '@/lib/filter'
import { parseSimulationQuery } from '@/lib/simulation'
import type { Action, SimulateQuery as SimulateResult } from '@/gql/graphql'

type Simulation = SimulateResult['simulate']

const { t } = useI18n()
const ws = useWorkspace()
const status = useStatus()
const route = useRoute()
const message = useMessage()
// A link may name who, the configuration and one call to run (see lib/simulation).
// The source it names wins: a preview of the workspace (even a clean one based
// on an older revision) must simulate that workspace. Without a link, unsaved
// changes make the workspace the default.
const linked = parseSimulationQuery(route.query)
const source = ref<'workspace' | 'published'>(
  linked.source ?? (ws.dirty ? 'workspace' : 'published'))
const who = ref<string | null>(linked.who ?? null)
const tab = ref<'overview' | 'single'>('overview')

const whoOptions = computed(() => [
  { type: 'group', label: t('simulate.group.users'), key: 'u', children: ws.users.map((u) => ({ label: u.name, value: u.name })) },
  { type: 'group', label: t('simulate.group.roles'), key: 'r', children: ws.roles.map((r) => ({ label: t('simulate.roleOption', { name: r.name }), value: `role:${r.name}` })) },
])
/** The configuration a simulation runs against, as shown with its result. */
const sourceLabel = computed(() => source.value === 'workspace'
  ? t(ws.dirty ? 'simulate.sourceWorkspace' : 'simulate.sourceWorkspaceClean', { id: ws.baseId ?? '' })
  : t('simulate.sourcePublished', { id: status.publishedId ?? '' }))

// Single check.
const entity = ref<string | null>(null)
const action = ref<Action>('READ')
const fields = ref<string[]>([])
const busy = ref(false)
const actions: Action[] = ['READ', 'AGGREGATE', 'CREATE', 'UPDATE', 'DELETE', 'EXECUTE']
const actionOptions = computed(() => actions.map((a) => ({ label: t(`grants.actions.${a}`), value: a })))
const fieldOptions = computed(() =>
  (ws.entity(entity.value ?? '')?.fields ?? []).filter((f) => !f.exclude).map((f) => ({ label: f.name, value: f.name })))
let keepFields = false
watch(entity, () => {
  if (!keepFields) fields.value = []
  keepFields = false
})

/**
 * Everything a result depends on. A result keeps the conditions it was
 * computed for; when they change it is marked stale instead of passing as
 * the answer for the new ones.
 */
const conditions = computed(() => JSON.stringify({
  who: who.value, source: source.value,
  config: source.value === 'workspace' ? canonical(ws.draft) : status.publishedId,
  entity: entity.value, action: action.value, fields: [...fields.value].sort(),
}))
interface Outcome {
  sim: Simulation
  key: string
  who: string
  entity: string
  action: Action
  fields: string[]
  source: string
}
const result = ref<Outcome | null>(null)
const stale = computed(() => Boolean(result.value && result.value.key !== conditions.value))

async function check() {
  if (!who.value || !entity.value) return
  const key = conditions.value
  const asked = {
    who: who.value, entity: entity.value, action: action.value, fields: [...fields.value], source: sourceLabel.value,
  }
  busy.value = true
  try {
    const input = {
      user: asked.who, entity: asked.entity, action: asked.action, fields: asked.fields.length ? asked.fields : null,
      draft: source.value === 'workspace' ? ws.draft : null,
    }
    const sim = (await run(SimulateQuery, { input })).simulate
    if (key === conditions.value) result.value = { sim, key, ...asked }
  } catch (e) {
    message.error(e instanceof Error ? e.message : String(e))
  } finally {
    busy.value = false
  }
}

/** Runs a single check for a call picked in the overview (an entity, action and field scope). */
async function pick(e: string, a: Action, scope: string[]) {
  keepFields = entity.value !== e
  entity.value = e
  action.value = a
  fields.value = [...scope]
  tab.value = 'single'
  await nextTick()
  await check()
}

onMounted(() => {
  if (linked.entity && linked.action) void pick(linked.entity, linked.action, linked.fields ?? [])
})
</script>

<template>
  <n-space vertical :size="16">
    <n-card size="small">
      <n-space align="center" :size="16">
        <n-select v-model:value="who" :options="whoOptions" filterable :placeholder="t('simulate.pick')" style="width: 260px" />
        <n-radio-group v-model:value="source" size="small">
          <n-radio-button value="workspace" :disabled="!ws.baseId">{{ t('simulate.workspace') }}</n-radio-button>
          <n-radio-button value="published">{{ t('simulate.published') }}</n-radio-button>
        </n-radio-group>
        <n-text depth="3" class="small">{{ sourceLabel }}</n-text>
      </n-space>
    </n-card>

    <n-tabs v-model:value="tab" type="line" animated>
      <n-tab-pane name="overview" :tab="t('simulate.overview')">
        <visibility-table :who="who" :draft="source === 'workspace' ? ws.draft : null" @pick="pick" />
      </n-tab-pane>
      <n-tab-pane name="single" :tab="t('simulate.single')">
        <n-space vertical>
          <n-form inline label-placement="left">
            <n-form-item :label="t('simulate.entity')">
              <n-select v-model:value="entity" filterable style="width: 200px"
                :options="ws.entities.map((e) => ({ label: e.name, value: e.name }))" />
            </n-form-item>
            <n-form-item :label="t('simulate.action')">
              <n-select v-model:value="action" :options="actionOptions" style="width: 110px" />
            </n-form-item>
            <n-form-item :label="t('simulate.fields')">
              <n-select v-model:value="fields" multiple filterable clearable :options="fieldOptions"
                :placeholder="t('simulate.defaultProjection')" style="width: 300px" />
            </n-form-item>
            <n-form-item>
              <n-button type="primary" :disabled="!who || !entity" :loading="busy" @click="check">
                {{ stale ? t('simulate.rerun') : t('simulate.run') }}
              </n-button>
            </n-form-item>
          </n-form>
          <n-alert v-if="stale" type="warning" :show-icon="false">{{ t('simulate.stale') }}</n-alert>
          <n-card v-if="result" size="small" :class="{ stale }">
            <div class="asked">
              <n-text depth="3">{{ t('simulate.askedFor') }}</n-text>
              <n-tag size="small" :bordered="false">{{ result.who }}</n-tag>
              <n-tag size="small" :bordered="false" class="mono">{{ result.entity }}</n-tag>
              <n-tag size="small" :bordered="false">{{ t(`grants.actions.${result.action}`) }}</n-tag>
              <n-tag size="small" :bordered="false">
                {{ result.fields.length ? result.fields.join(', ') : t('simulate.defaultProjectionShort') }}
              </n-tag>
              <n-text depth="3">{{ result.source }}</n-text>
            </div>
            <n-result :status="result.sim.allowed ? 'success' : result.sim.fieldScopes?.length ? 'warning' : 'error'"
              :title="result.sim.allowed ? t('simulate.allowed') : result.sim.fieldScopes?.length ? t('simulate.ambiguous') : t('simulate.denied')"
              :description="result.sim.reason || undefined" size="small">
              <template #footer>
                <n-space vertical align="start" style="text-align: left">
                  <template v-if="result.sim.allowed">
                    <div><n-text depth="3">{{ t('simulate.returnedFields') }}</n-text>
                      <n-tag v-for="f in result.sim.fields" :key="f" size="small" :bordered="false" style="margin: 2px">{{ f }}</n-tag></div>
                    <div><n-text depth="3">{{ t('simulate.rowScope') }}</n-text><code>{{ describeFilter(result.sim.rowFilter as Filter) }}</code></div>
                    <div><n-text depth="3">{{ t('simulate.effectiveGrants') }}</n-text><code>{{ result.sim.grants.join(', ') }}</code></div>
                  </template>
                  <n-alert v-if="result.sim.fieldScopes?.length" type="warning" :show-icon="false">
                    {{ t('simulate.ambiguousBody') }}
                    <div v-for="(s, i) in result.sim.fieldScopes" :key="i">
                      <n-button text type="warning" @click="pick(result.entity, result.action, s)"><code>{{ s.join(', ') }}</code></n-button>
                    </div>
                  </n-alert>
                </n-space>
              </template>
            </n-result>
          </n-card>
        </n-space>
      </n-tab-pane>
    </n-tabs>
  </n-space>
</template>

<style scoped>
.small { font-size: 12px; }
.asked { display: flex; flex-wrap: wrap; align-items: center; gap: 6px; margin-bottom: 4px; }
.stale { opacity: .55; }
</style>
