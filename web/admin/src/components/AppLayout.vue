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
  TimeOutline, SettingsOutline, KeyOutline, PersonCircleOutline, ListOutline, NotificationsOutline, HardwareChipOutline,
} from '@vicons/ionicons5'
import ChangeBar from './ChangeBar.vue'
import DraftsInbox from './DraftsInbox.vue'
import RuntimeBadge from './RuntimeBadge.vue'
import ThemeSwitch from './ThemeSwitch.vue'
import LocaleSwitch from './LocaleSwitch.vue'
import { upgradeLegacySections, useWorkspace } from '@/stores/workspace'
import { useStatus } from '@/stores/status'
import { useSettingsEdits } from '@/stores/settingsEdits'
import { can, isLeaving, leave, logout, session } from '@/api/session'
import { adminURL } from '@/api/base'
import { draftsChanged } from '@/lib/draftFeed'
import { useWorkspaceAction, useWorkspaceSync, workspace } from '@/lib/workspaceSync'
import { useConflictSources } from '@/lib/conflicts'
import { liveUpdates } from '@/lib/liveUpdates'
import { guardUnload } from '@/lib/unloadGuard'
import logo from '@/assets/logo.svg'

const { t } = useI18n()
const ws = useWorkspace()
// The workspace is the signed-in administrator's, read from the server: one
// left by another administrator signed in before in this browser is dropped.
ws.bindTo(session.username)
// A workspace an older console kept here for whoever was signed in waits
// for its owner to claim it, or to drop it.
const legacy = ref(ws.legacy())
// Claiming it moves the workspace onto its base and writes its changes as
// edits there, so the server merges them with anything written meanwhile.
async function claimLegacy() {
  const old = ws.legacyWorkspace()
  if (!old || ws.dirty) return
  await action(async () => {
    await workspace.reset(old.base)
    const sections = upgradeLegacySections(old.sections, ws.baseTokens)
    ws.$patch((st) => Object.assign(st, sections))
    ws.dropLegacy()
    legacy.value = null
    message.success(t('legacy.claimed'))
  })
}
function dropLegacy() {
  ws.dropLegacy()
  legacy.value = null
}
const route = useRoute()
const router = useRouter()
const message = useMessage()
const action = useWorkspaceAction()
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
  { type: 'group', label: t('nav.batch'), key: 'g-batch', children: [
    { key: 'plans', label: link('plans', 'nav.plans'), icon: icon(ListOutline) },
    { key: 'pending', label: link('pending', 'nav.pending'), icon: icon(NotificationsOutline) },
    { key: 'agent', label: link('agent', 'nav.agent'), icon: icon(HardwareChipOutline) },
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
  void status.settle()
  const id = status.publishedId
  nothingPublished.value = id === null
  if (id && id !== ws.basePublished && !ws.dirty && ws.baseId === ws.basePublished) {
    if (await action(() => workspace.follow(id))) {
      message.info(t('layout.switchedTo', { id, author: status.published?.author ?? '' }))
    }
  }
}
// The workspace is kept on the server, the one copy this administrator (in
// any console) and their agents write: edits here are written there, and
// what is written there is read here (see lib/workspaceSync).
const sync = useWorkspaceSync()
const { sources: conflictSources } = useConflictSources()
ws.$subscribe(() => sync.schedulePush())

const outdated = computed(() =>
  status.publishedId !== null && ws.basePublished !== null && status.publishedId !== ws.basePublished)

async function rebase() {
  const count = await action(() => status.rebaseWorkspace())
  if (count !== undefined) {
    message.success(t('stale.rebased', { id: status.publishedId, count }, count))
  }
}

// Leaving asks first while anything is not on the server (see unloadGuard).
const settingsEdits = useSettingsEdits()
function onBeforeUnload(e: BeforeUnloadEvent) {
  guardUnload(e, {
    leaving: isLeaving, workspaceUnsaved: () => workspace.unsaved(),
    settingsUnsaved: () => settingsEdits.unsaved.length > 0, flush: () => workspace.flush(),
  })
}

// Publishes, drafts and workspace writes arrive as server-sent events;
// polling covers a stream a proxy cuts off, and the execution status.
const live = liveUpdates({
  url: adminURL('events'),
  poll: async () => {
    await poll().catch(() => undefined)
    await sync.pull()
  },
  events: {
    published: () => void poll(),
    revisions: () => draftsChanged.value++,
    workspace: () => void sync.pull(),
    open: () => void sync.pull(),
  },
})
onMounted(async () => {
  window.addEventListener('beforeunload', onBeforeUnload)
  try {
    nothingPublished.value = !(await sync.pull())
  } catch (e) {
    message.error(e instanceof Error ? e.message : String(e))
  }
  await poll()
  live.start()
})
onBeforeUnmount(() => {
  window.removeEventListener('beforeunload', onBeforeUnload)
  live.stop()
})

const userMenu = computed(() => [{ key: 'logout', label: t('layout.logout') }])
async function onUserMenu(key: string) {
  if (key === 'logout') {
    // What is edited here is saved first, while signed in.
    await action(() => workspace.flush())
    await logout()
    leave()
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
          <drafts-inbox />
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
          <n-alert v-if="legacy" type="info" class="banner" :title="t('legacy.title')">
            {{ t('legacy.body', { base: legacy.base, count: legacy.changes }) }}
            <div class="alert-actions">
              <n-button size="small" type="primary" :disabled="ws.dirty" @click="claimLegacy">
                {{ t('legacy.claim') }}
              </n-button>
              <n-button size="small" @click="dropLegacy">{{ t('legacy.drop') }}</n-button>
              <n-text v-if="ws.dirty" depth="3">{{ t('legacy.dirty') }}</n-text>
            </div>
          </n-alert>
          <n-alert v-if="outdated" type="warning" class="banner"
            :title="t('stale.title', { id: status.publishedId, author: status.published?.author ?? '' })">
            {{ ws.dirty
              ? t('stale.body', { base: ws.baseId, count: ws.changes.length, id: status.publishedId }, ws.changes.length)
              : t('stale.bodyClean', { base: ws.baseId }) }}
            <div class="alert-actions">
              <n-button v-if="ws.dirty" size="small" type="warning" @click="rebase">
                {{ t('stale.rebase', { id: status.publishedId }) }}
              </n-button>
              <n-button v-else size="small" type="warning" @click="action(() => workspace.reset(status.publishedId!))">
                {{ t('stale.load', { id: status.publishedId }) }}
              </n-button>
              <n-button size="small" @click="router.push({ name: 'revisions' })">{{ t('stale.history') }}</n-button>
            </div>
          </n-alert>
          <n-alert v-if="ws.openConflicts.length && route.name !== 'review'" type="error" class="banner"
            :title="t('conflicts.bannerTitle', { count: ws.openConflicts.length })">
            {{ t('conflicts.bannerBody', { sources: conflictSources(ws.openConflicts) }) }}
            <div class="alert-actions">
              <n-button size="small" type="error" @click="router.push({ name: 'review' })">{{ t('conflicts.resolve') }}</n-button>
            </div>
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
