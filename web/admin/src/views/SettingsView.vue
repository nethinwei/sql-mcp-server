<script setup lang="ts">
import { computed, watch } from 'vue'
import { onBeforeRouteLeave } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { NAlert, NButton, NCard, NCollapse, NCollapseItem, NSpace, NTag, NText, useDialog, useMessage } from 'naive-ui'
import JsonEditor from '@/components/JsonEditor.vue'
import { useWorkspace } from '@/stores/workspace'
import { useSettingsEdits } from '@/stores/settingsEdits'
import { can } from '@/api/session'
import { useConfigSchema } from '@/lib/configSchema'
import { at, restartsBelow } from '@/lib/jsonSchema'

// Every top-level section other than datasources, entities, roles and users,
// edited as JSON against the configuration schema (completion, hover help,
// inline checks). Titles, help, defaults and restart markers all come from
// that schema, generated from the Go configuration structs. The server
// validates the whole draft again at review time. Each section has its own
// buffer (kept when leaving the page) and is applied to the workspace
// explicitly, alone or with the others.
const { t } = useI18n()
const ws = useWorkspace()
const edits = useSettingsEdits()
const message = useMessage()
const dialog = useDialog()
const editable = computed(() => can('admin:write'))

const sections = computed(() => Object.keys(ws.settings).sort())
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
const changed = (key: string) => edits.pending.includes(key)
/** The workspace changed this section (e.g. a rebase) while it was being edited. */
const outdated = (key: string) => changed(key) && edits.based[key] !== JSON.stringify(ws.settings[key], null, 2)

function format(key: string) {
  const r = edits.parse(key)
  if (r.ok) edits.texts[key] = JSON.stringify(r.value, null, 2)
}
function apply(keys: string[]) {
  if (!edits.apply(keys)) {
    message.error(t('settings.applyFailed'))
    return false
  }
  message.success(keys.length === 1 ? t('settings.applied', { key: keys[0] }) : t('settings.appliedAll', { count: keys.length }))
  return true
}

// Unapplied text survives navigation, but is easy to forget: offer to apply it.
onBeforeRouteLeave(() => {
  if (!edits.pending.length) return true
  return new Promise<boolean>((resolve) => {
    dialog.warning({
      title: t('settings.leaveTitle', { count: edits.pending.length }),
      content: t('settings.leaveBody', { list: edits.pending.join(', ') }),
      positiveText: t('settings.applyAndLeave'),
      negativeText: t('settings.leaveKeep'),
      onPositiveClick: () => resolve(apply(edits.pending)),
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
      <div class="pending">
        <span>{{ t('settings.pending', { list: edits.pending.join(', ') }) }}</span>
        <n-button v-if="editable" size="small" type="primary" @click="apply(edits.pending)">
          {{ t('settings.applyAll', { count: edits.pending.length }) }}
        </n-button>
      </div>
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
              <n-tag v-if="changed(key)" size="tiny" :bordered="false" type="info">{{ t('settings.modified') }}</n-tag>
            </n-space>
          </template>
          <json-editor v-model="edits.texts[key]" :schema="schema" :path="[key]" :readonly="!editable" />
          <n-text v-if="edits.errors[key]" type="error" class="small">
            {{ t('settings.jsonError', { error: edits.errors[key] }) }}
          </n-text>
          <n-text v-if="outdated(key)" type="warning" class="small block">{{ t('settings.outdated') }}</n-text>
          <div class="footer">
            <n-text depth="3" class="small">{{ t('settings.editorHint') }}</n-text>
            <n-space v-if="editable" :wrap="false">
              <n-button size="small" quaternary @click="format(key)">{{ t('settings.format') }}</n-button>
              <n-button size="small" :disabled="!changed(key)" @click="edits.restore(key)">{{ t('common.restore') }}</n-button>
              <n-button size="small" type="primary" :disabled="!changed(key)" @click="apply([key])">
                {{ t('settings.applyToWorkspace') }}
              </n-button>
            </n-space>
          </div>
        </n-collapse-item>
      </n-collapse>
    </n-card>
  </n-space>
</template>

<style scoped>
.label, .small { font-size: 12px; }
.block { display: block; margin-top: 4px; }
.pending { display: flex; justify-content: space-between; align-items: center; gap: 12px; flex-wrap: wrap; }
.footer { display: flex; justify-content: space-between; align-items: center; gap: 12px; flex-wrap: wrap; margin-top: 8px; }
</style>
