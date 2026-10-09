<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { NAlert, NButton, NCheckbox, NCheckboxGroup, NModal, NSpace, NSpin, NSwitch, NTag, NText, useMessage } from 'naive-ui'
import { run } from '@/api/client'
import { CatalogTableQuery } from '@/api/ops'
import { createResource } from '@/lib/asyncResource'
import { fieldSync } from '@/lib/fieldSync'
import { followsRemoval, useWorkspace, type FieldReference } from '@/stores/workspace'

// Field sync of one entity: the columns to add and the fields to remove,
// with what references each removed field and a way to go and fix it. The
// field lists of grants follow on their own; other references block publish.
const props = defineProps<{ tableRef: string | null; entity: string | null }>()
const show = defineModel<boolean>('show', { required: true })
const { t } = useI18n()
const ws = useWorkspace()
const router = useRouter()
const message = useMessage()

// The table is read each time the dialog opens; differences are computed and
// applied only from a table that was read (not while loading, after a
// failure, or when the table is not found).
const resource = createResource(async (ref: string) =>
  (await run(CatalogTableQuery, { ref, draft: ws.computeDraft })).catalogTable)
const addChecked = ref<string[]>([])
const removeChecked = ref<string[]>([])
const hideNew = ref(false)

const current = computed(() => (props.entity ? ws.entity(props.entity) : undefined))
const sync = computed(() => (resource.state.status === 'ok' ? fieldSync(current.value, resource.state.data) : null))
const added = computed(() => sync.value?.added ?? [])
const removed = computed(() => sync.value?.removed ?? [])

watch(show, async (open) => {
  addChecked.value = []
  removeChecked.value = []
  hideNew.value = false
  if (!open || !props.tableRef) return resource.reset()
  await resource.load(props.tableRef)
  if (!sync.value) return
  addChecked.value = added.value.map((c) => c.name)
  removeChecked.value = removed.value.filter((f) => !blocking(f).length)
})

const references = (field: string) => (props.entity ? ws.fieldReferences(props.entity, field) : [])
/** References a removal does not take care of: they block removing the field. */
const blocking = (field: string) => references(field).filter((r) => !followsRemoval(r))
const refLabel = (r: FieldReference) => t(`datasources.ref.${r.kind}`, {
  name: r.kind === 'role' || r.kind === 'user' ? r.name : ws.entityIndex.shortName(r.name),
}) + (r.via === 'rows' ? t('catalog.viaRows') : '')

function locate(r: FieldReference) {
  show.value = false
  if (r.kind === 'role') void router.push({ name: 'role', params: { name: r.name } })
  else if (r.kind === 'user') void router.push({ name: 'user', params: { name: r.name } })
  else void router.push({ name: 'entity', params: { id: r.name } })
}

function apply() {
  if (!props.entity || !sync.value) return
  const fields = added.value.filter((c) => addChecked.value.includes(c.name))
    .map((c) => (hideNew.value ? { name: c.name, exclude: true } : { name: c.name }))
  ws.syncFields(props.entity, fields, removeChecked.value.filter((f) => !blocking(f).length))
  show.value = false
  message.success(t('datasources.synced', {
    name: ws.entityIndex.shortName(props.entity), added: fields.length, removed: removeChecked.value.length,
  }))
}
</script>

<template>
  <n-modal v-model:show="show" preset="card" class="dialog" style="width: 760px"
    :title="t('datasources.syncTitle', { name: entity ? ws.entityIndex.shortName(entity) : '' })">
    <n-spin :show="resource.state.status === 'loading'">
      <n-alert v-if="resource.state.status === 'error'" type="error" :show-icon="false">{{ resource.state.error }}</n-alert>
      <n-alert v-else-if="resource.state.status === 'notFound'" type="warning" :show-icon="false">
        {{ t('catalog.tableNotFound', { ref: tableRef }) }}
      </n-alert>
      <n-alert v-else-if="resource.state.status === 'ok' && !current" type="warning" :show-icon="false">
        {{ t('catalog.entityGone') }}
      </n-alert>
      <n-space v-if="sync" vertical :size="14" class="body">
        <n-text v-if="!added.length && !removed.length" depth="3">{{ t('catalog.inSync') }}</n-text>
        <div v-if="added.length">
          <n-text strong>{{ t('datasources.syncAdded', { n: added.length }) }}</n-text>
          <n-checkbox-group v-model:value="addChecked" class="list">
            <n-checkbox v-for="c in added" :key="c.name" :value="c.name">
              <span class="mono">{{ c.name }}</span>
              <n-text depth="3" class="meta">{{ c.type }}{{ c.description ? ' · ' + c.description : '' }}</n-text>
            </n-checkbox>
          </n-checkbox-group>
          <div class="switch">
            <n-switch v-model:value="hideNew" size="small" />
            <n-text class="meta">{{ t('datasources.syncHideNew') }}</n-text>
          </div>
        </div>
        <div v-if="removed.length">
          <n-text strong>{{ t('datasources.syncRemoved', { n: removed.length }) }}</n-text>
          <n-checkbox-group v-model:value="removeChecked" class="list">
            <div v-for="f in removed" :key="f" class="removed">
              <n-checkbox :value="f" :disabled="blocking(f).length > 0"><span class="mono">{{ f }}</span></n-checkbox>
              <n-text v-if="blocking(f).length" type="warning" class="meta">{{ t('catalog.removalBlocked') }}</n-text>
              <n-tag v-for="r in references(f)" :key="r.kind + r.name" size="tiny" :bordered="false" class="ref"
                :type="followsRemoval(r) ? 'default' : 'warning'">
                {{ refLabel(r) }}
                <n-button text size="tiny" type="primary" class="locate" @click="locate(r)">{{ t('catalog.locate') }}</n-button>
              </n-tag>
            </div>
          </n-checkbox-group>
          <n-text depth="3" class="meta">{{ t('datasources.syncRefsHint') }}</n-text>
        </div>
      </n-space>
    </n-spin>
    <template #footer>
      <n-space justify="end">
        <n-button @click="show = false">{{ t('common.cancel') }}</n-button>
        <n-button type="primary" :disabled="!sync || (!addChecked.length && !removeChecked.length)" @click="apply">
          {{ t('datasources.syncApply', { added: addChecked.length, removed: removeChecked.length }) }}
        </n-button>
      </n-space>
    </template>
  </n-modal>
</template>

<style scoped>
.body { max-height: 60vh; overflow: auto; }
.list { display: flex; flex-direction: column; gap: 6px; margin-top: 6px; }
.removed { display: flex; align-items: center; flex-wrap: wrap; gap: 4px; }
.meta { margin-left: 6px; font-size: 12px; }
.switch { display: flex; align-items: center; gap: 4px; margin-top: 8px; }
.ref { margin-left: 6px; }
.locate { margin-left: 4px; }
</style>
