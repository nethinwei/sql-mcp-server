<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { NCheckbox, NDivider, NFormItem, NGi, NGrid, NInput, NInputNumber, NText, NTooltip } from 'naive-ui'
import { useConfigSchema } from '@/lib/configSchema'
import { at } from '@/lib/jsonSchema'
import { useWorkspace } from '@/stores/workspace'

// A role's or a user's limits (roles.<name>.limits, users.<name>.limits):
// each tightens the global value, which the placeholder shows; an empty
// field keeps it. The labels and help come from the configuration schema.
type Limits = Record<string, unknown>
const props = defineProps<{ kind: 'role' | 'user'; name: string; disabled?: boolean }>()
const limits = defineModel<Limits | null | undefined>()
const { t } = useI18n()
const ws = useWorkspace()
const { schema } = useConfigSchema()

type Field = { key: string; type: 'int' | 'number' | 'duration' | 'score' | 'bool'; global?: string[] }
const fields: Field[] = [
  { key: 'maxRows', type: 'int', global: ['cost', 'maxRows'] },
  { key: 'maxBytes', type: 'int', global: ['cost', 'maxBytes'] },
  { key: 'queryTimeout', type: 'duration', global: ['cost', 'queryTimeout'] },
  { key: 'softScore', type: 'score', global: ['cost', 'softScore'] },
  { key: 'hardScore', type: 'score', global: ['cost', 'hardScore'] },
  { key: 'rps', type: 'number' },
  { key: 'maxInflight', type: 'int' },
  { key: 'rejectFullScan', type: 'bool', global: ['cost', 'rejectFullScan'] },
]
const budgetFields: Field[] = [
  { key: 'maxConcurrent', type: 'int' },
  { key: 'maxExecution', type: 'duration' },
  { key: 'maxEstimatedScannedRows', type: 'int' },
  { key: 'maxReturnedRows', type: 'int' },
  { key: 'maxReturnedBytes', type: 'int' },
  { key: 'maxSessionCost', type: 'int' },
]

const settings = computed(() => ws.settings as Record<string, Record<string, unknown> | undefined>)
/** What applies when the field is left empty: the written global value, else its default. */
function globalOf(path: string[]): unknown {
  const written = settings.value[path[0]]?.[path[1]]
  if (written !== undefined) return written
  return schema.value ? at(schema.value, schema.value, path)?.default : undefined
}
/** The legacy budget.roles / budget.users entry a limits.budget field overrides. */
const legacyBudget = computed(() => {
  const budget = settings.value.budget as Record<string, Record<string, Record<string, unknown>>> | undefined
  return budget?.[props.kind === 'role' ? 'roles' : 'users']?.[props.name] ?? {}
})
function placeholder(f: Field, budget = false): string {
  const v = budget ? legacyBudget.value[f.key] : f.global && globalOf(f.global)
  if (v === undefined || v === null || v === 0 || v === '' || v === '0s') return t('limits.unlimited')
  return t('limits.globalValue', { value: String(v) })
}
function help(key: string, budget = false): string {
  const path = [props.kind === 'role' ? 'roles' : 'users', props.name, 'limits', ...(budget ? ['budget'] : []), key]
  return (schema.value && at(schema.value, schema.value, path)?.description) || ''
}

const value = (key: string, budget = false) =>
  (budget ? (limits.value?.budget as Limits | undefined) : limits.value)?.[key]
const durationRe = /^(\d+(\.\d+)?(ns|us|µs|ms|s|m|h))+$/
const invalid = (f: Field, budget = false) => {
  const v = value(f.key, budget)
  return f.type === 'duration' && typeof v === 'string' && v !== '' && !durationRe.test(v)
}

/** Sets one field; an empty one is removed, and limits without any become unset. */
function set(key: string, v: unknown, budget = false) {
  const next: Limits = { ...(limits.value ?? {}) }
  const target: Limits = budget ? { ...((next.budget as Limits | undefined) ?? {}) } : next
  if (v === null || v === undefined || v === '' || v === false || v === 0) delete target[key]
  else target[key] = v
  if (budget) {
    if (Object.keys(target).length) next.budget = target
    else delete next.budget
  }
  limits.value = Object.keys(next).length ? next : null
}
</script>

<template>
  <div>
    <n-text depth="3" class="hint">{{ t('limits.hint') }}</n-text>
    <n-grid :cols="4" :x-gap="12" responsive="screen" item-responsive class="grid">
      <n-gi v-for="f in fields" :key="f.key" span="4 s:2 m:1">
        <n-form-item :show-feedback="invalid(f)" :validation-status="invalid(f) ? 'error' : undefined"
          :feedback="t('limits.duration')">
          <template #label>
            <n-tooltip :disabled="!help(f.key)"><template #trigger><span>{{ t(`limits.fields.${f.key}`) }}</span></template>
              {{ help(f.key) }}</n-tooltip>
          </template>
          <n-checkbox v-if="f.type === 'bool'" :checked="!!value(f.key)" :disabled="disabled"
            @update:checked="(v: boolean) => set(f.key, v)">{{ t('limits.on') }}</n-checkbox>
          <n-input v-else-if="f.type === 'duration'" :value="(value(f.key) as string) ?? ''" :disabled="disabled"
            :placeholder="placeholder(f)" clearable @update:value="(v: string) => set(f.key, v.trim())" />
          <n-input-number v-else :value="(value(f.key) as number) ?? null" :disabled="disabled" clearable
            :min="0" :max="f.type === 'score' ? 100 : undefined" :precision="f.type === 'number' ? undefined : 0"
            :placeholder="placeholder(f)" class="full" @update:value="(v: number | null) => set(f.key, v)" />
        </n-form-item>
      </n-gi>
    </n-grid>
    <n-divider title-placement="left" class="divider">{{ t('limits.budget') }}</n-divider>
    <n-text depth="3" class="hint">{{ t('limits.budgetHint') }}</n-text>
    <n-grid :cols="3" :x-gap="12" responsive="screen" item-responsive class="grid">
      <n-gi v-for="f in budgetFields" :key="f.key" span="3 s:1">
        <n-form-item :show-feedback="invalid(f, true)" :validation-status="invalid(f, true) ? 'error' : undefined"
          :feedback="t('limits.duration')">
          <template #label>
            <n-tooltip :disabled="!help(f.key, true)">
              <template #trigger><span>{{ t(`limits.budgetFields.${f.key}`) }}</span></template>{{ help(f.key, true) }}
            </n-tooltip>
          </template>
          <n-input v-if="f.type === 'duration'" :value="(value(f.key, true) as string) ?? ''" :disabled="disabled"
            :placeholder="placeholder(f, true)" clearable @update:value="(v: string) => set(f.key, v.trim(), true)" />
          <n-input-number v-else :value="(value(f.key, true) as number) ?? null" :disabled="disabled" clearable :min="0"
            :precision="0" :placeholder="placeholder(f, true)" class="full"
            @update:value="(v: number | null) => set(f.key, v, true)" />
        </n-form-item>
      </n-gi>
    </n-grid>
  </div>
</template>

<style scoped>
.hint { font-size: 12px; display: block; margin-bottom: 8px; }
.grid { margin-top: 4px; }
.full { width: 100%; }
.divider { margin: 4px 0 8px; font-size: 13px; }
</style>
