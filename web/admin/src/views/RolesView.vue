<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { NButton, NCard, NEmpty, NForm, NFormItem, NGrid, NGi, NInput, NModal, NSpace, NTag, NText } from 'naive-ui'
import { useWorkspace } from '@/stores/workspace'
import { nameError } from '@/lib/names'
import { can } from '@/api/session'

const { t } = useI18n()
const ws = useWorkspace()
const route = useRoute()
const router = useRouter()
const creating = ref(false)
const name = ref('')
const description = ref('')
const error = computed(() => nameError(name.value, ws.roles.map((r) => r.name)))

const cards = computed(() => ws.roles.map((r) => ({
  ...r,
  members: ws.users.filter((u) => (u.roles ?? []).includes(r.name)).map((u) => u.name),
  entities: [...new Set((r.grants ?? []).map((g) => g.entity))],
})))

function create() {
  if (error.value) return
  ws.upsertRole({ name: name.value, description: description.value, grants: [] })
  creating.value = false
  void router.push({ name: 'role', params: { name: name.value } })
}
onMounted(() => { if (route.query.create) creating.value = true })
</script>

<template>
  <n-space vertical :size="16">
    <div class="toolbar">
      <n-text depth="3">{{ t('roles.intro') }}</n-text>
      <n-button v-if="can('admin:write')" type="primary" size="small" @click="creating = true">{{ t('roles.create') }}</n-button>
    </div>
    <n-empty v-if="!ws.roles.length" :description="t('roles.empty')" />
    <n-grid :cols="3" :x-gap="14" :y-gap="14" responsive="screen" item-responsive>
      <n-gi v-for="r in cards" :key="r.name" span="3 m:1">
        <n-card size="small" hoverable class="card" @click="router.push({ name: 'role', params: { name: r.name } })">
          <template #header><span class="mono">{{ r.name }}</span></template>
          <div class="body">
            <n-text depth="3" class="desc">{{ r.description || t('common.noDescription') }}</n-text>
            <div class="tags">
              <n-tag v-for="e in r.entities.slice(0, 5)" :key="e" size="small" :bordered="false">{{ e }}</n-tag>
              <n-text v-if="r.entities.length > 5" depth="3" class="small">+{{ r.entities.length - 5 }}</n-text>
              <n-text v-if="!r.entities.length" type="warning" class="small">{{ t('roles.none') }}</n-text>
            </div>
            <n-text depth="3" class="small">
              {{ t('roles.members', { count: r.members.length }, r.members.length) }}<template v-if="r.members.length">: {{ r.members.join(', ') }}</template>
            </n-text>
          </div>
        </n-card>
      </n-gi>
    </n-grid>

    <n-modal v-model:show="creating" preset="card" :title="t('roles.create')" class="dialog">
      <n-form @submit.prevent="create">
        <n-form-item :label="t('roles.name')" :feedback="name ? error ?? '' : ''" :validation-status="name && error ? 'error' : undefined">
          <n-input v-model:value="name" :placeholder="t('roles.namePlaceholder')" autofocus />
        </n-form-item>
        <n-form-item :label="t('roles.description')">
          <n-input v-model:value="description" :placeholder="t('roles.descriptionPlaceholder')" />
        </n-form-item>
      </n-form>
      <template #footer>
        <n-space justify="end">
          <n-button @click="creating = false">{{ t('common.cancel') }}</n-button>
          <n-button type="primary" :disabled="Boolean(error)" @click="create">{{ t('roles.createAndConfigure') }}</n-button>
        </n-space>
      </template>
    </n-modal>
  </n-space>
</template>

<style scoped>
.toolbar { display: flex; justify-content: space-between; align-items: center; gap: 12px; flex-wrap: wrap; }
.card { cursor: pointer; height: 100%; }
.body { display: flex; flex-direction: column; gap: 8px; }
.desc { font-size: 13px; }
.tags { display: flex; flex-wrap: wrap; gap: 4px; align-items: center; }
.small { font-size: 12px; }
</style>
