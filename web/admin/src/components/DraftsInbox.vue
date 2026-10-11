<script setup lang="ts">
import { computed, watch } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { useQuery } from '@urql/vue'
import { NBadge, NButton, NList, NListItem, NPopover, NSpace, NTag, NText, NTime } from 'naive-ui'
import { DraftsQuery } from '@/api/ops'
import { firstLine, pendingDrafts } from '@/lib/drafts'
import { draftsChanged } from '@/lib/draftFeed'
import { useDraftActions } from '@/lib/draftActions'
import { can } from '@/api/session'
import { useStatus } from '@/stores/status'
import { useWorkspace } from '@/stores/workspace'

// The drafts waiting for review, whoever saved them (an administrator here or
// elsewhere, or an agent through the configuration MCP endpoint): each is
// taken into the workspace as unsaved changes, as if made here, or discarded;
// it opens in the revision history too.
const { t } = useI18n()
const router = useRouter()
const status = useStatus()
const ws = useWorkspace()
const list = useQuery({ query: DraftsQuery, requestPolicy: 'cache-and-network' })
const refresh = () => list.executeQuery({ requestPolicy: 'network-only' })
watch([draftsChanged, () => status.publishedId, () => ws.savedDraftId, () => ws.taken.length], refresh)
const actions = useDraftActions()

// Drafts already taken into this workspace are being reviewed here.
const drafts = computed(() => pendingDrafts(list.data.value?.drafts ?? [], status.publishedId, ws.taken))
const open = (id: string) => router.push({ name: 'revisions', query: { id } })
</script>

<template>
  <n-popover v-if="drafts.length" trigger="click" placement="bottom-end" :width="380">
    <template #trigger>
      <n-badge :value="drafts.length" :offset="[-4, 2]">
        <n-button size="small" secondary type="info">{{ t('drafts.button') }}</n-button>
      </n-badge>
    </template>
    <n-text depth="3" class="hint">{{ t('drafts.hint', { id: status.publishedId ?? '—' }) }}</n-text>
    <n-list hoverable clickable :show-divider="false" class="list">
      <n-list-item v-for="d in drafts" :key="d.revision.id" @click="open(d.revision.id)">
        <n-space align="center" :size="6" :wrap="false">
          <n-text strong>#{{ d.revision.id }}</n-text>
          <n-tag v-if="d.stale" size="tiny" :bordered="false">{{ t('drafts.stale', { id: d.basedOn ?? '—' }) }}</n-tag>
          <n-tag v-if="d.revision.id === ws.savedDraftId" size="tiny" :bordered="false">{{ t('drafts.mine') }}</n-tag>
        </n-space>
        <div class="comment">{{ firstLine(d.revision.comment) || t('common.noDescription') }}</div>
        <n-text depth="3" class="meta">
          {{ d.revision.author }} · <n-time :time="new Date(d.revision.createdAt)" type="relative" />
        </n-text>
        <n-space v-if="can('admin:write')" :size="6" class="actions">
          <n-button size="tiny" type="primary" @click.stop="actions.review(d.revision.id)">
            {{ t('drafts.review') }}
          </n-button>
          <n-button size="tiny" @click.stop="actions.discard(d.revision.id)">{{ t('drafts.discard') }}</n-button>
        </n-space>
      </n-list-item>
    </n-list>
  </n-popover>
</template>

<style scoped>
.hint { font-size: 12px; }
.list { margin-top: 6px; max-height: 360px; overflow: auto; }
.comment { font-size: 13px; margin: 2px 0; }
.meta { font-size: 12px; }
.actions { margin-top: 4px; }
</style>
