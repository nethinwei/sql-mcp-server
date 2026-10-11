<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  NAlert, NButton, NCheckbox, NCheckboxGroup, NCollapse, NCollapseItem, NDivider, NForm, NFormItem, NInput, NSelect,
  NSpace, NSpin, NSwitch, NTag, NText,
} from 'naive-ui'
import { PlanScopeQuery } from '@/api/ops'
import { workspace } from '@/lib/workspaceSync'
import type { PlanScopeQuery as ScopeStats } from '@/gql/graphql'
import { useWorkspace } from '@/stores/workspace'
import { cleanPlan, patterns, tasksOf, type Plan, type PlanStatus, type PlanTask } from '@/lib/plan'
import FilterBuilder from '@/components/FilterBuilder.vue'

// A plan as a form: first what to do (the tasks), then which tables, with
// what the scope selects shown as it is edited, then the rules of the tasks
// chosen. Patterns are typed as text (comma separated globs) and kept as
// lists.
const plan = defineModel<Plan>({ required: true })
const { t } = useI18n()
const ws = useWorkspace()

const grantable = ['read', 'aggregate', 'create', 'update', 'delete']
const masks = ['phone', 'email', 'idcard', 'secret']
const statuses: PlanStatus[] = ['new', 'workspace', 'published']
const allTasks: PlanTask[] = ['import', 'grant', 'fields', 'sync']

// The tasks follow the plan when it is replaced (a template loaded) and
// clear their rules when unchecked.
const tasks = ref<PlanTask[]>(tasksOf(plan.value))
watch(() => plan.value, (p) => { tasks.value = tasksOf(p) })
function setTasks(next: PlanTask[]) {
  const p = plan.value
  const on = (task: PlanTask) => next.includes(task)
  p.import = on('import')
  if (!on('grant')) p.grants = []
  else if (!p.grants?.length) p.grants = [{ principal: '', actions: ['read'] }]
  if (!on('fields')) p.fields = []
  else if (!p.fields?.length) p.fields = [{ columns: [], mask: 'phone' }]
  if (!on('sync')) p.sync = {}
  else if (!p.sync?.add && !p.sync?.remove) p.sync = { add: true }
  tasks.value = next
}

const datasourceOptions = computed(() => ws.datasources.map((d) => ({ label: d.name, value: d.name })))
const principalOptions = computed(() => [
  ...ws.roles.map((r) => ({ label: t('plan.form.role', { name: r.name }), value: `role:${r.name}` })),
  ...ws.users.map((u) => ({ label: t('plan.form.user', { name: u.name }), value: `user:${u.name}` })),
])
// A typed name that is not a principal yet becomes a new role.
const onCreatePrincipal = (label: string) => ({ label: t('plan.form.newRole', { name: label }), value: `role:${label}` })

/** A text input bound to a list of patterns. */
function text(get: () => string[] | undefined, set: (v: string[]) => void) {
  return computed({ get: () => (get() ?? []).join(', '), set: (v: string) => set(patterns(v)) })
}
const schemas = text(() => plan.value.scope.schemas, (v) => (plan.value.scope.schemas = v))
const tables = text(() => plan.value.scope.tables, (v) => (plan.value.scope.tables = v))

const fieldAction = (f: { exclude?: boolean; mask?: string }) => (f.exclude ? 'exclude' : f.mask ?? 'phone')
function setFieldAction(i: number, v: string) {
  const f = plan.value.fields![i]
  f.exclude = v === 'exclude'
  f.mask = v === 'exclude' ? undefined : v
}
const fieldActions = computed(() => [
  { label: t('plan.form.hide'), value: 'exclude' },
  ...masks.map((m) => ({ label: t('plan.form.maskAs', { mask: t(`entity.masks.${m}`) }), value: m })),
])
// Common rules for personal data, by column name.
const presets = [
  { columns: ['*phone*', '*mobile*', '*tel*'], mask: 'phone' },
  { columns: ['*email*', '*mail'], mask: 'email' },
  { columns: ['*id_card*', '*idcard*', '*id_no*', '*passport*'], mask: 'idcard' },
  { columns: ['*password*', '*secret*', '*token*'], exclude: true },
]
function addPresets() {
  const have = new Set((plan.value.fields ?? []).map((f) => f.columns.join(',')))
  plan.value.fields = [
    ...(plan.value.fields ?? []).filter((f) => f.columns.length),
    ...presets.filter((p) => !have.has(p.columns.join(','))).map((p) => ({ ...p, columns: [...p.columns] })),
  ]
}

const sync = computed({ get: () => plan.value.sync ?? {}, set: (v) => (plan.value.sync = v) })

// What the scope selects in the workspace, read again as either changes.
const stats = ref<ScopeStats['planScope'] | null>(null)
const statsError = ref('')
const statsLoading = ref(false)
let seq = 0
let timer: ReturnType<typeof setTimeout> | undefined
watch([() => JSON.stringify(cleanPlan(plan.value).scope), () => ws.contextKey], ([scope]) => {
  clearTimeout(timer)
  timer = setTimeout(async () => {
    const mine = ++seq
    statsLoading.value = true
    try {
      const { planScope } = await workspace.read(PlanScopeQuery, { scope: JSON.parse(scope) })
      if (mine !== seq) return
      stats.value = planScope
      statsError.value = ''
    } catch (e) {
      if (mine === seq) statsError.value = e instanceof Error ? e.message : String(e)
    } finally {
      if (mine === seq) statsLoading.value = false
    }
  }, 400)
}, { immediate: true })
</script>

<template>
  <n-form label-placement="left" label-width="auto" size="small">
    <n-divider title-placement="left">{{ t('plan.form.tasks') }}</n-divider>
    <n-form-item :label="t('plan.form.whatToDo')">
      <n-checkbox-group :value="tasks" @update:value="(v: (string | number)[]) => setTasks(v as PlanTask[])">
        <n-space><n-checkbox v-for="task in allTasks" :key="task" :value="task" :label="t(`plan.task.${task}`)" /></n-space>
      </n-checkbox-group>
    </n-form-item>

    <n-divider title-placement="left">{{ t('plan.form.scope') }}</n-divider>
    <n-text depth="3" class="hint block">{{ t('plan.form.scopeHint') }}</n-text>
    <n-form-item v-if="plan.scope.refs?.length" :label="t('plan.form.tables')">
      <n-tag closable @close="plan.scope.refs = undefined">{{ t('plan.form.selectedTables', { n: plan.scope.refs.length }) }}</n-tag>
    </n-form-item>
    <n-form-item v-if="plan.exclude?.length" :label="t('plan.form.excluded')">
      <n-tag closable @close="plan.exclude = undefined">{{ t('plan.form.excludedTables', { n: plan.exclude.length }) }}</n-tag>
    </n-form-item>
    <n-form-item :label="t('plan.form.datasources')">
      <n-select v-model:value="plan.scope.datasources" multiple clearable :options="datasourceOptions" :placeholder="t('plan.form.all')" />
    </n-form-item>
    <n-form-item :label="t('plan.form.schemas')">
      <n-input v-model:value="schemas" clearable :placeholder="t('plan.form.schemasHint')" />
    </n-form-item>
    <n-form-item :label="t('plan.form.tablePatterns')">
      <n-input v-model:value="tables" clearable :placeholder="t('plan.form.tablesHint')" />
    </n-form-item>
    <n-form-item :label="t('plan.form.search')">
      <n-input v-model:value="plan.scope.search" clearable :placeholder="t('datasources.searchTables')" />
    </n-form-item>
    <n-form-item :label="t('plan.form.status')">
      <n-checkbox-group v-model:value="plan.scope.status">
        <n-space><n-checkbox v-for="s in statuses" :key="s" :value="s" :label="t(`catalog.status.${s}`)" /></n-space>
      </n-checkbox-group>
    </n-form-item>
    <n-form-item :label="t('plan.form.drift')">
      <n-switch v-model:value="plan.scope.drift" size="small" />
    </n-form-item>
    <n-alert :type="stats && !stats.total ? 'warning' : 'info'" :show-icon="false" class="summary" aria-live="polite">
      <n-spin :show="statsLoading" size="small">
        <n-text v-if="statsError" type="error">{{ statsError }}</n-text>
        <template v-else-if="stats">
          <n-text strong>{{ t('plan.form.matches', { n: stats.total }) }}</n-text>
          <div v-if="stats.conditions.length > 1" class="conditions">
            <n-text depth="3">{{ t('plan.form.eachCondition') }}</n-text>
            <n-tag v-for="c in stats.conditions" :key="c.name" size="small" :bordered="false"
              :type="c.matched ? 'default' : 'warning'">
              {{ t(`plan.condition.${c.name}`) }} {{ c.name === 'refs' ? '' : c.value }} → {{ c.matched }}
            </n-tag>
          </div>
          <div v-if="stats.samples.length" class="samples mono">
            <span v-for="r in stats.samples" :key="r.ref">{{ r.ref }}</span>
            <n-text v-if="stats.total > stats.samples.length" depth="3">{{ t('plan.more', { n: stats.total - stats.samples.length }) }}</n-text>
          </div>
        </template>
      </n-spin>
    </n-alert>

    <template v-if="tasks.length">
      <n-divider title-placement="left">{{ t('plan.form.rules') }}</n-divider>
      <n-form-item v-if="tasks.includes('import')" :label="t('plan.task.import')">
        <n-text depth="3" class="hint">{{ t('plan.form.importHint') }}</n-text>
      </n-form-item>
      <n-form-item v-if="tasks.includes('grant')" :label="t('plan.form.grants')">
        <div class="rules">
          <div v-for="(g, i) in plan.grants ?? []" :key="i" class="rule-card">
            <div class="rule">
              <n-select v-model:value="g.principal" filterable tag :options="principalOptions" :on-create="onCreatePrincipal"
                class="principal" :placeholder="t('plan.form.principal')" />
              <n-checkbox-group v-model:value="g.actions">
                <n-space :size="6"><n-checkbox v-for="a in grantable" :key="a" :value="a" :label="t(`grants.actions.${a.toUpperCase()}`)" /></n-space>
              </n-checkbox-group>
              <n-button text type="error" @click="plan.grants!.splice(i, 1)">{{ t('common.remove') }}</n-button>
            </div>
            <n-collapse class="limits">
              <n-collapse-item :title="t('plan.form.limits', { fields: g.fields?.length ? 1 : 0, rows: g.rows ? 1 : 0 })" :name="i">
                <n-form-item :label="t('plan.form.grantFields')" :show-feedback="false">
                  <n-input :value="(g.fields ?? []).join(', ')" :placeholder="t('plan.form.grantFieldsHint')"
                    @update:value="(v: string) => (g.fields = patterns(v))" />
                </n-form-item>
                <n-form-item :label="t('plan.form.rows')" :show-feedback="false">
                  <filter-builder :model-value="g.rows as never" :fields="[]"
                    @update:model-value="(v: unknown) => (g.rows = v as Record<string, unknown> | undefined)" />
                </n-form-item>
              </n-collapse-item>
            </n-collapse>
          </div>
          <n-button dashed size="small" @click="(plan.grants ??= []).push({ principal: '', actions: ['read'] })">
            {{ t('plan.form.addGrant') }}
          </n-button>
        </div>
      </n-form-item>
      <n-form-item v-if="tasks.includes('fields')" :label="t('plan.form.fields')">
        <div class="rules">
          <div v-for="(f, i) in plan.fields ?? []" :key="i" class="rule">
            <n-input :value="f.columns.join(', ')" class="wide" :placeholder="t('plan.form.columns')"
              @update:value="(v: string) => (f.columns = patterns(v))" />
            <n-input :value="(f.types ?? []).join(', ')" class="narrow" :placeholder="t('plan.form.types')"
              @update:value="(v: string) => (f.types = patterns(v))" />
            <n-select :value="fieldAction(f)" :options="fieldActions" class="narrow" @update:value="(v: string) => setFieldAction(i, v)" />
            <n-button text type="error" @click="plan.fields!.splice(i, 1)">{{ t('common.remove') }}</n-button>
          </div>
          <n-space>
            <n-button dashed size="small" @click="(plan.fields ??= []).push({ columns: [], mask: 'phone' })">{{ t('plan.form.addField') }}</n-button>
            <n-button size="small" @click="addPresets">{{ t('plan.form.presets') }}</n-button>
          </n-space>
        </div>
      </n-form-item>
      <n-form-item v-if="tasks.includes('sync')" :label="t('plan.form.sync')">
        <n-space :size="16">
          <n-checkbox :checked="!!sync.add" @update:checked="(v: boolean) => (sync = { ...sync, add: v })">{{ t('plan.form.syncAdd') }}</n-checkbox>
          <n-checkbox :checked="!!sync.remove" @update:checked="(v: boolean) => (sync = { ...sync, remove: v })">{{ t('plan.form.syncRemove') }}</n-checkbox>
          <n-checkbox :checked="!!sync.hideNew" :disabled="!sync.add" @update:checked="(v: boolean) => (sync = { ...sync, hideNew: v })">
            {{ t('datasources.syncHideNew') }}
          </n-checkbox>
        </n-space>
      </n-form-item>
    </template>
  </n-form>
</template>

<style scoped>
.rules { width: 100%; display: flex; flex-direction: column; gap: 8px; }
.rule { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; }
.rule-card { border: 1px solid rgba(128, 128, 128, .25); border-radius: 6px; padding: 8px; }
.limits { margin-top: 6px; }
.principal { width: 220px; }
.narrow { width: 160px; }
.wide { width: 240px; }
.hint { font-size: 12px; }
.block { display: block; margin-bottom: 8px; }
.summary { margin: 4px 0 8px; }
.conditions { display: flex; flex-wrap: wrap; gap: 6px; align-items: center; margin-top: 6px; }
.samples { display: flex; flex-wrap: wrap; gap: 4px 12px; margin-top: 6px; font-size: 12px; }
</style>
