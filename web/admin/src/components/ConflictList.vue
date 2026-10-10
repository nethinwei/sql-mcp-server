<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { NButton, NCard, NSpace, NTag, NText } from 'naive-ui'
import { useWorkspace, type Conflict } from '@/stores/workspace'
import { useConflictSources } from '@/lib/conflicts'
import { useWorkspaceAction, workspace } from '@/lib/workspaceSync'

// The values this workspace and another side (a newer published revision, a
// draft taken in, a write made meanwhile) changed differently, as the server
// found them, each resolved there by choosing a side.
const { t } = useI18n()
const ws = useWorkspace()
const action = useWorkspaceAction()
const { source, sources } = useConflictSources()

function item(c: Conflict) {
  if (c.kind === 'settings') return t('changes.settingsName')
  const name = c.kind === 'entity' ? ws.entityIndex.shortName(c.key) : c.key
  return `${t(`changes.kind.${c.kind}`)} ${name}`
}
function show(v: unknown, gone: boolean) {
  if (gone) return t('conflicts.removed')
  return typeof v === 'string' ? v : JSON.stringify(v)
}
</script>

<template>
  <n-card size="small" :title="t('conflicts.title', { count: ws.openConflicts.length })">
    <template #header-extra><n-text depth="3" class="hint">{{ t('conflicts.hint', { sources: sources(ws.openConflicts) }) }}</n-text></template>
    <div class="list">
      <div v-for="c in ws.openConflicts" :key="c.id" class="row">
        <div class="where">
          <n-text strong>{{ item(c) }}</n-text>
          <span v-if="c.path" class="mono path">{{ c.path }}</span>
        </div>
        <div class="values">
          <div><n-tag size="tiny" :bordered="false" type="info">{{ t('conflicts.mine') }}</n-tag>
            <code class="value">{{ show(c.mine, c.mineGone) }}</code></div>
          <div><n-tag size="tiny" :bordered="false" type="warning">{{ source(c) }}</n-tag>
            <code class="value">{{ show(c.theirs, c.theirsGone) }}</code></div>
        </div>
        <n-space :size="6" :wrap="false">
          <n-button size="small" @click="action(() => workspace.resolve(c.id, false))">{{ t('conflicts.keepMine') }}</n-button>
          <n-button size="small" @click="action(() => workspace.resolve(c.id, true))">{{ t('conflicts.takeTheirs', { source: source(c) }) }}</n-button>
        </n-space>
      </div>
    </div>
  </n-card>
</template>

<style scoped>
.hint { font-size: 12px; }
.list { display: flex; flex-direction: column; gap: 12px; }
.row { display: grid; grid-template-columns: minmax(160px, 1fr) minmax(240px, 2fr) auto; gap: 12px; align-items: center; }
.where { display: flex; flex-direction: column; gap: 2px; }
.path { font-size: 12px; opacity: .7; }
.values { display: flex; flex-direction: column; gap: 4px; min-width: 0; }
.value { font-size: 12px; margin-left: 6px; word-break: break-all; }
</style>
