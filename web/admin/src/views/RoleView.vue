<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import {
  NButton, NCard, NEmpty, NForm, NFormItem, NInput, NModal, NPageHeader, NSelect, NSpace, NText, useDialog,
} from 'naive-ui'
import GrantMatrix from '@/components/GrantMatrix.vue'
import VisibilityTable from '@/components/VisibilityTable.vue'
import { simulationRoute } from '@/lib/simulation'
import { useWorkspace } from '@/stores/workspace'
import { nameError } from '@/lib/names'
import { can } from '@/api/session'
import type { Action, GrantInput } from '@/gql/graphql'

const props = defineProps<{ name: string }>()
const { t } = useI18n()
const ws = useWorkspace()
const router = useRouter()
// A field scope picked in the preview opens it as a single simulation.
const simulatePick = (who: string, entity: string, action: Action, fields: string[]) =>
  router.push(simulationRoute({ who, source: 'workspace', entity, action, fields }))
const dialog = useDialog()
const editable = computed(() => can('admin:write'))

const role = computed(() => ws.role(props.name))
const members = computed(() => ws.users.filter((u) => (u.roles ?? []).includes(props.name)).map((u) => u.name))
// Preview the role on its own, or through one of its members (whose subject
// attributes feed tenant boundaries and row scopes).
const previewAs = ref(`role:${props.name}`)
watch(() => props.name, (n) => { previewAs.value = `role:${n}` })
const previewOptions = computed(() => [
  { label: t('visibility.asRole'), value: `role:${props.name}` },
  ...members.value.map((m) => ({ label: m, value: m })),
])
const subjectKeys = computed(() => [...new Set(ws.users.flatMap((u) => Object.keys((u.subject as object) ?? {})))])

const renaming = ref(false)
const newName = ref('')
const renameError = computed(() => nameError(newName.value, ws.roles.map((r) => r.name).filter((n) => n !== props.name)))
function startRename() {
  newName.value = props.name
  renaming.value = true
}
function rename() {
  if (!role.value || renameError.value) return
  ws.upsertRole({ ...role.value, name: newName.value }, props.name)
  renaming.value = false
  void router.replace({ name: 'role', params: { name: newName.value } })
}

function setGrants(grants: GrantInput[]) {
  if (role.value) role.value.grants = grants
}

function remove() {
  dialog.warning({
    title: t('roles.deleteTitle', { name: props.name }),
    content: members.value.length ? t('roles.deleteMembers', { list: members.value.join(', ') }) : t('roles.deleteNoMembers'),
    positiveText: t('common.delete'),
    negativeText: t('common.cancel'),
    onPositiveClick: () => {
      ws.removeRole(props.name)
      void router.push({ name: 'roles' })
    },
  })
}
</script>

<template>
  <n-empty v-if="!role" :description="t('roles.notFound', { name })">
    <template #extra><n-button @click="router.push({ name: 'roles' })">{{ t('roles.backToList') }}</n-button></template>
  </n-empty>
  <n-space v-else vertical :size="16">
    <n-page-header @back="router.push({ name: 'roles' })">
      <template #title><span class="mono">{{ role.name }}</span></template>
      <template #extra>
        <n-space v-if="editable">
          <n-button size="small" @click="startRename">{{ t('common.rename') }}</n-button>
          <n-button size="small" type="error" ghost @click="remove">{{ t('roles.delete') }}</n-button>
        </n-space>
      </template>
    </n-page-header>

    <n-card size="small">
      <n-form label-placement="top">
        <n-form-item :label="t('roles.description')" :show-feedback="false">
          <n-input v-model:value="role.description" :disabled="!editable" :placeholder="t('roles.descriptionPlaceholder')" />
        </n-form-item>
        <n-form-item :label="t('roles.membersLabel')" :show-feedback="false" class="members">
          <n-select :value="members" multiple filterable :disabled="!editable" :placeholder="t('roles.membersPlaceholder')"
            :options="ws.users.map((u) => ({ label: u.name, value: u.name }))"
            @update:value="(v: string[]) => ws.setRoleMembers(name, v)" />
        </n-form-item>
      </n-form>
    </n-card>

    <n-card size="small" :title="t('roles.permissions')">
      <template #header-extra><n-text depth="3" class="hint">{{ t('roles.permissionsHint') }}</n-text></template>
      <grant-matrix :model-value="role.grants ?? []" :entities="ws.entities" :subject-keys="subjectKeys"
        :readonly="!editable" @update:model-value="setGrants" />
    </n-card>

    <n-card size="small" :title="t('visibility.title')">
      <template #header-extra>
        <n-space align="center" :size="8" :wrap="false">
          <n-text depth="3" class="hint">{{ t('visibility.previewAs') }}</n-text>
          <n-select v-model:value="previewAs" size="small" :options="previewOptions" class="preview-as" />
        </n-space>
      </template>
      <visibility-table :who="previewAs" :draft="ws.draft" live compact
        @pick="(e, a, f) => simulatePick(previewAs, e, a, f)" />
    </n-card>

    <n-modal v-model:show="renaming" preset="card" :title="t('roles.renameTitle')" class="dialog">
      <n-form-item :label="t('roles.name')" :feedback="renameError ?? ''" :validation-status="renameError ? 'error' : undefined">
        <n-input v-model:value="newName" @keyup.enter="rename" />
      </n-form-item>
      <template #footer>
        <n-space justify="end">
          <n-button @click="renaming = false">{{ t('common.cancel') }}</n-button>
          <n-button type="primary" :disabled="Boolean(renameError)" @click="rename">{{ t('common.confirm') }}</n-button>
        </n-space>
      </template>
    </n-modal>
  </n-space>
</template>

<style scoped>
.members { margin-top: 12px; }
.hint { font-size: 12px; }
.preview-as { width: 220px; }
</style>
