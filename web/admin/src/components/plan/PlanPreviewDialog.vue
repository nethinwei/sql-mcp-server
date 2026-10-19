<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { NAlert, NButton, NCheckbox, NModal, NSpace, NSpin, NTag, NText, useMessage } from 'naive-ui'
import { ApiError, run } from '@/api/client'
import { ApplyPlanMutation, PreviewPlanQuery } from '@/api/ops'
import { workspace } from '@/lib/workspaceSync'
import type { PlanReportPartsFragment } from '@/gql/graphql'
import { useWorkspace, type Proposal } from '@/stores/workspace'
import type { Plan } from '@/lib/plan'
import { createPreviewer } from '@/lib/planPreview'
import PlanReport from './PlanReport.vue'
import ChangeDrillDown from './ChangeDrillDown.vue'

// Evaluates a plan on the server against the workspace and shows its report;
// taking it puts its outcome into the workspace, where it is reviewed and
// published like any edit. Leaving tables out (and back in) or naming an
// entity evaluates the plan again; only the latest evaluation is shown, and
// the outcome is taken only while it is the preview of the plan and the
// workspace on screen. Blocking exceptions keep it from being taken; those
// to confirm must be acknowledged.
// review keeps the new tables a grant covers up for confirmation, as
// reviewing a recurring template's proposal from the pending page needs.
const props = defineProps<{ plan: Plan | null; title?: string; review?: boolean }>()
const show = defineModel<boolean>('show', { required: true })
const emit = defineEmits<{ applied: [result: { added: string[]; changed: number; plan: Plan }] }>()
const { t } = useI18n()
const ws = useWorkspace()
const message = useMessage()
const router = useRouter()

const current = ref<Plan | null>(null)
const previewer = createPreviewer<Proposal>(
  async (plan) => (await workspace.read(PreviewPlanQuery, { plan, review: !!props.review })).previewPlan,
  () => ws.contextKey,
)
const state = previewer.state
const acknowledged = ref(false)

const evaluate = () => {
  acknowledged.value = false
  if (current.value) void previewer.evaluate(current.value)
}

watch(show, (open) => {
  if (!open) return previewer.reset()
  if (!props.plan) return
  current.value = JSON.parse(JSON.stringify(props.plan)) as Plan
  evaluate()
})

const excluded = computed(() => current.value?.exclude ?? [])
function exclude(refs: string[]) {
  const p = current.value!
  p.exclude = [...new Set([...(p.exclude ?? []), ...refs])]
  evaluate()
}
function include(ref: string | null) {
  const p = current.value!
  p.exclude = ref === null ? [] : (p.exclude ?? []).filter((r) => r !== ref)
  evaluate()
}
function rename(ref: string, name: string) {
  const p = current.value!
  p.names = { ...(p.names ?? {}), [ref]: name.trim() }
  evaluate()
}

const preview = computed(() => state.preview)
const level = (l: string) => preview.value?.report.exceptions.filter((x) => x.level === l).length ?? 0
const blocking = computed(() => level('block'))
const toConfirm = computed(() => level('confirm'))
// Changed counts the entities changed or removed and the principals changed.
const changed = computed(() => (preview.value
  ? preview.value.entities.length - preview.value.added.length + preview.value.removed.length +
    preview.value.roles.length + preview.value.users.length
  : 0))
const empty = computed(() => !!preview.value && !preview.value.entities.length && !preview.value.removed.length &&
  !preview.value.roles.length && !preview.value.users.length)
const fresh = computed(() => !!current.value && previewer.current(current.value, ws.contextKey))
/** Why the outcome cannot be taken now, or null. */
const blocked = computed(() => {
  const p = preview.value
  if (!p || state.loading) return t('plan.waiting')
  if (!fresh.value) return t('plan.stalePreview')
  if (!p.validation.ok) return t('plan.invalid')
  if (empty.value) return t('plan.noChanges')
  if (blocking.value) return t('plan.blocked', { n: blocking.value })
  if (toConfirm.value && !acknowledged.value) return t('plan.needAck', { n: toConfirm.value })
  return null
})

// The plan is written into the workspace on the server, as an agent's
// plan_apply writes it, what was edited here written first.
const applying = ref(false)
async function apply() {
  if (blocked.value) {
    if (!fresh.value && !state.loading) evaluate()
    return
  }
  const p = preview.value!
  const plan = current.value!
  applying.value = true
  try {
    await workspace.flush()
    const confirmed = p.report.exceptions.filter((x) => x.level === 'confirm').map((x) => x.id)
    await run(ApplyPlanMutation, { input: { plan, planId: p.planId, review: !!props.review, confirmed } }, true)
    await workspace.refresh()
    emit('applied', { added: [...p.added], changed: changed.value, plan })
    show.value = false
  } catch (e) {
    if (e instanceof ApiError && e.code === 'CONFLICT') {
      message.warning(t('plan.previewAgain'))
      evaluate()
    } else message.error(e instanceof Error ? e.message : String(e))
  } finally {
    applying.value = false
  }
}

/** Leaves the preview for the entity an exception asks to fix. */
function locate(entity: string) {
  show.value = false
  void router.push({ name: 'entity', params: { id: entity } })
}

const drilling = ref(false)
const drillChange = ref<PlanReportPartsFragment['changes'][number] | null>(null)
function drill(c: PlanReportPartsFragment['changes'][number]) {
  drillChange.value = c
  drilling.value = true
}
</script>

<template>
  <n-modal v-model:show="show" preset="card" class="dialog" style="width: 820px" :title="title ?? t('plan.previewTitle')">
    <n-spin :show="state.loading">
      <div class="stack body" style="gap: 12px">
        <n-alert v-if="state.error" type="error" :show-icon="false">{{ state.error }}</n-alert>
        <n-alert v-if="excluded.length" type="default" :show-icon="false">
          {{ t('plan.excludedList', { n: excluded.length }) }}
          <span class="excluded">
            <n-tag v-for="r in excluded.slice(0, 20)" :key="r" size="small" closable class="mono" @close="include(r)">{{ r }}</n-tag>
            <n-text v-if="excluded.length > 20" depth="3">{{ t('plan.more', { n: excluded.length - 20 }) }}</n-text>
          </span>
          <n-button text size="tiny" type="primary" @click="include(null)">{{ t('plan.includeAll') }}</n-button>
        </n-alert>
        <template v-if="preview">
          <plan-report :report="preview.report" editable @exclude="exclude" @rename="rename" @drill="drill"
            @locate="locate" />
          <n-alert v-if="preview.removed.length" type="warning" :show-icon="false">
            {{ t('plan.removedList', { n: preview.removed.length }) }}
            <span class="excluded">
              <n-tag v-for="id in preview.removed.slice(0, 20)" :key="id" size="small" class="mono">{{ id }}</n-tag>
              <n-text v-if="preview.removed.length > 20" depth="3">{{ t('plan.more', { n: preview.removed.length - 20 }) }}</n-text>
            </span>
          </n-alert>
          <n-alert v-if="!preview.validation.ok" type="error" :title="t('plan.invalid')">{{ preview.validation.error }}</n-alert>
          <template v-else-if="!empty">
            <n-alert v-if="preview.validation.warnings.length" type="warning" :show-icon="false">
              <div v-for="w in preview.validation.warnings" :key="w">{{ w }}</div>
            </n-alert>
            <n-text depth="3">
              {{ preview.validation.restartRequired.length
                ? t('plan.effectRestart', { list: preview.validation.restartRequired.join(', ') })
                : t('plan.effectHot') }}
            </n-text>
          </template>
        </template>
      </div>
    </n-spin>
    <template #footer>
      <div class="footer">
        <n-checkbox v-if="toConfirm && !blocking" v-model:checked="acknowledged">{{ t('plan.ack', { n: toConfirm }) }}</n-checkbox>
        <n-text v-else-if="blocked && preview && !state.loading" type="warning" class="reason">{{ blocked }}</n-text>
        <span v-else />
        <n-space>
          <n-button @click="show = false">{{ t('common.cancel') }}</n-button>
          <n-button type="primary" :disabled="Boolean(blocked) && (fresh || state.loading)" :loading="applying"
            @click="apply">
            {{ !fresh && preview && !state.loading ? t('plan.reevaluate')
              : preview ? t('plan.apply', { added: preview.added.length, changed }) : t('plan.applyPlain') }}
          </n-button>
        </n-space>
      </div>
    </template>
  </n-modal>
  <change-drill-down v-model:show="drilling" :change="drillChange" :proposal="preview" />
</template>

<style scoped>
.stack { display: flex; flex-direction: column; }
.body { min-height: 120px; max-height: 65vh; overflow: auto; }
.excluded { display: inline-flex; flex-wrap: wrap; gap: 4px; margin: 0 8px; }
.footer { display: flex; justify-content: space-between; align-items: center; gap: 12px; }
.reason { font-size: 13px; }
</style>
