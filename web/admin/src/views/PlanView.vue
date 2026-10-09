<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import {
  NAlert, NButton, NCard, NEmpty, NForm, NFormItem, NGi, NGrid, NInput, NList, NListItem, NModal, NPopconfirm, NRadio,
  NRadioGroup, NSpace, NSwitch, NTag, NText, NTooltip, useMessage,
} from 'naive-ui'
import { ApiError, run } from '@/api/client'
import {
  DeletePlanTemplateMutation, PlanTemplateVersionsQuery, PlanTemplatesQuery, SavePlanTemplateMutation,
} from '@/api/ops'
import type { PlanTemplatePartsFragment } from '@/gql/graphql'
import { can } from '@/api/session'
import { cleanPlan, loadPlan, newPlan, tasksOf, type Plan } from '@/lib/plan'
import { planDraft } from '@/lib/planDraft'
import PlanForm from '@/components/plan/PlanForm.vue'
import PlanPreviewDialog from '@/components/plan/PlanPreviewDialog.vue'

// Configuring by rules: what to do, on which tables, previewed on the server
// and taken into the workspace like any edit. A new plan starts by importing;
// a saved one (a template, or tables handed over) means what it says, so a
// template that only grants never starts importing. Templates are versioned:
// saving one another administrator saved meanwhile is refused.
type Template = PlanTemplatePartsFragment

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const message = useMessage()
const writable = computed(() => can('admin:write'))

const plan = ref<Plan>(newPlan())
/** The template the form was loaded from, as loaded. */
const loaded = ref<Template | null>(null)

const templates = ref<Template[]>([])
const templatesError = ref('')
async function loadTemplates() {
  try {
    templates.value = (await run(PlanTemplatesQuery, {})).planTemplates
    templatesError.value = ''
  } catch (e) {
    templatesError.value = e instanceof Error ? e.message : String(e)
  }
}

function useTemplate(tpl: Template) {
  plan.value = loadPlan(tpl.plan as Plan)
  loaded.value = tpl
}

function reset() {
  plan.value = newPlan()
  loaded.value = null
}

onMounted(async () => {
  await loadTemplates()
  if (planDraft.value) {
    // Tables handed over: what to do with them is chosen here.
    plan.value = loadPlan(planDraft.value)
    planDraft.value = null
  } else if (typeof route.query.template === 'string') {
    const tpl = templates.value.find((x) => x.name === route.query.template)
    if (tpl) useTemplate(tpl)
  }
})
watch(() => route.query.template, (name) => {
  const tpl = templates.value.find((x) => x.name === name)
  if (tpl) useTemplate(tpl)
})

const cleaned = computed(() => cleanPlan(plan.value))
/** Why the plan cannot be evaluated yet, or null. */
const blocked = computed(() => {
  const p = cleaned.value
  if (!tasksOf(plan.value).length) return t('plan.form.needRule')
  if (p.grants?.some((g) => !g.principal || !g.actions.length)) return t('plan.form.needPrincipal')
  if (p.fields?.some((f) => !f.columns.length)) return t('plan.form.needColumns')
  return null
})

const previewing = ref(false)
const applied = ref<{ added: number; changed: number } | null>(null)

// Saving: update the loaded template, or create one (also "save as").
const saveOpen = ref(false)
const saveMode = ref<'update' | 'create'>('create')
const saving = ref({ name: '', recurring: false })
const conflict = ref<string | null>(null)
function openSave() {
  saveMode.value = loaded.value ? 'update' : 'create'
  saving.value = { name: loaded.value ? '' : '', recurring: loaded.value?.recurring ?? false }
  conflict.value = null
  saveOpen.value = true
}
const saveName = computed(() => (saveMode.value === 'update' ? loaded.value!.name : saving.value.name.trim()))

async function saveTemplate() {
  conflict.value = null
  try {
    const { savePlanTemplate } = await run(SavePlanTemplateMutation, { input: {
      name: saveName.value, plan: cleaned.value, recurring: saving.value.recurring,
      expectedVersion: saveMode.value === 'update' ? loaded.value!.version : null,
    } }, true)
    message.success(t('plan.templateSaved', { name: savePlanTemplate.name, version: savePlanTemplate.version }))
    saveOpen.value = false
    loaded.value = savePlanTemplate
    await loadTemplates()
  } catch (e) {
    if (e instanceof ApiError && e.code === 'CONFLICT') conflict.value = e.message
    else message.error(e instanceof Error ? e.message : String(e))
  }
}

async function removeTemplate(name: string) {
  try {
    await run(DeletePlanTemplateMutation, { name }, true)
    if (loaded.value?.name === name) loaded.value = null
    await loadTemplates()
  } catch (e) {
    message.error(e instanceof Error ? e.message : String(e))
  }
}

// Versions of a template.
const versionsOf = ref<string | null>(null)
const versions = ref<Template[]>([])
async function openVersions(name: string) {
  versionsOf.value = name
  versions.value = (await run(PlanTemplateVersionsQuery, { name })).planTemplateVersions
}
function useVersion(v: Template) {
  // An older version is loaded to be saved over the current one.
  const latest = templates.value.find((x) => x.name === v.name)
  useTemplate({ ...v, version: latest?.version ?? v.version })
  versionsOf.value = null
}

const summary = (tpl: Template) => tasksOf(loadPlan(tpl.plan as Plan)).map((task) => t(`plan.task.${task}`)).join(' · ')
</script>

<template>
  <n-grid :cols="3" :x-gap="16" :y-gap="16" responsive="screen" item-responsive>
    <n-gi span="3 l:2">
      <n-card size="small" :title="loaded ? t('plan.titleFrom', { name: loaded.name, version: loaded.version }) : t('plan.title')">
        <template #header-extra>
          <n-button size="small" text @click="reset">{{ t('plan.reset') }}</n-button>
        </template>
        <n-alert type="default" :show-icon="false" class="gap">{{ t('plan.intro') }}</n-alert>
        <n-alert v-if="applied" type="success" closable class="gap" @close="applied = null">
          {{ t('catalog.applied', { added: applied.added, changed: applied.changed }) }}
          <div class="alert-actions">
            <n-button size="small" @click="router.push({ name: 'entities' })">{{ t('catalog.nextEntities') }}</n-button>
            <n-button size="small" type="primary" @click="router.push({ name: 'review' })">{{ t('catalog.nextReview') }}</n-button>
          </div>
        </n-alert>
        <plan-form v-model="plan" />
        <n-space justify="end" align="center" class="actions">
          <n-text v-if="blocked" depth="3" class="meta">{{ blocked }}</n-text>
          <n-tooltip :disabled="writable">
            <template #trigger>
              <n-button :disabled="!writable || Boolean(blocked)" @click="openSave">{{ t('plan.saveTemplate') }}</n-button>
            </template>
            {{ t('catalog.needsWrite') }}
          </n-tooltip>
          <n-tooltip :disabled="writable">
            <template #trigger>
              <n-button type="primary" :disabled="!writable || Boolean(blocked)" @click="previewing = true">{{ t('plan.preview') }}</n-button>
            </template>
            {{ t('catalog.needsWrite') }}
          </n-tooltip>
        </n-space>
      </n-card>
    </n-gi>
    <n-gi span="3 l:1">
      <n-card size="small" :title="t('plan.templates')">
        <n-alert v-if="templatesError" type="error" :show-icon="false">{{ templatesError }}</n-alert>
        <n-empty v-else-if="!templates.length" :description="t('plan.noTemplates')" />
        <n-list v-else size="small">
          <n-list-item v-for="tpl in templates" :key="tpl.name">
            <div class="tpl">
              <div>
                <n-text strong>{{ tpl.name }}</n-text>
                <n-tag size="tiny" :bordered="false" class="tag">v{{ tpl.version }}</n-tag>
                <n-tag v-if="tpl.recurring" size="tiny" type="info" :bordered="false" class="tag">{{ t('plan.recurring') }}</n-tag>
                <n-text depth="3" class="meta block">{{ summary(tpl) }}</n-text>
                <n-text depth="3" class="meta block">{{ tpl.author }} · {{ new Date(tpl.updatedAt).toLocaleString() }}</n-text>
              </div>
              <n-space :size="4" :wrap="false">
                <n-button size="tiny" @click="useTemplate(tpl)">{{ t('plan.load') }}</n-button>
                <n-button size="tiny" @click="openVersions(tpl.name)">{{ t('plan.versions') }}</n-button>
                <n-popconfirm @positive-click="removeTemplate(tpl.name)">
                  <template #trigger><n-button size="tiny" :disabled="!writable">{{ t('common.delete') }}</n-button></template>
                  {{ t('plan.deleteTemplate', { name: tpl.name }) }}
                </n-popconfirm>
              </n-space>
            </div>
          </n-list-item>
        </n-list>
      </n-card>
    </n-gi>
  </n-grid>

  <plan-preview-dialog v-model:show="previewing" :plan="cleaned"
    @applied="(r) => (applied = { added: r.added.length, changed: r.changed })" />
  <n-modal v-model:show="saveOpen" preset="card" class="dialog" :title="t('plan.saveTemplate')">
    <n-form label-placement="top">
      <n-form-item v-if="loaded" :show-label="false">
        <n-radio-group v-model:value="saveMode">
          <n-space vertical>
            <n-radio value="update">{{ t('plan.saveUpdate', { name: loaded.name, version: loaded.version }) }}</n-radio>
            <n-radio value="create">{{ t('plan.saveAs') }}</n-radio>
          </n-space>
        </n-radio-group>
      </n-form-item>
      <n-form-item v-if="saveMode === 'create'" :label="t('plan.templateName')">
        <n-input v-model:value="saving.name" maxlength="64" />
      </n-form-item>
      <n-form-item :label="t('plan.recurring')">
        <n-space vertical :size="4">
          <n-switch v-model:value="saving.recurring" />
          <n-text depth="3" class="meta">{{ t('plan.recurringHint') }}</n-text>
        </n-space>
      </n-form-item>
    </n-form>
    <n-alert v-if="conflict" type="warning" :title="t('plan.templateConflict')">
      {{ conflict }}
      <div class="alert-actions">
        <n-button size="small" @click="saveOpen = false; loaded && openVersions(loaded.name)">{{ t('plan.versions') }}</n-button>
      </div>
    </n-alert>
    <template #footer>
      <n-space justify="end">
        <n-button @click="saveOpen = false">{{ t('common.cancel') }}</n-button>
        <n-button type="primary" :disabled="!saveName" @click="saveTemplate">{{ t('common.save') }}</n-button>
      </n-space>
    </template>
  </n-modal>
  <n-modal :show="versionsOf !== null" preset="card" class="dialog" :title="t('plan.versionsOf', { name: versionsOf })"
    @update:show="(v: boolean) => !v && (versionsOf = null)">
    <n-list size="small">
      <n-list-item v-for="v in versions" :key="v.version">
        <div class="tpl">
          <div>
            <n-text strong>v{{ v.version }}</n-text>
            <n-text depth="3" class="meta block">{{ v.author }} · {{ new Date(v.updatedAt).toLocaleString() }} · {{ summary(v) }}</n-text>
          </div>
          <n-button size="tiny" @click="useVersion(v)">{{ t('plan.load') }}</n-button>
        </div>
      </n-list-item>
    </n-list>
  </n-modal>
</template>

<style scoped>
.gap { margin-bottom: 12px; }
.actions { margin-top: 8px; }
.tpl { display: flex; justify-content: space-between; gap: 8px; align-items: flex-start; }
.tag { margin-left: 6px; }
.meta { font-size: 12px; }
.block { display: block; }
</style>
