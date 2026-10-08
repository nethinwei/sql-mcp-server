<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { NAlert, NButton, NCard, NCollapse, NCollapseItem, NSpace, NTag, NText, useMessage } from 'naive-ui'
import JsonEditor from '@/components/JsonEditor.vue'
import { useWorkspace } from '@/stores/workspace'
import { can } from '@/api/session'
import { useConfigSchema } from '@/lib/configSchema'
import { at, restartsBelow } from '@/lib/jsonSchema'

// Every top-level section other than datasources, entities, roles and users,
// edited as JSON against the configuration schema (completion, hover help,
// inline checks). Titles, help, defaults and restart markers all come from
// that schema, generated from the Go configuration structs. The server
// validates the whole draft again at review time.
const { t } = useI18n()
const ws = useWorkspace()
const message = useMessage()
const editable = computed(() => can('admin:write'))

const sections = computed(() => Object.keys(ws.settings).sort())
const texts = ref<Record<string, string>>({})
const errors = ref<Record<string, string>>({})
const pretty = (key: string) => JSON.stringify(ws.settings[key], null, 2)

function reset() {
  texts.value = Object.fromEntries(sections.value.map((k) => [k, pretty(k)]))
  errors.value = {}
}
watch(() => ws.settings, reset, { immediate: true, deep: true })

const { schema } = useConfigSchema()
const section = (key: string) => (schema.value ? at(schema.value, schema.value, [key]) : undefined)
const label = (key: string) => section(key)?.title ?? ''
/** 'all' when any change needs a restart, 'some' when only some fields do. */
const restart = (key: string) => {
  const s = section(key)
  if (!schema.value || !s) return null
  return s['x-restart'] ? 'all' : restartsBelow(schema.value, s) ? 'some' : null
}
const changed = (key: string) => texts.value[key] !== pretty(key)

function parse(key: string): { ok: true; value: unknown } | { ok: false } {
  try {
    const value: unknown = JSON.parse(texts.value[key])
    delete errors.value[key]
    return { ok: true, value }
  } catch (e) {
    errors.value[key] = e instanceof Error ? e.message : String(e)
    return { ok: false }
  }
}
function format(key: string) {
  const r = parse(key)
  if (r.ok) texts.value[key] = JSON.stringify(r.value, null, 2)
}
function apply(key: string) {
  const r = parse(key)
  if (!r.ok) return
  ws.setSettings({ ...ws.settings, [key]: r.value })
  message.success(t('settings.applied', { key }))
}
</script>

<template>
  <n-space vertical :size="16">
    <n-alert type="default" :show-icon="false">{{ t('settings.intro') }}</n-alert>
    <n-card size="small">
      <n-collapse>
        <n-collapse-item v-for="key in sections" :key="key" :name="key">
          <template #header>
            <n-space align="center" :size="8">
              <span class="mono">{{ key }}</span>
              <n-text depth="3" class="label">{{ label(key) }}</n-text>
              <n-tag v-if="restart(key)" size="tiny" :bordered="false" type="warning">
                {{ restart(key) === 'all' ? t('settings.restart') : t('settings.restartSome') }}
              </n-tag>
              <n-tag v-if="changed(key)" size="tiny" :bordered="false" type="info">{{ t('settings.modified') }}</n-tag>
            </n-space>
          </template>
          <json-editor v-model="texts[key]" :schema="schema" :path="[key]" :readonly="!editable" />
          <n-text v-if="errors[key]" type="error" class="small">{{ t('settings.jsonError', { error: errors[key] }) }}</n-text>
          <div class="footer">
            <n-text depth="3" class="small">{{ t('settings.editorHint') }}</n-text>
            <n-space v-if="editable" :wrap="false">
              <n-button size="small" quaternary @click="format(key)">{{ t('settings.format') }}</n-button>
              <n-button size="small" :disabled="!changed(key)" @click="texts[key] = pretty(key)">{{ t('common.restore') }}</n-button>
              <n-button size="small" type="primary" :disabled="!changed(key)" @click="apply(key)">{{ t('settings.applyToWorkspace') }}</n-button>
            </n-space>
          </div>
        </n-collapse-item>
      </n-collapse>
    </n-card>
  </n-space>
</template>

<style scoped>
.label, .small { font-size: 12px; }
.footer { display: flex; justify-content: space-between; align-items: center; gap: 12px; flex-wrap: wrap; margin-top: 8px; }
</style>
