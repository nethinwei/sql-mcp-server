<script setup lang="ts">
import { computed } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { useQuery } from '@urql/vue'
import {
  NAlert, NButton, NCard, NGrid, NGi, NList, NListItem, NSpace, NStatistic, NTag, NText, NTime,
} from 'naive-ui'
import { useWorkspace } from '@/stores/workspace'
import { entityId } from '@/lib/entityRefs'
import { RevisionsQuery } from '@/api/ops'
import { can } from '@/api/session'

const { t } = useI18n()
const ws = useWorkspace()
const router = useRouter()
const revisions = useQuery({ query: RevisionsQuery, variables: { limit: 6 } })

const stateType: Record<string, 'success' | 'default' | 'warning' | 'info'> = {
  PUBLISHED: 'success', DRAFT: 'info', SUPERSEDED: 'default', ROLLED_BACK: 'warning',
}

const unreachable = computed(() =>
  ws.entities.filter((e) => {
    const a = ws.accessTo(entityId(e))
    return a.roles.length === 0 && a.users.length === 0 && !e.legacyAccess
  }),
)
const usersWithoutToken = computed(() => ws.users.filter((u) => !u.disabled && !ws.hasToken(u)))
const emptyRoles = computed(() => ws.roles.filter((r) => !ws.users.some((u) => (u.roles ?? []).includes(r.name))))
const stats = computed(() => [
  { key: 'datasources', value: ws.datasources.length, route: 'datasources' },
  { key: 'entities', value: ws.entities.length, route: 'entities' },
  { key: 'roles', value: ws.roles.length, route: 'roles' },
  { key: 'users', value: ws.users.length, route: 'users' },
])
</script>

<template>
  <n-space vertical :size="16">
    <n-grid :cols="4" :x-gap="16" :y-gap="16" responsive="screen" item-responsive>
      <n-gi v-for="s in stats" :key="s.key" span="2 m:1">
        <n-card size="small" hoverable class="stat" @click="router.push({ name: s.route })">
          <n-statistic :label="t(`overview.${s.key}`)" :value="s.value" />
        </n-card>
      </n-gi>
    </n-grid>

    <n-grid :cols="3" :x-gap="16" :y-gap="16" responsive="screen" item-responsive>
      <n-gi span="3 l:2">
        <n-card :title="t('overview.attention')" size="small" class="fill">
          <n-space vertical :size="12">
            <n-alert v-if="unreachable.length" type="info" :title="t('overview.unreachableTitle', { count: unreachable.length }, unreachable.length)">
              {{ t('overview.unreachableBody') }}
              <span class="mono">{{ unreachable.slice(0, 6).map((e) => ws.entityIndex.shortName(entityId(e))).join(', ') }}</span>
              <span v-if="unreachable.length > 6"> {{ t('overview.andMore') }}</span>
              <div class="alert-actions">
                <n-button size="small" @click="router.push({ name: 'roles' })">{{ t('overview.grantNow') }}</n-button>
              </div>
            </n-alert>
            <n-alert v-if="usersWithoutToken.length" type="warning" :title="t('overview.noTokenTitle', { count: usersWithoutToken.length }, usersWithoutToken.length)">
              {{ t('overview.noTokenBody') }}
              <span class="mono">{{ usersWithoutToken.map((u) => u.name).join(', ') }}</span>
              <div class="alert-actions">
                <n-button size="small" @click="router.push({ name: 'user', params: { name: usersWithoutToken[0].name } })">
                  {{ t('overview.generateNow') }}
                </n-button>
              </div>
            </n-alert>
            <n-alert v-if="emptyRoles.length" type="default" :title="t('overview.emptyRolesTitle', { count: emptyRoles.length }, emptyRoles.length)">
              <span class="mono">{{ emptyRoles.map((r) => r.name).join(', ') }}</span>
            </n-alert>
            <n-text v-if="!unreachable.length && !usersWithoutToken.length && !emptyRoles.length" depth="3">
              {{ t('overview.allGood') }}
            </n-text>
          </n-space>
        </n-card>
      </n-gi>
      <n-gi span="3 l:1">
        <n-card :title="t('overview.quick')" size="small" class="fill">
          <n-space vertical>
            <n-button block :disabled="!can('admin:write')" @click="router.push({ name: 'datasources' })">{{ t('overview.importTables') }}</n-button>
            <n-button block :disabled="!can('admin:write')" @click="router.push({ name: 'roles', query: { create: '1' } })">{{ t('overview.newRole') }}</n-button>
            <n-button block :disabled="!can('admin:write')" @click="router.push({ name: 'users', query: { create: '1' } })">{{ t('overview.newUser') }}</n-button>
            <n-button block @click="router.push({ name: 'simulate' })">{{ t('overview.simulateOnce') }}</n-button>
          </n-space>
        </n-card>
      </n-gi>
    </n-grid>

    <n-card :title="t('overview.recent')" size="small">
      <template #header-extra>
        <n-button text type="primary" @click="router.push({ name: 'revisions' })">{{ t('overview.allRevisions') }}</n-button>
      </template>
      <n-list :show-divider="false">
        <n-list-item v-for="r in revisions.data.value?.revisions ?? []" :key="r.id">
          <div class="rev">
            <n-space align="center" :size="8" :wrap="false" class="rev-main">
              <n-text strong>#{{ r.id }}</n-text>
              <n-tag size="small" :bordered="false" :type="stateType[r.state]">{{ t(`revisionState.${r.state}`) }}</n-tag>
              <n-text depth="2" class="rev-comment">{{ r.comment || t('common.noDescription') }}</n-text>
            </n-space>
            <n-text depth="3" class="rev-meta">{{ r.author }} · <n-time :time="new Date(r.createdAt)" type="relative" /></n-text>
          </div>
        </n-list-item>
      </n-list>
    </n-card>
  </n-space>
</template>

<style scoped>
.stat { cursor: pointer; }
.fill { height: 100%; }
.rev { display: flex; align-items: center; justify-content: space-between; gap: 12px; }
.rev-main { min-width: 0; }
.rev-comment { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.rev-meta { font-size: 12px; white-space: nowrap; }
</style>
