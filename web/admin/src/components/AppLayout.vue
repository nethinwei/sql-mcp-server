<script setup lang="ts">
import { computed, h, onBeforeUnmount, onMounted, ref, type Component } from 'vue'
import { RouterLink, useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import {
  NAlert, NButton, NDropdown, NIcon, NLayout, NLayoutContent, NLayoutHeader, NLayoutSider, NMenu,
  NSpace, NSpin, NTag, NText, useMessage, type MenuOption,
} from 'naive-ui'
import {
  GridOutline, ServerOutline, CubeOutline, ShieldCheckmarkOutline, PeopleOutline, FlaskOutline,
  TimeOutline, SettingsOutline, KeyOutline, PersonCircleOutline,
} from '@vicons/ionicons5'
import ChangeBar from './ChangeBar.vue'
import RuntimeBadge from './RuntimeBadge.vue'
import ThemeSwitch from './ThemeSwitch.vue'
import LocaleSwitch from './LocaleSwitch.vue'
import { useWorkspace } from '@/stores/workspace'
import { useStatus } from '@/stores/status'
import { useSettingsEdits } from '@/stores/settingsEdits'
import { can, logout, session } from '@/api/session'
import logo from '@/assets/logo.svg'

const { t } = useI18n()
const ws = useWorkspace()
const route = useRoute()
const router = useRouter()
const message = useMessage()
const status = useStatus()
const collapsed = ref(false)
const nothingPublished = ref(false)

function icon(c: Component) {
  return () => h(NIcon, null, { default: () => h(c) })
}
function link(name: string, key: string) {
  return () => h(RouterLink, { to: { name } }, { default: () => t(key) })
}

const menu = computed<MenuOption[]>(() => [
  { key: 'overview', label: link('overview', 'nav.overview'), icon: icon(GridOutline) },
  { type: 'group', label: t('nav.data'), key: 'g-data', children: [
    { key: 'datasources', label: link('datasources', 'nav.datasources'), icon: icon(ServerOutline) },
    { key: 'entities', label: link('entities', 'nav.entities'), icon: icon(CubeOutline) },
  ] },
  { type: 'group', label: t('nav.access'), key: 'g-access', children: [
    { key: 'roles', label: link('roles', 'nav.roles'), icon: icon(ShieldCheckmarkOutline) },
    { key: 'users', label: link('users', 'nav.users'), icon: icon(PeopleOutline) },
    { key: 'simulate', label: link('simulate', 'nav.simulate'), icon: icon(FlaskOutline) },
  ] },
  { type: 'group', label: t('nav.release'), key: 'g-release', children: [
    { key: 'revisions', label: link('revisions', 'nav.revisions'), icon: icon(TimeOutline) },
    { key: 'settings', label: link('settings', 'nav.settings'), icon: icon(SettingsOutline) },
    ...(can('admin:accounts')
      ? [{ key: 'admins', label: link('admins', 'nav.admins'), icon: icon(KeyOutline) }]
      : []),
  ] },
])

const activeKey = computed(() => {
  const name = String(route.name ?? '')
  return ({ entity: 'entities', role: 'roles', user: 'users', review: '' } as Record<string, string>)[name] ?? name
})

// Polls the server; a clean workspace that tracks the published revision
// follows new publishes, anything else gets the banner below.
async function poll() {
  await status.refresh()
  if (!status.loaded) return
  const id = status.publishedId
  nothingPublished.value = id === null
  if (id && id !== ws.basePublished && !ws.dirty && ws.baseId === ws.basePublished) {
    await ws.load(id)
    message.info(t('layout.switchedTo', { id, author: status.published?.author ?? '' }))
  }
}
function onVisible() {
  if (document.visibilityState === 'visible') void poll()
}

const outdated = computed(() =>
  status.publishedId !== null && ws.basePublished !== null && status.publishedId !== ws.basePublished)
const conflictList = computed(() =>
  status.conflicts.map((c) => `${t(`changes.kind.${c.kind}`)} ${c.kind === 'settings' ? t('changes.settingsName') : c.name}`)
    .join(t('common.listSep')))

async function rebase() {
  try {
    const count = await status.rebaseWorkspace()
    message.success(t('stale.rebased', { id: status.rebasedOnto, count }, count))
  } catch (e) {
    message.error(e instanceof Error ? e.message : String(e))
  }
}

// The workspace is kept in localStorage, but unapplied settings text is not.
const settingsEdits = useSettingsEdits()
function onBeforeUnload(e: BeforeUnloadEvent) {
  if (settingsEdits.pending.length) e.preventDefault()
}

let timer: number | undefined
onMounted(async () => {
  window.addEventListener('beforeunload', onBeforeUnload)
  if (ws.baseId) {
    try {
      const moved = await ws.verifyBase()
      if (moved) {
        status.conflicts = moved.conflicts
        status.rebasedOnto = moved.conflicts.length ? moved.id : null
        message.warning(t('layout.baseReloaded', { id: moved.id }))
      }
    } catch (e) {
      message.error(e instanceof Error ? e.message : String(e))
    }
  } else {
    try {
      nothingPublished.value = !(await ws.loadPublished())
    } catch (e) {
      message.error(e instanceof Error ? e.message : String(e))
    }
  }
  await poll()
  timer = window.setInterval(() => document.visibilityState === 'visible' && void poll(), 10000)
  document.addEventListener('visibilitychange', onVisible)
})
onBeforeUnmount(() => {
  window.removeEventListener('beforeunload', onBeforeUnload)
  window.clearInterval(timer)
  document.removeEventListener('visibilitychange', onVisible)
})

const userMenu = computed(() => [{ key: 'logout', label: t('layout.logout') }])
async function onUserMenu(key: string) {
  if (key === 'logout') {
    await logout()
    await router.push({ name: 'login' })
  }
}
</script>

<template>
  <n-layout has-sider class="shell">
    <n-layout-sider
      bordered collapse-mode="width" :collapsed-width="64" :width="220" show-trigger
      :collapsed="collapsed" @collapse="collapsed = true" @expand="collapsed = false"
    >
      <div class="brand" :class="{ collapsed }">
        <img :src="logo" class="logo" alt="" /><span v-if="!collapsed" class="brand-name">{{ t('app.title') }}</span>
      </div>
      <n-menu :value="activeKey" :options="menu" :collapsed="collapsed" :collapsed-width="64" />
    </n-layout-sider>
    <n-layout class="main">
      <n-layout-header bordered class="header">
        <n-space align="center" :size="8" :wrap="false">
          <n-text depth="3" class="nowrap">{{ t('layout.basedOn') }}</n-text>
          <n-tag v-if="ws.baseId" size="small" :bordered="false" type="info">{{ t('layout.version', { id: ws.baseId }) }}</n-tag>
          <n-tag v-else size="small" :bordered="false">{{ t('layout.notLoaded') }}</n-tag>
        </n-space>
        <n-space align="center" :size="12" :wrap="false">
          <runtime-badge />
          <change-bar />
          <locale-switch />
          <theme-switch />
          <n-dropdown :options="userMenu" @select="onUserMenu">
            <n-button quaternary size="small">
              <template #icon><n-icon><person-circle-outline /></n-icon></template>
              {{ session.username }}
            </n-button>
          </n-dropdown>
        </n-space>
      </n-layout-header>
      <n-layout-content class="content" content-style="padding: 20px 24px;" :native-scrollbar="false">
        <div class="page">
          <n-alert v-if="outdated" type="warning" class="banner"
            :title="t('stale.title', { id: status.publishedId, author: status.published?.author ?? '' })">
            {{ ws.dirty
              ? t('stale.body', { base: ws.baseId, count: ws.changes.length, id: status.publishedId }, ws.changes.length)
              : t('stale.bodyClean', { base: ws.baseId }) }}
            <div class="alert-actions">
              <n-button v-if="ws.dirty" size="small" type="warning" @click="rebase">
                {{ t('stale.rebase', { id: status.publishedId }) }}
              </n-button>
              <n-button v-else size="small" type="warning" @click="ws.load(status.publishedId!)">
                {{ t('stale.load', { id: status.publishedId }) }}
              </n-button>
              <n-button size="small" @click="router.push({ name: 'revisions' })">{{ t('stale.history') }}</n-button>
            </div>
          </n-alert>
          <n-alert v-if="status.conflicts.length" type="info" class="banner" closable
            :title="t('stale.conflictsTitle', { id: status.rebasedOnto })" @close="status.dismissConflicts()">
            {{ t('stale.conflictsBody', { list: conflictList, id: status.rebasedOnto }) }}
          </n-alert>
          <n-alert v-if="nothingPublished" type="info" class="banner" :title="t('layout.nothingPublishedTitle')">
            {{ t('layout.nothingPublishedBody') }}
          </n-alert>
          <n-spin :show="ws.loading">
            <router-view />
          </n-spin>
        </div>
      </n-layout-content>
    </n-layout>
  </n-layout>
</template>

<style scoped>
.shell { height: 100%; }
.main { height: 100%; display: flex; flex-direction: column; }
.header { height: 56px; flex: none; padding: 0 20px; display: flex; align-items: center; justify-content: space-between; gap: 16px; }
.content { flex: 1; min-height: 0; }
.page { max-width: 1280px; margin: 0 auto; }
.banner { margin-bottom: 16px; }
.brand { height: 56px; display: flex; align-items: center; gap: 10px; padding: 0 20px; font-weight: 600; white-space: nowrap; overflow: hidden; }
.brand.collapsed { justify-content: center; padding: 0; }
.brand-name { overflow: hidden; text-overflow: ellipsis; }
.logo { width: 28px; height: 28px; flex: none; }
.nowrap { white-space: nowrap; }
</style>
