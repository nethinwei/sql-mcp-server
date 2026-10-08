<script setup lang="ts">
import { computed, h, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import {
  NButton, NDataTable, NEmpty, NForm, NFormItem, NInput, NModal, NSelect, NSpace, NTag, NText,
  type DataTableColumns,
} from 'naive-ui'
import { useWorkspace } from '@/stores/workspace'
import { nameError } from '@/lib/names'
import { can } from '@/api/session'
import type { UserInput } from '@/gql/graphql'

const { t } = useI18n()
const ws = useWorkspace()
const route = useRoute()
const router = useRouter()
const search = ref('')
const creating = ref(false)
const name = ref('')
const roles = ref<string[]>([])
const error = computed(() => nameError(name.value, ws.users.map((u) => u.name)))

const rows = computed(() => {
  const q = search.value.trim().toLowerCase()
  return ws.users.filter((u) => !q || u.name.includes(q) || (u.description ?? '').toLowerCase().includes(q))
})

const columns = computed<DataTableColumns<UserInput>>(() => [
  { title: t('users.user'), key: 'name', minWidth: 140, render: (u) => h('span', { class: 'mono strong' }, u.name) },
  { title: t('users.description'), key: 'description', ellipsis: { tooltip: true } },
  {
    title: t('users.roles'), key: 'roles', minWidth: 180,
    render: (u) => (u.roles ?? []).length
      ? h(NSpace, { size: 4 }, () => (u.roles ?? []).map((r) => h(NTag, { size: 'small', type: 'info', bordered: false }, () => r)))
      : h(NText, { depth: 3 }, () => '—'),
  },
  { title: t('users.directGrants'), key: 'grants', width: 80, render: (u) => (u.grants ?? []).length || '—' },
  {
    title: t('users.state'), key: 'state', width: 130,
    render: (u) => u.disabled
      ? h(NTag, { size: 'small', bordered: false }, () => t('users.disabled'))
      : ws.hasToken(u)
        ? h(NTag, { size: 'small', type: 'success', bordered: false }, () => t('users.active'))
        : h(NTag, { size: 'small', type: 'warning', bordered: false }, () => t('users.noToken')),
  },
])

function create() {
  if (error.value) return
  ws.upsertUser({ name: name.value, roles: roles.value, grants: [] })
  creating.value = false
  void router.push({ name: 'user', params: { name: name.value }, query: { token: '1' } })
}
onMounted(() => { if (route.query.create) creating.value = true })
</script>

<template>
  <n-space vertical :size="12">
    <div class="toolbar">
      <n-input v-model:value="search" :placeholder="t('users.search')" clearable size="small" class="search" />
      <n-button v-if="can('admin:write')" type="primary" size="small" @click="creating = true">{{ t('users.create') }}</n-button>
    </div>
    <n-empty v-if="!ws.users.length" :description="t('users.empty')" />
    <n-data-table v-else :columns="columns" :data="rows" :row-key="(u: UserInput) => u.name" size="small"
      :row-props="(u: UserInput) => ({ class: 'clickable', onClick: () => router.push({ name: 'user', params: { name: u.name } }) })" />

    <n-modal v-model:show="creating" preset="card" :title="t('users.create')" class="dialog">
      <n-form @submit.prevent="create">
        <n-form-item :label="t('users.name')" :feedback="name ? error ?? '' : ''" :validation-status="name && error ? 'error' : undefined">
          <n-input v-model:value="name" :placeholder="t('users.namePlaceholder')" autofocus />
        </n-form-item>
        <n-form-item :label="t('users.roles')">
          <n-select v-model:value="roles" multiple :options="ws.roles.map((r) => ({ label: r.name, value: r.name }))" />
        </n-form-item>
      </n-form>
      <template #footer>
        <n-space justify="end">
          <n-button @click="creating = false">{{ t('common.cancel') }}</n-button>
          <n-button type="primary" :disabled="Boolean(error)" @click="create">{{ t('common.create') }}</n-button>
        </n-space>
      </template>
    </n-modal>
  </n-space>
</template>

<style scoped>
.toolbar { display: flex; justify-content: space-between; gap: 12px; flex-wrap: wrap; }
.search { width: 240px; }
:deep(.clickable) { cursor: pointer; }
:deep(.strong) { font-weight: 600; }
</style>
