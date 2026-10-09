<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { NButton, NCheckbox, NCheckboxGroup, NForm, NFormItem, NModal, NSelect, NSpace, NSwitch, NText } from 'naive-ui'
import { useWorkspace } from '@/stores/workspace'
import { nameError } from '@/lib/names'
import type { Plan } from '@/lib/plan'

// Importing selected tables is a plan over them: import, optionally grant to
// roles and hide or mask sensitive columns. The plan is previewed before it
// enters the workspace.
const props = defineProps<{ base: Plan; count: number }>()
const show = defineModel<boolean>('show', { required: true })
const emit = defineEmits<{ plan: [plan: Plan] }>()
const { t } = useI18n()
const ws = useWorkspace()

const roles = ref<string[]>([])
const actions = ref<string[]>(['read'])
const maskSensitive = ref(true)
const grantable = ['read', 'aggregate', 'create', 'update', 'delete']
const roleOptions = computed(() => ws.roles.map((r) => ({ label: r.name, value: r.name })))
const roleError = computed(() => roles.value
  .filter((r) => !ws.role(r))
  .map((r) => nameError(r, ws.roles.map((x) => x.name)))
  .find(Boolean) ?? null)

watch(show, (open) => {
  if (!open) return
  roles.value = []
  actions.value = ['read']
})

// Columns whose names suggest personal data are masked by the matching rule.
const sensitive = [
  { columns: ['*phone*', '*mobile*', '*tel*'], mask: 'phone' },
  { columns: ['*email*', '*mail'], mask: 'email' },
  { columns: ['*id_card*', '*idcard*', '*id_no*', '*passport*'], mask: 'idcard' },
  { columns: ['*password*', '*secret*', '*token*'], mask: 'secret' },
]

function next() {
  emit('plan', {
    ...props.base, import: true,
    grants: actions.value.length ? roles.value.map((r) => ({ principal: `role:${r}`, actions: [...actions.value] })) : [],
    fields: maskSensitive.value ? sensitive.map((s) => ({ ...s })) : [],
  })
  show.value = false
}
</script>

<template>
  <n-modal v-model:show="show" preset="card" class="dialog" :title="t('catalog.importTitle', { n: count })">
    <n-form label-placement="top">
      <n-form-item :label="t('datasources.grantRoles')" :feedback="roleError ?? ''" :validation-status="roleError ? 'error' : undefined">
        <n-select v-model:value="roles" multiple filterable tag :options="roleOptions" :placeholder="t('datasources.grantRolesPlaceholder')" />
      </n-form-item>
      <n-form-item v-if="roles.length" :label="t('datasources.grantActions')">
        <n-checkbox-group v-model:value="actions">
          <n-space><n-checkbox v-for="a in grantable" :key="a" :value="a" :label="t(`grants.actions.${a.toUpperCase()}`)" /></n-space>
        </n-checkbox-group>
      </n-form-item>
      <n-form-item :label="t('catalog.maskSensitive')" :show-feedback="false">
        <n-space vertical :size="4">
          <n-switch v-model:value="maskSensitive" size="small" />
          <n-text depth="3" class="hint">{{ t('catalog.maskSensitiveHint') }}</n-text>
        </n-space>
      </n-form-item>
    </n-form>
    <n-text v-if="!roles.length" depth="3" class="hint">{{ t('datasources.grantHint') }}</n-text>
    <template #footer>
      <n-space justify="end">
        <n-button @click="show = false">{{ t('common.cancel') }}</n-button>
        <n-button type="primary" :disabled="Boolean(roleError) || (roles.length > 0 && !actions.length)" @click="next">
          {{ t('catalog.previewImport') }}
        </n-button>
      </n-space>
    </template>
  </n-modal>
</template>

<style scoped>
.hint { font-size: 12px; }
</style>
