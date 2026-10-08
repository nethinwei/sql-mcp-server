<script setup lang="ts">
import { computed, h, ref, watchEffect } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import {
  NAlert, NButton, NCard, NDataTable, NDescriptions, NDescriptionsItem, NEmpty, NForm, NFormItem, NInput,
  NModal, NPageHeader, NSelect, NSpace, NSwitch, NTag, NText, useDialog, useMessage, type DataTableColumns,
} from 'naive-ui'
import FilterBuilder from '@/components/FilterBuilder.vue'
import { useWorkspace } from '@/stores/workspace'
import { describeFilter, type Filter } from '@/lib/filter'
import { useConfigSchema } from '@/lib/configSchema'
import { can } from '@/api/session'
import { loadTableComments, tableComments } from '@/lib/tableComments'
import type { FieldInput, GrantInput, RelationshipInput } from '@/gql/graphql'

const props = defineProps<{ name: string }>()
const { t, te } = useI18n()
const { options } = useConfigSchema()
const ws = useWorkspace()
const router = useRouter()
const dialog = useDialog()
const message = useMessage()
const editable = computed(() => can('admin:write'))

const entity = computed(() => ws.entity(props.name))
// Empty descriptions fall back to the database comments at runtime; they are
// shown as placeholders so only a written description overrides them.
const comments = computed(() => (entity.value ? tableComments(entity.value) : null))
watchEffect(() => { if (entity.value) void loadTableComments([entity.value]) })
const fieldNames = computed(() => (entity.value?.fields ?? []).map((f) => f.name))
const subjectKeys = computed(() => [...new Set(ws.users.flatMap((u) => Object.keys((u.subject as object) ?? {})))])
const kindLabel = computed(() => t(`entity.kinds.${entity.value?.kind === 'procedure' ? 'procedure' : entity.value?.kind === 'view' ? 'view' : 'table'}`))

// Built-in mask rules come from the configuration schema; extensions may
// register more, so other names can be typed in.
const maskOptions = computed(() => [
  { label: t('entity.masks.none'), value: '' },
  ...options(['entities', 0, 'fields', 0, 'mask'])
    .map((m) => ({ label: te(`entity.masks.${m}`) ? t(`entity.masks.${m}`) : m, value: m })),
])
const cardinalityOptions = computed(() =>
  options(['entities', 0, 'relationships', 0, 'cardinality']).map((v) => ({ label: v, value: v })))

const fieldColumns = computed<DataTableColumns<FieldInput>>(() => [
  { title: t('entity.field'), key: 'name', width: 170, render: (f) => h('span', { class: 'mono' }, f.name) },
  {
    title: t('entity.fieldDescription'), key: 'description', minWidth: 220,
    render: (f) => h(NInput, {
      size: 'small', value: f.description ?? '', disabled: !editable.value, placeholder: comments.value?.columns.get(f.name) || t('entity.fieldDescriptionHint'),
      onUpdateValue: (v: string) => { f.description = v },
    }),
  },
  {
    title: t('entity.alias'), key: 'alias', width: 150,
    render: (f) => h(NInput, {
      size: 'small', value: f.alias ?? '', placeholder: f.name, disabled: !editable.value, onUpdateValue: (v: string) => { f.alias = v },
    }),
  },
  {
    title: t('entity.mask'), key: 'mask', width: 130,
    render: (f) => h(NSelect, {
      size: 'small', value: f.mask ?? '', options: maskOptions.value, disabled: !editable.value, filterable: true, tag: true,
      onUpdateValue: (v: string) => { f.mask = v },
    }),
  },
  {
    title: t('entity.hidden'), key: 'exclude', width: 80, align: 'center',
    render: (f) => h(NSwitch, {
      size: 'small', value: Boolean(f.exclude), disabled: !editable.value,
      onUpdateValue: (v: boolean) => { f.exclude = v },
    }),
  },
])

function summarize(grants: GrantInput[] | null | undefined) {
  return (grants ?? []).filter((g) => g.entity === props.name).map((g) => t('entity.grantSummary', {
    actions: g.actions.map((a) => t(`grants.actions.${a}`)).join('/'),
    fields: g.fieldsRestricted ? t('entity.nFields', { count: g.readFields?.length ?? 0 }, g.readFields?.length ?? 0) : t('entity.allFields'),
    rows: describeFilter(g.rows as Filter),
  }))
}
const access = computed(() => [
  ...ws.roles.flatMap((r) => summarize(r.grants).map((d) => ({ kind: 'role' as const, who: r.name, d }))),
  ...ws.users.flatMap((u) => summarize(u.grants).map((d) => ({ kind: 'user' as const, who: u.name, d }))),
])

const grantRole = ref<string | null>(null)
function quickGrant() {
  const role = grantRole.value ? ws.role(grantRole.value) : undefined
  if (!role) return
  role.grants = [...(role.grants ?? []), { entity: props.name, actions: ['READ'] }]
  message.success(t('entity.granted', { role: role.name, entity: props.name }))
  grantRole.value = null
}

// joinOn maps local fields to target fields.
const joinText = (r: RelationshipInput) =>
  Object.entries((r.joinOn ?? {}) as Record<string, string>).map(([l, f]) => `${l} = ${r.target}.${f}`).join(', ')
const newRel = ref<RelationshipInput>({ name: '', target: '', cardinality: 'belongs-to', joinOn: {} })
const newRelLocal = ref<string | null>(null)
const newRelRemote = ref<string | null>(null)
function addRelationship() {
  const e = entity.value
  if (!e || !newRelLocal.value || !newRelRemote.value) return
  e.relationships = [...(e.relationships ?? []), {
    ...newRel.value, name: newRel.value.name || newRel.value.target,
    joinOn: { [newRelLocal.value]: newRelRemote.value },
  }]
  newRel.value = { name: '', target: '', cardinality: 'belongs-to', joinOn: {} }
  newRelLocal.value = null
  newRelRemote.value = null
}
const targetFields = computed(() => ws.entity(newRel.value.target)?.fields?.map((f) => ({ label: f.name, value: f.name })) ?? [])

// Renaming moves every reference (see workspace.upsertEntity) and keeps the
// table the entity reads.
const renaming = ref(false)
const newName = ref('')
const renameError = computed(() => {
  const name = newName.value.trim()
  if (!name) return t('common.required')
  return name !== props.name && ws.entity(name) ? t('names.taken') : null
})
function startRename() {
  newName.value = props.name
  renaming.value = true
}
function rename() {
  if (!entity.value || renameError.value) return
  const name = newName.value.trim()
  ws.upsertEntity({ ...entity.value, name }, props.name)
  renaming.value = false
  void router.replace({ name: 'entity', params: { name } })
}

function remove() {
  const affected = [...new Set(access.value.map((a) => `${t(a.kind === 'role' ? 'entity.accessRole' : 'entity.accessUser')} ${a.who}`))]
  dialog.warning({
    title: t('entity.deleteTitle', { name: props.name }),
    content: affected.length ? t('entity.deleteAffects', { list: affected.join(', ') }) : t('entity.deleteNoAccess'),
    positiveText: t('common.delete'),
    negativeText: t('common.cancel'),
    onPositiveClick: () => {
      ws.removeEntity(props.name)
      void router.push({ name: 'entities' })
    },
  })
}
</script>

<template>
  <n-empty v-if="!entity" :description="t('entity.notFound', { name })">
    <template #extra><n-button @click="router.push({ name: 'entities' })">{{ t('entity.backToList') }}</n-button></template>
  </n-empty>
  <n-space v-else vertical :size="16">
    <n-page-header @back="router.push({ name: 'entities' })">
      <template #title><span class="mono">{{ entity.name }}</span></template>
      <template #subtitle>{{ entity.description || comments?.description }}</template>
      <template #extra>
        <n-space v-if="editable">
          <n-button size="small" @click="startRename">{{ t('common.rename') }}</n-button>
          <n-button size="small" type="error" ghost @click="remove">{{ t('entity.delete') }}</n-button>
        </n-space>
      </template>
    </n-page-header>

    <n-modal v-model:show="renaming" preset="card" :title="t('entity.renameTitle')" class="dialog">
      <n-form-item :label="t('entity.name')" :feedback="renameError ?? t('entity.renameHint')"
        :validation-status="renameError ? 'error' : undefined">
        <n-input v-model:value="newName" @keyup.enter="rename" />
      </n-form-item>
      <template #footer>
        <n-space justify="end">
          <n-button @click="renaming = false">{{ t('common.cancel') }}</n-button>
          <n-button type="primary" :disabled="Boolean(renameError)" @click="rename">{{ t('common.confirm') }}</n-button>
        </n-space>
      </template>
    </n-modal>

    <n-card size="small" :title="t('entity.basics')">
      <n-descriptions :column="4" size="small" label-placement="top">
        <n-descriptions-item :label="t('entity.datasource')">{{ entity.datasource ?? 'default' }}</n-descriptions-item>
        <n-descriptions-item :label="t('entity.table')">
          <span class="mono">{{ entity.schema ? entity.schema + '.' : '' }}{{ entity.source ?? entity.name }}</span>
        </n-descriptions-item>
        <n-descriptions-item :label="t('entity.kind')">{{ kindLabel }}</n-descriptions-item>
        <n-descriptions-item :label="t('entity.primaryKey')"><span class="mono">{{ (entity.primaryKey ?? []).join(', ') || '—' }}</span></n-descriptions-item>
      </n-descriptions>
      <n-form label-placement="top" class="basics-form">
        <n-form-item :label="t('entity.description')" :show-feedback="false">
          <n-input v-model:value="entity.description" type="textarea" :autosize="{ minRows: 1, maxRows: 4 }"
            :disabled="!editable" :placeholder="comments?.description || t('entity.descriptionHint')" />
        </n-form-item>
        <n-text depth="3" class="hint">{{ t('entity.commentDefault') }}</n-text>
        <div class="switch-row">
          <n-switch :value="entity.mcp?.dmlTools ?? true" :disabled="!editable"
            @update:value="(v: boolean) => (entity!.mcp = { ...(entity!.mcp ?? {}), dmlTools: v })" />
          <div>
            <div>{{ t('entity.visible') }}</div>
            <n-text depth="3" class="hint">{{ t('entity.visibleHint') }}</n-text>
          </div>
        </div>
      </n-form>
    </n-card>

    <n-card size="small" :title="t('entity.fields')">
      <template #header-extra>
        <n-text depth="3" class="hint">{{ t('entity.commentDefault') }} {{ t('entity.maskHint') }}</n-text>
      </template>
      <n-data-table :columns="fieldColumns" :data="entity.fields ?? []" size="small" :row-key="(f: FieldInput) => f.name"
        :scroll-x="720" />
    </n-card>

    <n-card size="small" :title="t('entity.access')">
      <n-empty v-if="!access.length && !entity.legacyAccess" :description="t('entity.accessEmpty')" size="small" />
      <div v-else class="access">
        <div v-for="(a, i) in access" :key="i" class="access-row">
          <n-tag size="small" :bordered="false" :type="a.kind === 'role' ? 'info' : 'default'">
            {{ t(a.kind === 'role' ? 'entity.accessRole' : 'entity.accessUser') }}
          </n-tag>
          <router-link :to="{ name: a.kind, params: { name: a.who } }" class="mono link">{{ a.who }}</router-link>
          <n-text depth="3" class="hint">{{ a.d }}</n-text>
        </div>
        <n-alert v-if="entity.legacyAccess" type="default" :show-icon="false">
          {{ t('entity.legacy') }} <code>{{ JSON.stringify(entity.legacyAccess) }}</code>
        </n-alert>
      </div>
      <n-space v-if="editable && ws.roles.length" align="center" class="quick-grant">
        <n-select v-model:value="grantRole" size="small" class="role-select" :placeholder="t('entity.pickRole')"
          :options="ws.roles.map((r) => ({ label: r.name, value: r.name }))" />
        <n-button size="small" :disabled="!grantRole" @click="quickGrant">{{ t('entity.grantRead') }}</n-button>
      </n-space>
    </n-card>

    <n-card size="small" :title="t('entity.tenant')">
      <template #header-extra><n-text depth="3" class="hint">{{ t('entity.tenantHint') }}</n-text></template>
      <filter-builder :model-value="entity.tenantPolicy as Filter | undefined" :fields="fieldNames" :subject-keys="subjectKeys"
        :disabled="!editable" @update:model-value="(f) => (entity!.tenantPolicy = f)" />
    </n-card>

    <n-card size="small" :title="t('entity.relationships')">
      <div class="rels">
        <n-text v-if="!(entity.relationships ?? []).length" depth="3">{{ t('entity.noRelationships') }}</n-text>
        <div v-for="(r, i) in entity.relationships ?? []" :key="r.name" class="rel">
          <n-tag size="small" :bordered="false">{{ r.cardinality }}</n-tag>
          <span class="mono">{{ r.name }}</span>
          <n-text depth="3">→</n-text>
          <router-link class="mono link" :to="{ name: 'entity', params: { name: r.target } }">{{ r.target }}</router-link>
          <n-text depth="3" class="mono hint">{{ joinText(r) }}</n-text>
          <n-button v-if="editable" size="tiny" quaternary type="error"
            @click="entity!.relationships = (entity!.relationships ?? []).filter((_, j) => j !== i)">{{ t('common.remove') }}</n-button>
        </div>
      </div>
      <div v-if="editable" class="rel-form">
        <n-select v-model:value="newRel.cardinality" size="small" class="w140"
          :options="cardinalityOptions" />
        <n-select v-model:value="newRel.target" size="small" class="w160" :placeholder="t('entity.target')" filterable
          :options="ws.entities.filter((e) => e.name !== name).map((e) => ({ label: e.name, value: e.name }))" />
        <n-select v-model:value="newRelLocal" size="small" class="w140" :placeholder="t('entity.localField')" filterable
          :options="fieldNames.map((f) => ({ label: f, value: f }))" />
        <n-text depth="3">=</n-text>
        <n-select v-model:value="newRelRemote" size="small" class="w140" :placeholder="t('entity.targetField')" filterable
          :options="targetFields" />
        <n-input v-model:value="newRel.name" size="small" class="w140" :placeholder="t('entity.relName')" />
        <n-button size="small" :disabled="!newRel.target || !newRelLocal || !newRelRemote" @click="addRelationship">
          {{ t('common.add') }}
        </n-button>
      </div>
    </n-card>
  </n-space>
</template>

<style scoped>
.basics-form { margin-top: 12px; }
.switch-row { display: flex; gap: 12px; align-items: flex-start; margin-top: 14px; }
.hint { font-size: 12px; }
.access { display: flex; flex-direction: column; gap: 8px; }
.access-row, .rel { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.link { color: #2f6fed; text-decoration: none; }
.quick-grant { margin-top: 12px; }
.role-select { width: 200px; }
.rels { display: flex; flex-direction: column; gap: 8px; }
.rel-form { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; margin-top: 12px; }
.w120 { width: 120px; } .w140 { width: 140px; } .w160 { width: 160px; }
</style>
