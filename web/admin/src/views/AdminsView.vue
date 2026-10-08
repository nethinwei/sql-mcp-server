<script setup lang="ts">
import { computed, h, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useQuery } from '@urql/vue'
import {
  NButton, NCard, NCheckbox, NCheckboxGroup, NDataTable, NForm, NFormItem, NInput, NModal, NSpace, NSwitch,
  NTag, NText, useMessage, type DataTableColumns,
} from 'naive-ui'
import { run } from '@/api/client'
import {
  AdminAccountsQuery, CreateAdminAccountMutation, SetAdminPasswordMutation, UpdateAdminAccountMutation,
} from '@/api/ops'
import { session, signedOut } from '@/api/session'
import { useRouter } from 'vue-router'
import type { AdminAccountsQuery as AccountsResult } from '@/gql/graphql'

type Account = AccountsResult['adminAccounts'][number]

const { t } = useI18n()
const router = useRouter()
const message = useMessage()
const accounts = useQuery({ query: AdminAccountsQuery, variables: {} })
const refresh = () => accounts.executeQuery({ requestPolicy: 'network-only' })

const permissions = ['admin:read', 'admin:write', 'admin:publish', 'admin:accounts', 'admin:*']
const permissionOptions = computed(() =>
  permissions.map((p) => ({ value: p, label: t(`admins.perm.${p}`), hint: t(`admins.permHint.${p}`) })))

async function act(fn: () => Promise<unknown>, done: string) {
  try {
    await fn()
    message.success(done)
    refresh()
  } catch (e) {
    message.error(e instanceof Error ? e.message : String(e))
  }
}

const columns = computed<DataTableColumns<Account>>(() => [
  {
    title: t('admins.account'), key: 'username',
    render: (a) => h(NSpace, { size: 6, align: 'center' }, () => [
      h('span', { class: 'mono' }, a.username),
      a.username === session.username ? h(NTag, { size: 'tiny', bordered: false, type: 'info' }, () => t('admins.me')) : null,
    ]),
  },
  {
    title: t('admins.permissions'), key: 'permissions',
    render: (a) => h(NSpace, { size: 4 }, () => a.permissions.map((p) =>
      h(NTag, { size: 'small', bordered: false }, () => permissionOptions.value.find((o) => o.value === p)?.label ?? p))),
  },
  {
    title: t('admins.enabled'), key: 'disabled', width: 80,
    render: (a) => h(NSwitch, {
      size: 'small', value: !a.disabled,
      onUpdateValue: (v: boolean) => act(() => run(UpdateAdminAccountMutation, { input: { username: a.username, disabled: !v } }, true),
        v ? t('admins.enabledMsg') : t('admins.disabledMsg')),
    }),
  },
  {
    title: '', key: 'ops', width: 220,
    render: (a) => h(NSpace, { size: 4, wrap: false }, () => [
      h(NButton, { size: 'tiny', onClick: () => openPermissions(a) }, () => t('admins.editPermissions')),
      h(NButton, { size: 'tiny', onClick: () => openPassword(a.username) }, () => t('admins.resetPassword')),
    ]),
  },
])

const createShow = ref(false)
const form = ref({ username: '', password: '', permissions: ['admin:read'] as string[] })
function create() {
  void act(() => run(CreateAdminAccountMutation, { input: form.value }, true), t('admins.created', { name: form.value.username }))
    .then(() => { createShow.value = false; form.value = { username: '', password: '', permissions: ['admin:read'] } })
}

const permTarget = ref<Account | null>(null)
const permDraft = ref<string[]>([])
function openPermissions(a: Account) {
  permTarget.value = a
  permDraft.value = [...a.permissions]
}
function savePermissions() {
  const a = permTarget.value
  if (!a) return
  void act(() => run(UpdateAdminAccountMutation, { input: { username: a.username, permissions: permDraft.value } }, true),
    t('admins.permissionsUpdated')).then(() => { permTarget.value = null })
}

const pwTarget = ref<string | null>(null)
const pw = ref('')
function openPassword(username: string) {
  pwTarget.value = username
  pw.value = ''
}
async function savePassword() {
  const u = pwTarget.value
  if (!u) return
  if (u !== session.username) {
    await act(() => run(SetAdminPasswordMutation, { input: { username: u, password: pw.value } }, true),
      t('admins.passwordUpdated'))
    pwTarget.value = null
    return
  }
  // A password change ends every session of the account, this one included.
  try {
    await run(SetAdminPasswordMutation, { input: { username: u, password: pw.value } }, true)
    message.success(t('admins.ownPasswordUpdated'))
    signedOut()
    void router.push({ name: 'login' })
  } catch (e) {
    message.error(e instanceof Error ? e.message : String(e))
  }
}
</script>

<template>
  <n-space vertical :size="12">
    <n-space justify="space-between" align="center">
      <n-text depth="3">{{ t('admins.intro') }}</n-text>
      <n-button type="primary" size="small" @click="createShow = true">{{ t('admins.create') }}</n-button>
    </n-space>
    <n-card size="small">
      <n-data-table :columns="columns" :data="accounts.data.value?.adminAccounts ?? []" size="small"
        :row-key="(a: Account) => a.username" :loading="accounts.fetching.value" />
    </n-card>

    <n-modal v-model:show="createShow" preset="card" :title="t('admins.create')" class="dialog">
      <n-form>
        <n-form-item :label="t('admins.username')"><n-input v-model:value="form.username" :placeholder="t('admins.usernamePlaceholder')" /></n-form-item>
        <n-form-item :label="t('admins.initialPassword')"><n-input v-model:value="form.password" type="password" show-password-on="click" :placeholder="t('admins.passwordPlaceholder')" /></n-form-item>
        <n-form-item :label="t('admins.permissions')">
          <n-checkbox-group v-model:value="form.permissions">
            <n-space vertical :size="4">
              <n-checkbox v-for="o in permissionOptions" :key="o.value" :value="o.value">
                {{ o.label }} <n-text depth="3" style="font-size: 12px">{{ o.hint }}</n-text>
              </n-checkbox>
            </n-space>
          </n-checkbox-group>
        </n-form-item>
      </n-form>
      <template #footer>
        <n-space justify="end">
          <n-button @click="createShow = false">{{ t('common.cancel') }}</n-button>
          <n-button type="primary" :disabled="!form.username || form.password.length < 12" @click="create">{{ t('common.create') }}</n-button>
        </n-space>
      </template>
    </n-modal>

    <n-modal :show="permTarget !== null" preset="card" :title="t('admins.editTitle', { name: permTarget?.username })" class="dialog"
      @update:show="(v: boolean) => !v && (permTarget = null)">
      <n-checkbox-group v-model:value="permDraft">
        <n-space vertical :size="4">
          <n-checkbox v-for="o in permissionOptions" :key="o.value" :value="o.value">
            {{ o.label }} <n-text depth="3" style="font-size: 12px">{{ o.hint }}</n-text>
          </n-checkbox>
        </n-space>
      </n-checkbox-group>
      <template #footer>
        <n-space justify="end">
          <n-button @click="permTarget = null">{{ t('common.cancel') }}</n-button>
          <n-button type="primary" @click="savePermissions">{{ t('common.save') }}</n-button>
        </n-space>
      </template>
    </n-modal>

    <n-modal :show="pwTarget !== null" preset="card" :title="t('admins.passwordTitle', { name: pwTarget })" class="dialog"
      @update:show="(v: boolean) => !v && (pwTarget = null)">
      <n-input v-model:value="pw" type="password" show-password-on="click" :placeholder="t('admins.passwordPlaceholder')" />
      <template #footer>
        <n-space justify="end">
          <n-button @click="pwTarget = null">{{ t('common.cancel') }}</n-button>
          <n-button type="primary" :disabled="pw.length < 12" @click="savePassword">{{ t('common.save') }}</n-button>
        </n-space>
      </template>
    </n-modal>
  </n-space>
</template>
