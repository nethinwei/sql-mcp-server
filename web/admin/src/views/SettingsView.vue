<script setup lang="ts">
import { computed, watch } from 'vue'
import { onBeforeRouteLeave } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { NAlert, NButton, NCard, NCollapse, NCollapseItem, NSpace, NTag, NText, useDialog } from 'naive-ui'
import JsonEditor from '@/components/JsonEditor.vue'
import { useWorkspace } from '@/stores/workspace'
import { useSettingsEdits } from '@/stores/settingsEdits'
import { can } from '@/api/session'
import { useConfigSchema } from '@/lib/configSchema'
import { at, restartsBelow } from '@/lib/jsonSchema'

// Every top-level section other than datasources, entities, roles and users,
// edited as JSON against the configuration schema (completion, hover help,
// inline checks). Titles, help, defaults and restart markers all come from
// that schema, generated from the Go configuration structs. Valid JSON goes
// into the workspace as it is typed, like edits on other pages; the server
// validates the whole draft again at review time.
const { t } = useI18n()
const ws = useWorkspace()
const edits = useSettingsEdits()
const dialog = useDialog()
const editable = computed(() => can('admin:write'))

const sections = computed(() => [...new Set([...Object.keys(ws.settings), ...edits.pending])].sort())
watch(() => ws.settings, () => edits.sync(), { immediate: true, deep: true })

const { schema } = useConfigSchema()
const section = (key: string) => (schema.value ? at(schema.value, schema.value, [key]) : undefined)
const label = (key: string) => section(key)?.title ?? ''
/** 'all' when any change needs a restart, 'some' when only some fields do. */
const restart = (key: string) => {
  const s = section(key)
  if (!schema.value || !s) return null
  return s['x-restart'] ? 'all' : restartsBelow(schema.value, s) ? 'some' : null
}
const baseSettings = computed(() =>
  (JSON.parse(ws.baseline || '{}') as { settings?: Record<string, unknown> }).settings ?? {})
/** The section differs from the revision the workspace is based on. */
const changed = (key: string) =>
  key in edits.errors || JSON.stringify(ws.settings[key]) !== JSON.stringify(baseSettings.value[key])

// Text that is not valid JSON is not in the workspace yet: say so on leaving.
onBeforeRouteLeave(() => {
  if (!edits.pending.length) return true
  return new Promise<boolean>((resolve) => {
    dialog.warning({
      title: t('settings.leaveTitle', { count: edits.pending.length }),
      content: t('settings.leaveBody', { list: edits.pending.join(', ') }),
      positiveText: t('settings.stay'),
      negativeText: t('settings.leaveAnyway'),
      onPositiveClick: () => resolve(false),
      onNegativeClick: () => resolve(true),
      onClose: () => resolve(false),
      onMaskClick: () => resolve(false),
    })
  })
})
</script>

<template>
  <n-space vertical :size="16">
    <n-alert type="default" :show-icon="false">{{ t('settings.intro') }}</n-alert>
    <n-alert v-if="edits.pending.length" type="warning" :show-icon="false">
      {{ t('settings.pending', { list: edits.pending.join(', ') }) }}
    </n-alert>
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
              <n-tag v-if="key in edits.errors" size="tiny" :bordered="false" type="error">{{ t('settings.invalid') }}</n-tag>
              <n-tag v-else-if="changed(key)" size="tiny" :bordered="false" type="info">{{ t('settings.modified') }}</n-tag>
            </n-space>
          </template>
          <json-editor :model-value="edits.texts[key] ?? ''" :schema="schema" :path="[key]" :readonly="!editable"
            @update:model-value="(v: string) => edits.edit(key, v)" />
          <n-text v-if="edits.errors[key]" type="error" class="small">
            {{ t('settings.jsonError', { error: edits.errors[key] }) }}
          </n-text>
          <div class="footer">
            <n-text depth="3" class="small">{{ t('settings.editorHint') }}</n-text>
            <n-space v-if="editable" :wrap="false">
              <n-button size="small" quaternary :disabled="key in edits.errors" @click="edits.format(key)">
                {{ t('settings.format') }}
              </n-button>
              <n-button size="small" :disabled="!changed(key)" @click="edits.revert(key)">{{ t('common.restore') }}</n-button>
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
