<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { useI18n } from 'vue-i18n'
import {
  NAlert, NButton, NCard, NForm, NFormItem, NRadioButton, NRadioGroup, NResult, NSelect,
  NSpace, NTabPane, NTabs, NTag, NText, useMessage,
} from 'naive-ui'
import VisibilityTable from '@/components/VisibilityTable.vue'
import { useWorkspace } from '@/stores/workspace'
import { run } from '@/api/client'
import { SimulateQuery } from '@/api/ops'
import { describeFilter, type Filter } from '@/lib/filter'
import type { Action, SimulateQuery as SimulateResult } from '@/gql/graphql'

type Simulation = SimulateResult['simulate']

const { t } = useI18n()
const ws = useWorkspace()
const route = useRoute()
const message = useMessage()
const source = ref<'workspace' | 'published'>(ws.dirty ? 'workspace' : 'published')
const who = ref<string | null>(typeof route.query.user === 'string' ? route.query.user : null)

const whoOptions = computed(() => [
  { type: 'group', label: t('simulate.group.users'), key: 'u', children: ws.users.map((u) => ({ label: u.name, value: u.name })) },
  { type: 'group', label: t('simulate.group.roles'), key: 'r', children: ws.roles.map((r) => ({ label: t('simulate.roleOption', { name: r.name }), value: `role:${r.name}` })) },
])

function input(entity: string, action: Action, fields?: string[]) {
  return {
    user: who.value ?? '',
    entity,
    action,
    fields: fields?.length ? fields : null,
    draft: source.value === 'workspace' ? ws.draft : null,
  }
}

// Single check.
const entity = ref<string | null>(null)
const action = ref<Action>('READ')
const fields = ref<string[]>([])
const result = ref<Simulation | null>(null)
const busy = ref(false)
const actions: Action[] = ['READ', 'AGGREGATE', 'CREATE', 'UPDATE', 'DELETE', 'EXECUTE']
const actionOptions = computed(() => actions.map((a) => ({ label: t(`grants.actions.${a}`), value: a })))
const fieldOptions = computed(() =>
  (ws.entity(entity.value ?? '')?.fields ?? []).filter((f) => !f.exclude).map((f) => ({ label: f.name, value: f.name })))
watch(entity, () => { fields.value = []; result.value = null })

async function check() {
  if (!who.value || !entity.value) return
  busy.value = true
  try {
    result.value = (await run(SimulateQuery, { input: input(entity.value, action.value, fields.value) })).simulate
  } catch (e) {
    message.error(e instanceof Error ? e.message : String(e))
  } finally {
    busy.value = false
  }
}

</script>

<template>
  <n-space vertical :size="16">
    <n-card size="small">
      <n-space align="center" :size="16">
        <n-select v-model:value="who" :options="whoOptions" filterable :placeholder="t('simulate.pick')" style="width: 260px" />
        <n-radio-group v-model:value="source" size="small">
          <n-radio-button value="workspace" :disabled="!ws.dirty">{{ t('simulate.workspace') }}</n-radio-button>
          <n-radio-button value="published">{{ t('simulate.published') }}</n-radio-button>
        </n-radio-group>
      </n-space>
    </n-card>

    <n-tabs type="line" animated>
      <n-tab-pane name="overview" :tab="t('simulate.overview')">
        <visibility-table :who="who" :draft="source === 'workspace' ? ws.draft : null" />
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
              <n-button type="primary" :disabled="!who || !entity" :loading="busy" @click="check">{{ t('simulate.run') }}</n-button>
            </n-form-item>
          </n-form>
          <n-card v-if="result" size="small">
            <n-result :status="result.allowed ? 'success' : result.fieldScopes?.length ? 'warning' : 'error'"
              :title="result.allowed ? t('simulate.allowed') : result.fieldScopes?.length ? t('simulate.ambiguous') : t('simulate.denied')"
              :description="result.reason || undefined" size="small">
              <template #footer>
                <n-space vertical align="start" style="text-align: left">
                  <template v-if="result.allowed">
                    <div><n-text depth="3">{{ t('simulate.returnedFields') }}</n-text>
                      <n-tag v-for="f in result.fields" :key="f" size="small" :bordered="false" style="margin: 2px">{{ f }}</n-tag></div>
                    <div><n-text depth="3">{{ t('simulate.rowScope') }}</n-text><code>{{ describeFilter(result.rowFilter as Filter) }}</code></div>
                    <div><n-text depth="3">{{ t('simulate.effectiveGrants') }}</n-text><code>{{ result.grants.join(', ') }}</code></div>
                  </template>
                  <n-alert v-if="result.fieldScopes?.length" type="warning" :show-icon="false">
                    {{ t('simulate.ambiguousBody') }}
                    <div v-for="(s, i) in result.fieldScopes" :key="i"><code>{{ s.join(', ') }}</code></div>
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
