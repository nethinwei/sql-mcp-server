<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { NButton, NEmpty, NText } from 'naive-ui'
import FilterGroup from './FilterGroup.vue'
import { isCondition, type Filter, type Group } from '@/lib/filter'

// Edits a row filter visually. Internally the filter is always a group; a
// single condition or an empty group is emitted in its simplest form.
const props = defineProps<{ modelValue: Filter | null | undefined; fields: string[]; subjectKeys?: string[]; disabled?: boolean }>()
const emit = defineEmits<{ 'update:modelValue': [value: Filter | undefined] }>()
const { t } = useI18n()

function toGroup(f: Filter | null | undefined): Group {
  if (!f) return { and: [] }
  if (isCondition(f)) return { and: [f] }
  return JSON.parse(JSON.stringify(f)) as Group
}

const group = ref<Group>(toGroup(props.modelValue))
watch(() => props.modelValue, (v) => {
  if (JSON.stringify(v ?? null) !== JSON.stringify(simplify(group.value) ?? null)) group.value = toGroup(v)
})

function simplify(g: Group): Filter | undefined {
  const items = (g.and ?? g.or ?? []).map((i) => (isCondition(i) ? i : simplify(i))).filter(Boolean) as Filter[]
  if (items.length === 0) return undefined
  if (items.length === 1) return items[0]
  return g.or ? { or: items } : { and: items }
}

function onChange(g: Group) {
  group.value = g
  emit('update:modelValue', simplify(g))
}

const empty = computed(() => (group.value.and ?? group.value.or ?? []).length === 0)
</script>

<template>
  <div>
    <n-empty v-if="empty" size="small" :description="t('filter.emptyHint')">
      <template v-if="!disabled" #extra>
        <n-button size="small" dashed @click="onChange({ and: [{ op: 'eq', field: fields[0] ?? '', value: '' }] })">
          {{ t('filter.addCondition') }}
        </n-button>
      </template>
    </n-empty>
    <template v-else>
      <filter-group :group="group" :fields="fields" :subject-keys="subjectKeys ?? []" :disabled="disabled" root
        @update="onChange" />
      <n-text depth="3" class="hint">{{ t('filter.valueHint') }}</n-text>
    </template>
  </div>
</template>

<style scoped>
.hint { display: block; margin-top: 8px; font-size: 12px; }
</style>
