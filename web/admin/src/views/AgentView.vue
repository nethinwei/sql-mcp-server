<script setup lang="ts">
import { computed, h, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  NAlert, NButton, NCard, NDataTable, NForm, NFormItem, NInput, NInputNumber, NPopconfirm, NSpace, NTag, NText,
  useMessage, type DataTableColumns,
} from 'naive-ui'
import { run } from '@/api/client'
import { CreateMcpTokenMutation, McpTokensQuery, RevokeMcpTokenMutation } from '@/api/ops'
import type { McpTokensQuery as Tokens } from '@/gql/graphql'
import { session } from '@/api/session'
import McpConnect from '@/components/McpConnect.vue'
import SecretOnce from '@/components/SecretOnce.vue'

// The configuration MCP endpoint lets an agent work for the signed-in
// administrator: search the catalog, scan, preview plans and save them as
// drafts. It never publishes; drafts are reviewed and published here. Each
// client gets a token of its own, named, with an expiry, revoked on its own;
// a token acts with the administrator's permissions and is shown once.
type Token = Tokens['mcpTokens'][number]

const { t } = useI18n()
const message = useMessage()
const tokens = ref<Token[]>([])
const error = ref('')
const name = ref('')
const days = ref<number | null>(90)
const created = ref<{ token: string; name: string } | null>(null)

async function load() {
  try {
    tokens.value = (await run(McpTokensQuery, {})).mcpTokens
    error.value = ''
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  }
}
onMounted(load)

async function create() {
  try {
    const { createMcpToken } = await run(CreateMcpTokenMutation, { input: { name: name.value.trim(), expiresInDays: days.value ?? 0 } }, true)
    created.value = { token: createMcpToken.token, name: createMcpToken.info.name }
    name.value = ''
    await load()
  } catch (e) {
    message.error(e instanceof Error ? e.message : String(e))
  }
}

async function revoke(id: string) {
  await run(RevokeMcpTokenMutation, { id }, true)
  message.success(t('agent.revoked'))
  await load()
}

const when = (at?: string | null) => (at ? new Date(at).toLocaleString() : '—')
const expired = (tk: Token) => !!tk.expiresAt && new Date(tk.expiresAt).getTime() <= Date.now()
const columns = computed<DataTableColumns<Token>>(() => [
  { title: t('agent.client'), key: 'name', render: (tk) => h('span', { class: 'strong' }, tk.name) },
  { title: t('agent.created'), key: 'createdAt', render: (tk) => when(tk.createdAt) },
  {
    title: t('agent.expires'), key: 'expiresAt',
    render: (tk) => (expired(tk) ? h(NTag, { size: 'small', type: 'error', bordered: false }, () => t('agent.expired'))
      : tk.expiresAt ? when(tk.expiresAt) : t('agent.never')),
  },
  { title: t('agent.lastUsed'), key: 'lastUsedAt', render: (tk) => (tk.lastUsedAt ? when(tk.lastUsedAt) : t('agent.unused')) },
  {
    title: '', key: 'revoke', width: 90,
    render: (tk) => h(NPopconfirm, { onPositiveClick: () => revoke(tk.id) }, {
      trigger: () => h(NButton, { size: 'tiny' }, () => t('agent.revoke')),
      default: () => t('agent.revokeConfirm', { name: tk.name }),
    }),
  },
])
</script>

<template>
  <n-card size="small" :title="t('agent.title')">
    <div class="stack" style="gap: 12px">
      <n-alert type="default" :show-icon="false">{{ t('agent.intro') }}</n-alert>
      <n-alert v-if="error" type="error" :show-icon="false">{{ error }}</n-alert>
      <n-text>{{ t('agent.tokensOf', { user: session.username, n: tokens.length }) }}</n-text>
      <n-data-table v-if="tokens.length" size="small" :columns="columns" :data="tokens" :row-key="(tk: Token) => tk.id" />
      <n-form inline label-placement="left" size="small" @submit.prevent="create">
        <n-form-item :label="t('agent.client')">
          <n-input v-model:value="name" maxlength="64" :placeholder="t('agent.clientHint')" />
        </n-form-item>
        <n-form-item :label="t('agent.expiresIn')">
          <n-input-number v-model:value="days" :min="0" :max="3650" :placeholder="t('agent.never')" class="days" />
        </n-form-item>
        <n-form-item>
          <n-button type="primary" attr-type="submit" :disabled="!name.trim()">{{ t('agent.generate') }}</n-button>
        </n-form-item>
      </n-form>
      <n-text depth="3" class="hint">{{ t('agent.expiresHint') }}</n-text>
      <template v-if="created">
        <n-space align="center"><n-text strong>{{ t('agent.createdFor', { name: created.name }) }}</n-text></n-space>
        <secret-once :secret="created.token" />
        <mcp-connect :token="created.token" admin />
      </template>
      <n-text depth="3" class="hint">{{ t('agent.tools') }}</n-text>
    </div>
  </n-card>
</template>

<style scoped>
.stack { display: flex; flex-direction: column; }
.hint { font-size: 12px; }
.days { width: 140px; }
:deep(.strong) { font-weight: 600; }
</style>
