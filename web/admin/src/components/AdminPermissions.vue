<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { NCheckbox, NCheckboxGroup, NSpace, NText } from 'naive-ui'

// An administrator's permissions. admin:* grants every other one, so while
// it is chosen the others show as granted and cannot be changed, and it is
// kept alone; unchosen, the others are as they were before.
const model = defineModel<string[]>({ required: true })
const { t } = useI18n()

const all = 'admin:*'
const each = ['admin:read', 'admin:write', 'admin:publish', 'admin:accounts']
const options = computed(() =>
  [...each, all].map((p) => ({ value: p, label: t(`admins.perm.${p}`), hint: t(`admins.permHint.${p}`) })))

const everything = computed(() => model.value.includes(all))
const shown = computed(() => (everything.value ? [...each, all] : model.value))
// What was chosen besides admin:*, to restore when it is unchosen.
let others: string[] = model.value.filter((p) => p !== all)

function update(next: (string | number)[]) {
  const chosen = next.map(String)
  if (chosen.includes(all) && !everything.value) {
    others = model.value
    model.value = [all]
  } else if (!chosen.includes(all) && everything.value) {
    model.value = others
  } else {
    model.value = chosen
  }
}
</script>

<template>
  <n-checkbox-group :value="shown" @update:value="update">
    <n-space vertical :size="4">
      <n-checkbox v-for="o in options" :key="o.value" :value="o.value" :disabled="everything && o.value !== all">
        {{ o.label }} <n-text depth="3" style="font-size: 12px">{{ o.hint }}</n-text>
      </n-checkbox>
    </n-space>
  </n-checkbox-group>
</template>
