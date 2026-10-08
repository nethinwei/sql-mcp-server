<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { NAutoComplete, NButton, NIcon, NRadioButton, NRadioGroup, NSelect, NText } from 'naive-ui'
import { CloseOutline } from '@vicons/ionicons5'
import { isCondition, ops, type Condition, type Filter, type Group, type Op } from '@/lib/filter'

const props = defineProps<{ group: Group; fields: string[]; subjectKeys: string[]; root?: boolean; disabled?: boolean }>()
const emit = defineEmits<{ update: [value: Group]; remove: [] }>()
const { t } = useI18n()

const mode = computed(() => (props.group.or ? 'or' : 'and'))
const items = computed<Filter[]>(() => props.group.and ?? props.group.or ?? [])
const fieldOptions = computed(() => props.fields.map((f) => ({ label: f, value: f })))
const opOptions = computed(() => ops.map((o) => ({ label: t(`filter.ops.${o.value}`), value: o.value })))

function update(next: Filter[], m = mode.value) {
  emit('update', m === 'or' ? { or: next } : { and: next })
}
function setItem(i: number, f: Filter) {
  const next = [...items.value]
  next[i] = f
  update(next)
}
function removeItem(i: number) {
  update(items.value.filter((_, j) => j !== i))
}

/** Renders a value for editing; strings that look like numbers get quotes. */
function formatValue(c: Condition): string {
  const v = c.value
  if (Array.isArray(v)) return v.map((x) => formatScalar(x)).join(', ')
  return formatScalar(v)
}
function formatScalar(v: unknown): string {
  if (v === undefined || v === null) return ''
  if (typeof v === 'string' && /^-?\d+(\.\d+)?$|^(true|false)$/.test(v)) return `"${v}"`
  return String(v)
}
function parseScalar(s: string): unknown {
  const v = s.trim()
  if (/^".*"$/.test(v)) return v.slice(1, -1)
  if (/^-?\d+(\.\d+)?$/.test(v)) return Number(v)
  if (v === 'true' || v === 'false') return v === 'true'
  return v
}
function setValue(c: Condition, i: number, raw: string) {
  const arity = ops.find((o) => o.value === c.op)?.arity
  const value = arity === 'list' ? raw.split(',').map(parseScalar).filter((x) => x !== '') : parseScalar(raw)
  setItem(i, { ...c, value })
}
function setOp(c: Condition, i: number, op: Op) {
  const arity = ops.find((o) => o.value === op)?.arity
  const next: Condition = { op, field: c.field }
  if (arity !== 0) next.value = arity === 'list' && !Array.isArray(c.value) ? (c.value === undefined ? [] : [c.value]) : c.value
  setItem(i, next)
}
const arityOf = (c: Condition) => ops.find((o) => o.value === c.op)?.arity

const valueOptions = computed(() =>
  props.subjectKeys.map((k) => ({ label: `${t('filter.currentUser')}.${k}`, value: '$' + `{subject.${k}}` })))
</script>

<template>
  <div class="group" :class="{ nested: !root }">
    <div class="head">
      <n-radio-group :value="mode" size="small" :disabled="disabled" @update:value="(m: string) => update(items, m)">
        <n-radio-button value="and">{{ t('filter.matchAll') }}</n-radio-button>
        <n-radio-button value="or">{{ t('filter.matchAny') }}</n-radio-button>
      </n-radio-group>
      <n-button v-if="!root && !disabled" size="tiny" quaternary @click="emit('remove')">{{ t('filter.removeGroup') }}</n-button>
    </div>
    <div v-for="(item, i) in items" :key="i" class="row">
      <template v-if="isCondition(item)">
        <n-select size="small" class="field" filterable tag :value="item.field" :options="fieldOptions"
          :placeholder="t('filter.field')" :disabled="disabled" @update:value="(f: string) => setItem(i, { ...item, field: f })" />
        <n-select size="small" class="op" :value="item.op" :options="opOptions" :disabled="disabled"
          @update:value="(o: Op) => setOp(item, i, o)" />
        <n-auto-complete v-if="arityOf(item) !== 0" size="small" class="value" :value="formatValue(item)"
          :options="valueOptions" :disabled="disabled"
          :placeholder="arityOf(item) === 'list' ? t('filter.listValue') : t('filter.value')"
          @update:value="(v: string) => setValue(item, i, v)" />
        <n-text v-else depth="3" class="value novalue">{{ t('filter.noValue') }}</n-text>
        <n-button v-if="!disabled" size="small" quaternary circle :aria-label="t('common.remove')" @click="removeItem(i)">
          <template #icon><n-icon><close-outline /></n-icon></template>
        </n-button>
      </template>
      <filter-group v-else :group="item as Group" :fields="fields" :subject-keys="subjectKeys" :disabled="disabled"
        @update="(g: Group) => setItem(i, g)" @remove="removeItem(i)" />
    </div>
    <div v-if="!disabled" class="actions">
      <n-button size="small" dashed @click="update([...items, { op: 'eq', field: fields[0] ?? '', value: '' }])">
        {{ t('filter.addCondition') }}
      </n-button>
      <n-button size="small" dashed @click="update([...items, { or: [] }])">{{ t('filter.addGroup') }}</n-button>
    </div>
  </div>
</template>

<style scoped>
.group { display: flex; flex-direction: column; gap: 8px; min-width: 0; }
.group.nested { flex: 1; border-left: 3px solid rgba(47,111,237,.35); padding: 4px 0 4px 12px; }
.head, .actions { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; }
.row { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; }
.field { width: 170px; }
.op { width: 130px; }
.value { width: 220px; }
.novalue { font-size: 12px; }
</style>
