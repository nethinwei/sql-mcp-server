<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { NButton, NFormItem, NInput, NTabPane, NTabs, NText, useMessage } from 'naive-ui'
import { useWorkspace } from '@/stores/workspace'
import { serviceBase } from '@/api/base'

// Ready-to-paste client configuration for a user's token, one tab per MCP
// client, as MCP services usually offer it: the remote endpoint and the
// token as an Authorization: Bearer header.
const props = defineProps<{ token: string }>()
const { t } = useI18n()
const message = useMessage()

// The MCP endpoint as clients reach it: server.publicURL when configured
// (behind a gateway or proxy the console's own address may not be it),
// otherwise next to the console. It can be changed here for one copy.
const ws = useWorkspace()
const router = useRouter()
const configured = computed(() => {
  const server = ws.settings.server as { publicURL?: string } | undefined
  return server?.publicURL || ''
})
const url = ref('')
watch(configured, (v) => { url.value = v || `${window.location.origin}${serviceBase}mcp` }, { immediate: true })

const name = 'sql-mcp-server'
const header = computed(() => `Bearer ${props.token}`)
const json = (v: unknown) => JSON.stringify(v, null, 2)

const snippets = computed(() => [
  {
    key: 'claude', label: 'Claude Code', file: t('connect.claudeFile'),
    text: `claude mcp add --transport http ${name} ${url.value} \\\n  --header "Authorization: ${header.value}"`,
  },
  {
    key: 'cursor', label: 'Cursor', file: '~/.cursor/mcp.json',
    text: json({ mcpServers: { [name]: { url: url.value, headers: { Authorization: header.value } } } }),
  },
  {
    key: 'vscode', label: 'VS Code', file: t('connect.vscodeFile'),
    text: json({ servers: { [name]: { type: 'http', url: url.value, headers: { Authorization: header.value } } } }),
  },
  {
    key: 'json', label: t('connect.other'), file: t('connect.otherFile'),
    text: json({ mcpServers: { [name]: { type: 'http', url: url.value, headers: { Authorization: header.value } } } }),
  },
  {
    key: 'curl', label: t('connect.test'), file: t('connect.testFile'),
    text: [
      `curl -sS ${url.value} \\`,
      `  -H "Authorization: ${header.value}" \\`,
      `  -H "Content-Type: application/json" -H "Accept: application/json, text/event-stream" \\`,
      `  -d '${JSON.stringify({
        jsonrpc: '2.0', id: 1, method: 'initialize',
        params: { protocolVersion: '2025-06-18', capabilities: {}, clientInfo: { name: 'curl', version: '1' } },
      })}'`,
    ].join('\n'),
  },
])

async function copy(text: string) {
  try {
    await navigator.clipboard.writeText(text)
    message.success(t('common.copied'))
  } catch {
    message.error(t('connect.copyFailed'))
  }
}
</script>

<template>
  <div class="connect">
    <n-form-item :label="t('connect.url')" :show-feedback="false" class="url">
      <n-input v-model:value="url" class="mono" />
    </n-form-item>
    <n-text v-if="configured" depth="3" class="hint">{{ t('connect.urlConfigured') }}</n-text>
    <n-text v-else type="warning" class="hint">
      {{ t('connect.urlGuessed') }}
      <n-button text type="primary" size="tiny" @click="router.push({ name: 'settings', query: { section: 'server' } })">{{ t('connect.urlConfigure') }}</n-button>
    </n-text>
    <n-tabs type="line" size="small" animated>
      <n-tab-pane v-for="s in snippets" :key="s.key" :name="s.key" :tab="s.label">
        <div class="where">
          <n-text depth="3">{{ s.file }}</n-text>
          <n-button size="small" type="primary" @click="copy(s.text)">{{ t('common.copy') }}</n-button>
        </div>
        <pre class="code mono">{{ s.text }}</pre>
      </n-tab-pane>
    </n-tabs>
    <n-text depth="3" class="hint">{{ t('connect.secretHint') }}</n-text>
  </div>
</template>

<style scoped>
.connect { display: flex; flex-direction: column; gap: 8px; margin-top: 12px; }
.url { max-width: 640px; }
.hint { font-size: 12px; }
.where { display: flex; align-items: center; justify-content: space-between; gap: 8px; margin-bottom: 6px; }
.code {
  margin: 0; padding: 12px; border-radius: 6px; background: rgba(128, 128, 128, .12);
  white-space: pre-wrap; word-break: break-all; font-size: 12px; line-height: 1.6;
}
</style>
