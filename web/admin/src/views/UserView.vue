<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import {
  NAlert, NButton, NCard, NEmpty, NForm, NFormItem, NIcon, NInput, NPageHeader, NSelect, NSpace, NSwitch,
  NTag, NText, useDialog, useMessage,
} from 'naive-ui'
import { CloseOutline } from '@vicons/ionicons5'
import GrantMatrix from '@/components/GrantMatrix.vue'
import SecretOnce from '@/components/SecretOnce.vue'
import VisibilityTable from '@/components/VisibilityTable.vue'
import { simulationRoute } from '@/lib/simulation'
import { useWorkspace } from '@/stores/workspace'
import { run } from '@/api/client'
import { GenerateUserTokenMutation } from '@/api/ops'
import { can } from '@/api/session'
import type { Action, GrantInput } from '@/gql/graphql'

const props = defineProps<{ name: string }>()
const { t } = useI18n()
const ws = useWorkspace()
const route = useRoute()
const router = useRouter()
// A field scope picked in the preview opens it as a single simulation.
const simulatePick = (who: string, entity: string, action: Action, fields: string[]) =>
  router.push(simulationRoute({ who, source: 'workspace', entity, action, fields }))
const dialog = useDialog()
const message = useMessage()
const editable = computed(() => can('admin:write'))
const user = computed(() => ws.user(props.name))
const newToken = ref('')

const tokenState = computed(() => {
  const u = user.value
  if (!u) return 'none'
  if (u.tokenHash === '') return 'revoked'
  if (u.tokenHash) return 'new'
  return ws.baseTokens[u.name] ? 'kept' : 'none'
})
const tokenTag = computed(() => ({
  kept: { type: 'success' as const, label: t('users.tokenKept') },
  new: { type: 'info' as const, label: t('users.tokenNew') },
  revoked: { type: 'error' as const, label: t('users.tokenRevoked') },
  none: { type: 'warning' as const, label: t('users.tokenNone') },
})[tokenState.value])

async function generate() {
  try {
    const { generateUserToken } = await run(GenerateUserTokenMutation, {}, true)
    if (user.value) user.value.tokenHash = generateUserToken.tokenHash
    newToken.value = generateUserToken.token
  } catch (e) {
    message.error(e instanceof Error ? e.message : String(e))
  }
}
function revoke() {
  dialog.warning({
    title: t('users.revokeTitle'),
    content: t('users.revokeBody'),
    positiveText: t('users.revoke'),
    negativeText: t('common.cancel'),
    onPositiveClick: () => {
      if (user.value) user.value.tokenHash = ''
      newToken.value = ''
    },
  })
}

// Subject attributes edited as key/value rows.
const subjectRows = computed(() => Object.entries((user.value?.subject as Record<string, unknown>) ?? {}))
function setSubject(rows: [string, unknown][]) {
  if (!user.value) return
  const obj = Object.fromEntries(rows)
  user.value.subject = Object.keys(obj).length ? obj : undefined
}
function parseValue(v: string): unknown {
  return /^-?\d+(\.\d+)?$/.test(v.trim()) ? Number(v) : v
}

function setGrants(grants: GrantInput[]) {
  if (user.value) user.value.grants = grants
}

function remove() {
  dialog.warning({
    title: t('users.deleteTitle', { name: props.name }),
    content: t('users.deleteBody'),
    positiveText: t('common.delete'),
    negativeText: t('common.cancel'),
    onPositiveClick: () => {
      ws.removeUser(props.name)
      void router.push({ name: 'users' })
    },
  })
}

onMounted(() => { if (route.query.token && editable.value && tokenState.value === 'none') void generate() })
</script>

<template>
  <n-empty v-if="!user" :description="t('users.notFound', { name })">
    <template #extra><n-button @click="router.push({ name: 'users' })">{{ t('users.backToList') }}</n-button></template>
  </n-empty>
  <n-space v-else vertical :size="16">
    <n-page-header @back="router.push({ name: 'users' })">
      <template #title><span class="mono">{{ user.name }}</span></template>
      <template #extra>
        <n-space>
          <n-button size="small" @click="router.push(simulationRoute({ who: name, source: 'workspace' }))">
            {{ t('users.viewAccess') }}
          </n-button>
          <n-button v-if="editable" size="small" type="error" ghost @click="remove">{{ t('users.delete') }}</n-button>
        </n-space>
      </template>
    </n-page-header>

    <n-card size="small">
      <n-form label-placement="top">
        <n-form-item :label="t('users.description')" :show-feedback="false">
          <n-input v-model:value="user.description" :disabled="!editable" :placeholder="t('users.descriptionPlaceholder')" />
        </n-form-item>
        <n-form-item :label="t('users.roles')" :show-feedback="false" class="gap">
          <n-select v-model:value="user.roles" multiple filterable :disabled="!editable"
            :options="ws.roles.map((r) => ({ label: r.name, value: r.name }))" />
        </n-form-item>
        <div class="switch-row">
          <n-switch :value="!user.disabled" :disabled="!editable" @update:value="(v: boolean) => (user!.disabled = !v)" />
          <span>{{ t('users.enabled') }}</span>
        </div>
      </n-form>
    </n-card>

    <n-card size="small" :title="t('users.credentials')">
      <n-space vertical>
        <n-space align="center">
          <n-tag :type="tokenTag.type" :bordered="false">{{ tokenTag.label }}</n-tag>
          <n-button v-if="editable" size="small" @click="generate">
            {{ tokenState === 'none' ? t('users.generate') : t('users.regenerate') }}
          </n-button>
          <n-button v-if="editable && (tokenState === 'kept' || tokenState === 'new')" size="small" type="error" quaternary @click="revoke">
            {{ t('users.revoke') }}
          </n-button>
        </n-space>
        <secret-once v-if="newToken" :secret="newToken" :note="t('users.tokenOnce')" />
      </n-space>
    </n-card>

    <n-card size="small" :title="t('users.subject')">
      <template #header-extra><n-text depth="3" class="hint">{{ t('users.subjectHint') }}</n-text></template>
      <div class="attrs">
        <div v-for="([k, v], i) in subjectRows" :key="i" class="attr">
          <n-input :value="k" size="small" class="attr-key" :placeholder="t('users.attrName')" :disabled="!editable"
            @update:value="(nk: string) => setSubject(subjectRows.map((r, j) => (j === i ? [nk, r[1]] : r)))" />
          <n-input :value="String(v)" size="small" class="attr-value" :placeholder="t('users.attrValue')" :disabled="!editable"
            @update:value="(nv: string) => setSubject(subjectRows.map((r, j) => (j === i ? [r[0], parseValue(nv)] : r)))" />
          <n-button v-if="editable" size="small" quaternary circle :aria-label="t('common.remove')"
            @click="setSubject(subjectRows.filter((_, j) => j !== i))">
            <template #icon><n-icon><close-outline /></n-icon></template>
          </n-button>
        </div>
        <n-button v-if="editable" size="small" dashed class="add-attr" @click="setSubject([...subjectRows, ['', '']])">
          {{ t('users.addAttr') }}
        </n-button>
      </div>
    </n-card>

    <n-card size="small" :title="t('users.grants')">
      <template #header-extra><n-text depth="3" class="hint">{{ t('users.grantsHint') }}</n-text></template>
      <n-alert v-if="!(user.grants ?? []).length" type="default" :show-icon="false" class="from-roles">
        {{ t('users.grantsFromRoles', { roles: (user.roles ?? []).join(', ') || t('users.noRoles') }) }}
      </n-alert>
      <grant-matrix :model-value="user.grants ?? []" :entities="ws.entities"
        :subject-keys="Object.keys((user.subject as object) ?? {})" :readonly="!editable" @update:model-value="setGrants" />
    </n-card>

    <n-card size="small" :title="t('visibility.userTitle')">
      <visibility-table :who="user.name" :draft="ws.draft" live compact
        @pick="(e, a, f) => simulatePick(name, e, a, f)" />
    </n-card>
  </n-space>
</template>

<style scoped>
.gap { margin-top: 12px; }
.switch-row { display: flex; align-items: center; gap: 10px; margin-top: 14px; }
.hint { font-size: 12px; }
.attrs { display: flex; flex-direction: column; gap: 8px; }
.attr { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.attr-key { width: 180px; }
.attr-value { width: 240px; }
.add-attr { align-self: flex-start; }
.from-roles { margin-bottom: 10px; }
</style>
