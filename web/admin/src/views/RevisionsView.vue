<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useQuery } from '@urql/vue'
import { useRoute } from 'vue-router'
import { useI18n } from 'vue-i18n'
import {
  NAlert, NButton, NCard, NCode, NDescriptions, NDescriptionsItem, NEmpty, NGrid, NGi, NSpace, NSpin, NTabPane, NTabs,
  NTag, NText, NTime, NTimeline, NTimelineItem, useDialog, useMessage,
} from 'naive-ui'
import DiffView from '@/components/DiffView.vue'
import { draftsChanged } from '@/lib/draftFeed'
import { useDraftActions } from '@/lib/draftActions'
import { useRevisionDetail } from '@/lib/revisionDetail'
import { useWorkspaceAction, workspace } from '@/lib/workspaceSync'
import { useWorkspace } from '@/stores/workspace'
import { useStatus } from '@/stores/status'
import { ApiError, run } from '@/api/client'
import { DraftsQuery, PublishMutation, RevisionsQuery, RollbackMutation } from '@/api/ops'
import { can } from '@/api/session'

const { t, locale } = useI18n()
const ws = useWorkspace()
const status = useStatus()
const dialog = useDialog()
const message = useMessage()
const action = useWorkspaceAction()
// Older revisions load a page at a time; the list is fetched whole with a
// growing limit, so revisions created meanwhile never leave a gap.
const pageSize = 50
const limit = ref(pageSize)
const list = useQuery({ query: RevisionsQuery, variables: computed(() => ({ limit: limit.value })) })
const revisions = computed(() => list.data.value?.revisions ?? [])
const more = computed(() => revisions.value.length >= limit.value)
// The published revision may be older than the revisions listed.
const published = computed(() => revisions.value.find((r) => r.state === 'PUBLISHED') ?? status.published)
// ?id= opens a revision directly, e.g. a draft just saved at review.
const route = useRoute()
const draftActions = useDraftActions()
const selectedId = ref<string | null>(typeof route.query.id === 'string' ? route.query.id : null)
const selected = computed(() => revisions.value.find((r) => r.id === selectedId.value) ?? revisions.value[0])
// A draft saved elsewhere refreshes the list; opening one by ?id= while here
// selects it.
watch(draftsChanged, () => {
  list.executeQuery({ requestPolicy: 'network-only' })
  draftList.executeQuery({ requestPolicy: 'network-only' })
})
watch(() => route.query.id, (id) => {
  if (typeof id !== 'string') return
  selectedId.value = id
  list.executeQuery({ requestPolicy: 'network-only' })
})

const stateType: Record<string, 'success' | 'default' | 'warning' | 'info'> = {
  PUBLISHED: 'success', DRAFT: 'info', SUPERSEDED: 'default', ROLLED_BACK: 'warning', DISCARDED: 'default',
}
const when = (iso: string) => new Date(iso).toLocaleString(locale.value)

// A draft is compared with what it builds on (what it changes); another
// revision with the published one. A draft that does not build on the
// published revision cannot be published as it is: it is re-applied onto it.
// What each draft builds on, as the server works it out over the history.
const draftList = useQuery({ query: DraftsQuery })
const basedOn = (id: string) => draftList.data.value?.drafts.find((d) => d.revision.id === id)?.basedOn ?? null
const selectedBase = computed(() => (selected.value?.state === 'DRAFT' ? basedOn(selected.value.id) : null))
const stale = computed(() => !!selectedBase.value && !!published.value && selectedBase.value !== published.value.id)
const diffFrom = computed(() => selectedBase.value ?? published.value?.id ?? null)
const { yaml, diff, loading: loadingDetail, error: detailError } =
  useRevisionDetail(computed(() => selected.value?.id ?? null), diffFrom)

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
    if (e instanceof ApiError && e.code === 'CONFLICT') {
      // Another publish came first: show it; a draft is offered for review
      // onto it, a rollback reports the conflict below.
      list.executeQuery({ requestPolicy: 'network-only' })
      await status.refresh()
      if (selected.value?.state === 'DRAFT') return offerReview(selected.value.id)
    }
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
  void act((restart) => run(PublishMutation, {
    input: { id, restartRequired: restart, expectedPublished: current },
  }, true), t('revisions.published', { id }))
}

/**
 * Explains why a draft cannot be published as it is and offers to review it
 * in the workspace, onto the published revision.
 */
function offerReview(id: string) {
  const base = basedOn(id)
  const current = status.publishedId
  dialog.warning({
    title: t('revisions.staleTitle', { id, base, current }),
    content: t('revisions.staleBody', { base, current }),
    positiveText: t('drafts.review'),
    negativeText: t('common.cancel'),
    onPositiveClick: () => draftActions.review(id, { go: true }),
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
  const go = () => action(async () => {
    await workspace.load(id, false)
    message.success(t('revisions.loaded', { id }))
  })
  if (!ws.dirty) return void go()
  // Unsaved changes are merged onto the revision (conflicts resolved at
  // Review), or discarded.
  dialog.warning({
    title: t('revisions.loadWithChanges'),
    positiveText: t('revisions.loadKeeping'),
    negativeText: t('revisions.loadDiscarding'),
    onPositiveClick: () => action(async () => {
      await workspace.load(id, true)
      const n = ws.openConflicts.length
      if (n) message.warning(t('drafts.conflicts', { id, n }))
      else message.success(t('revisions.loaded', { id }))
    }),
    onNegativeClick: go,
  })
}
</script>

<template>
  <n-empty v-if="!revisions.length && !list.fetching.value" :description="t('revisions.empty')" />
  <n-grid v-else :cols="3" :x-gap="16" :y-gap="16" responsive="screen" item-responsive>
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
        <n-button v-if="more" block secondary size="small" :loading="list.fetching.value" @click="limit += pageSize">
          {{ t('common.loadMore') }}</n-button>
      </n-card>
    </n-gi>
    <n-gi span="3 l:2">
      <n-card v-if="selected" size="small" :title="t('revisions.title', { id: selected.id })">
        <template #header-extra>
          <n-space>
            <template v-if="selected.state === 'DRAFT'">
              <n-button v-if="can('admin:write')" size="small" @click="draftActions.discard(selected.id)">
                {{ t('drafts.discard') }}</n-button>
              <n-button v-if="!stale && can('admin:publish')" size="small" @click="publish(selected.id)">
                {{ t('revisions.publishDraft') }}</n-button>
              <n-button v-if="can('admin:write')" size="small" type="primary"
                @click="draftActions.review(selected.id, { go: true })">{{ t('drafts.review') }}</n-button>
            </template>
            <n-button v-else size="small" @click="loadIntoWorkspace(selected.id)">{{ t('revisions.load') }}</n-button>
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
            <n-tab-pane name="diff" :tab="selectedBase ? t('revisions.diffVsBase', { id: selectedBase })
              : published && published.id !== selected.id ? t('revisions.diffVs', { id: published.id }) : t('revisions.diff')">
              <n-alert v-if="stale" type="warning" :show-icon="false" class="gap">
                {{ t('revisions.staleBody', { base: selectedBase, current: published?.id }) }}
              </n-alert>
              <n-text v-if="published?.id === selected.id" depth="3">{{ t('revisions.isPublished') }}</n-text>
              <n-alert v-else-if="detailError" type="error" :show-icon="false">{{ detailError }}</n-alert>
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
.gap { margin-bottom: 8px; }
.yaml { max-height: 560px; overflow: auto; border: 1px solid rgba(128,128,128,.2); border-radius: 6px; padding: 10px 14px; }
</style>
