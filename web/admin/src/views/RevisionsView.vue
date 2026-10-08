<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useQuery } from '@urql/vue'
import { useI18n } from 'vue-i18n'
import {
  NButton, NCard, NCode, NDescriptions, NDescriptionsItem, NEmpty, NGrid, NGi, NSpace, NSpin, NTabPane, NTabs,
  NTag, NText, NTime, NTimeline, NTimelineItem, useDialog, useMessage,
} from 'naive-ui'
import DiffView from '@/components/DiffView.vue'
import { useWorkspace } from '@/stores/workspace'
import { useStatus } from '@/stores/status'
import { run } from '@/api/client'
import { DiffQuery, PublishMutation, RevisionYamlQuery, RevisionsQuery, RollbackMutation } from '@/api/ops'
import { can } from '@/api/session'

const { t, locale } = useI18n()
const ws = useWorkspace()
const status = useStatus()
const dialog = useDialog()
const message = useMessage()
const list = useQuery({ query: RevisionsQuery, variables: { limit: 100 } })
const revisions = computed(() => list.data.value?.revisions ?? [])
const published = computed(() => revisions.value.find((r) => r.state === 'PUBLISHED'))
const selectedId = ref<string | null>(null)
const selected = computed(() => revisions.value.find((r) => r.id === selectedId.value) ?? revisions.value[0])

const stateType: Record<string, 'success' | 'default' | 'warning' | 'info'> = {
  PUBLISHED: 'success', DRAFT: 'info', SUPERSEDED: 'default', ROLLED_BACK: 'warning',
}
const when = (iso: string) => new Date(iso).toLocaleString(locale.value)

const yaml = ref('')
const diff = ref('')
const loadingDetail = ref(false)
watch(selected, async (r) => {
  if (!r) return
  loadingDetail.value = true
  try {
    yaml.value = (await run(RevisionYamlQuery, { id: r.id })).revision?.yaml ?? ''
    diff.value = published.value && published.value.id !== r.id
      ? (await run(DiffQuery, { from: published.value.id, to: r.id })).diff
      : ''
  } finally {
    loadingDetail.value = false
  }
}, { immediate: true })

/**
 * Runs a publish-like operation; when the server reports that the change needs
 * a restart, asks for confirmation and retries with restartRequired.
 */
async function act(op: (restartRequired: boolean) => Promise<unknown>, done: string, restartRequired = false) {
  try {
    await op(restartRequired)
    message.success(done)
    list.executeQuery({ requestPolicy: 'network-only' })
    void status.refresh()
  } catch (e) {
    const msg = e instanceof Error ? e.message : String(e)
    if (!restartRequired && msg.includes('requires restart')) {
      dialog.warning({
        title: t('revisions.restartTitle'),
        content: msg,
        positiveText: t('revisions.continueAnyway'),
        negativeText: t('common.cancel'),
        onPositiveClick: () => act(op, done, true),
      })
      return
    }
    message.error(msg)
  }
}

function publish(id: string) {
  const current = published.value?.id ?? null
  const go = () => act((restart) => run(PublishMutation, {
    input: { id, restartRequired: restart, expectedPublished: current },
  }, true), t('revisions.published', { id }))
  // A draft based on an older revision replaces the newer published content.
  const parent = revisions.value.find((r) => r.id === id)?.parent
  if (!current || !parent || parent === current) return void go()
  dialog.warning({
    title: t('stale.staleDraftTitle', { parent, current }),
    content: t('stale.staleDraftBody', { current }),
    positiveText: t('stale.publishAnyway'),
    negativeText: t('common.cancel'),
    onPositiveClick: go,
  })
}
function rollback(id: string) {
  dialog.warning({
    title: t('revisions.rollbackTitle', { id }),
    content: t('revisions.rollbackBody', { id }),
    positiveText: t('revisions.rollback'),
    negativeText: t('common.cancel'),
    onPositiveClick: () => act((restart) =>
      run(RollbackMutation, { input: { to: id, restartRequired: restart, comment: t('revisions.rollbackComment', { id }) } }, true),
      t('revisions.rolledBack')),
  })
}
function loadIntoWorkspace(id: string) {
  const go = async () => {
    await ws.load(id)
    message.success(t('revisions.loaded', { id }))
  }
  if (!ws.dirty) return void go()
  dialog.warning({
    title: t('revisions.loadDiscards'),
    positiveText: t('revisions.load'),
    negativeText: t('common.cancel'),
    onPositiveClick: go,
  })
}
</script>

<template>
  <n-empty v-if="!revisions.length && !list.fetching.value" :description="t('revisions.empty')" />
  <n-grid v-else :cols="3" :x-gap="16" responsive="screen" item-responsive>
    <n-gi span="3 l:1">
      <n-card size="small" :title="t('revisions.list')" content-style="max-height: calc(100vh - 200px); overflow: auto">
        <n-timeline>
          <n-timeline-item v-for="r in revisions" :key="r.id"
            :type="r.state === 'PUBLISHED' ? 'success' : r.state === 'DRAFT' ? 'info' : 'default'">
            <div class="item" :class="{ active: selected?.id === r.id }" @click="selectedId = r.id">
              <n-space align="center" :size="6">
                <n-text strong>#{{ r.id }}</n-text>
                <n-tag size="tiny" :bordered="false" :type="stateType[r.state]">{{ t(`revisionState.${r.state}`) }}</n-tag>
                <n-tag v-if="ws.baseId === r.id" size="tiny" :bordered="false" type="info">{{ t('revisions.basedOnTag') }}</n-tag>
              </n-space>
              <div class="comment">{{ r.comment || t('common.noDescription') }}</div>
              <n-text depth="3" style="font-size: 12px">{{ r.author }} · <n-time :time="new Date(r.createdAt)" type="relative" /></n-text>
            </div>
          </n-timeline-item>
        </n-timeline>
      </n-card>
    </n-gi>
    <n-gi span="3 l:2">
      <n-card v-if="selected" size="small" :title="t('revisions.title', { id: selected.id })">
        <template #header-extra>
          <n-space>
            <n-button size="small" @click="loadIntoWorkspace(selected.id)">{{ t('revisions.load') }}</n-button>
            <n-button v-if="selected.state === 'DRAFT' && can('admin:publish')" size="small" type="primary"
              @click="publish(selected.id)">{{ t('revisions.publishDraft') }}</n-button>
            <n-button v-if="(selected.state === 'SUPERSEDED' || selected.state === 'ROLLED_BACK') && can('admin:publish')"
              size="small" type="warning" ghost @click="rollback(selected.id)">{{ t('revisions.rollback') }}</n-button>
          </n-space>
        </template>
        <n-descriptions :column="3" size="small" label-placement="top" style="margin-bottom: 12px">
          <n-descriptions-item :label="t('revisions.author')">{{ selected.author }}</n-descriptions-item>
          <n-descriptions-item :label="t('revisions.created')">{{ when(selected.createdAt) }}</n-descriptions-item>
          <n-descriptions-item :label="t('revisions.publishedAt')">{{ selected.publishedAt ? when(selected.publishedAt) : '—' }}</n-descriptions-item>
          <n-descriptions-item :label="t('revisions.parent')">{{ selected.parent ? '#' + selected.parent : '—' }}</n-descriptions-item>
          <n-descriptions-item :label="t('revisions.hash')" :span="2"><span class="mono">{{ selected.contentHash.slice(7, 23) }}…</span></n-descriptions-item>
        </n-descriptions>
        <n-spin :show="loadingDetail">
          <n-tabs type="line">
            <n-tab-pane name="diff" :tab="published && published.id !== selected.id ? t('revisions.diffVs', { id: published.id }) : t('revisions.diff')">
              <n-text v-if="published?.id === selected.id" depth="3">{{ t('revisions.isPublished') }}</n-text>
              <diff-view v-else :diff="diff" />
            </n-tab-pane>
            <n-tab-pane name="yaml" :tab="t('revisions.yaml')">
              <div class="yaml"><n-code :code="yaml" language="yaml" word-wrap /></div>
            </n-tab-pane>
          </n-tabs>
        </n-spin>
      </n-card>
    </n-gi>
  </n-grid>
</template>

<style scoped>
.item { cursor: pointer; padding: 2px 6px; border-radius: 6px; margin: -2px -6px; }
.item:hover, .item.active { background: rgba(47,111,237,.08); }
.comment { font-size: 13px; margin: 2px 0; }
.yaml { max-height: 560px; overflow: auto; border: 1px solid rgba(128,128,128,.2); border-radius: 6px; padding: 10px 14px; }
</style>
