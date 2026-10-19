<script setup lang="ts">
import { computed, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  NAlert, NButton, NCollapse, NCollapseItem, NInput, NList, NListItem, NPagination, NSpace, NTag, NText,
} from 'naive-ui'
import type { PlanReportPartsFragment } from '@/gql/graphql'
import { useWorkspace } from '@/stores/workspace'
import { download, toCSV } from '@/lib/selection'
import { reasonText } from '@/lib/reasons'

// What a plan covers, what it changes and the exceptions to look at, in
// full: a change applied to many tables reads as one line and opens to the
// entities it changes; exceptions are grouped by kind and level (block,
// confirm, info), searched, paged and exported, and can be dealt with here
// one by one or a kind at a time (leave the tables out, or name an entity).
type Report = PlanReportPartsFragment
type Exception = Report['exceptions'][number]
type Change = Report['changes'][number]

const props = defineProps<{ report: Report; editable?: boolean }>()
const emit = defineEmits<{
  exclude: [refs: string[]]
  rename: [ref: string, name: string]
  drill: [change: Change]
  locate: [entity: string]
}>()
const { t } = useI18n()
const ws = useWorkspace()

// Exceptions only an edit of the entity (or what names it) resolves: a link
// leads there.
const fixable = new Set(['referenced', 'keyReferenced', 'rowColumnMissing'])

const short = (id?: string | null) => (id ? ws.entityIndex.shortName(id) || id : '')
const levelOrder = { block: 0, confirm: 1, info: 2 } as Record<string, number>
const levelType = { block: 'error', confirm: 'warning', info: 'default' } as const

function changeText(c: Change) {
  const n = c.entities.length
  switch (c.kind) {
    case 'import': return t('plan.change.import', { n })
    case 'createRole': return t('plan.change.createRole', { principal: c.principal })
    case 'grant': return t('plan.change.grant', {
      principal: c.principal, n, actions: c.actions.map((a) => t(`grants.actions.${a.toUpperCase()}`)).join('、'),
    })
    case 'exclude': return t('plan.change.exclude', { fields: c.fields, n })
    case 'mask': return t('plan.change.mask', { fields: c.fields, n, mask: t(`entity.masks.${c.mask}`, c.mask ?? '') })
    case 'syncAdd': return t('plan.change.syncAdd', { fields: c.fields, n })
    case 'syncRemove': return t('plan.change.syncRemove', { fields: c.fields, n })
    case 'removeTable': return t('plan.change.removeTable', { n })
    case 'syncKeys': return t('plan.change.syncKeys', { n })
    case 'syncKind': return t('plan.change.syncKind', { n })
  }
  return c.kind
}

const groups = computed(() => {
  const out = new Map<string, Exception[]>()
  for (const x of props.report.exceptions) out.set(`${x.level}:${x.kind}`, [...(out.get(`${x.level}:${x.kind}`) ?? []), x])
  return [...out].sort(([, a], [, b]) => levelOrder[a[0].level] - levelOrder[b[0].level])
})
const counts = computed(() => {
  const by = { block: 0, confirm: 0, info: 0 } as Record<string, number>
  for (const x of props.report.exceptions) by[x.level]++
  return by
})

// Per group: a search and a page.
const search = reactive<Record<string, string>>({})
const pages = reactive<Record<string, number>>({})
const pageSize = 10
const matching = (key: string, xs: Exception[]) => {
  const q = (search[key] ?? '').trim().toLowerCase()
  return q ? xs.filter((x) => [x.entity, x.ref, x.detail].some((v) => v?.toLowerCase().includes(q))) : xs
}
const shown = (key: string, xs: Exception[]) => {
  const page = pages[key] ?? 1
  return matching(key, xs).slice((page - 1) * pageSize, page * pageSize)
}
const refsOf = (xs: Exception[]) => [...new Set(xs.flatMap((x) => (x.ref ? [x.ref] : [])))]

const renaming = ref<Record<string, string>>({})
const unscanned = computed(() => props.report.unscanned.reduce((n, u) => n + u.schemas.length, 0))
const when = (at?: string | null) => (at ? new Date(at).toLocaleString() : '')

function exportExceptions() {
  download('plan-exceptions.csv', toCSV([
    ['level', 'kind', 'entity', 'table', 'detail'],
    ...props.report.exceptions.map((x) => [x.level, x.kind, x.entity ?? '', x.ref ?? '', x.detail ?? '']),
  ]))
}
</script>

<template>
  <div class="stack" style="gap: 12px">
    <n-text>
      {{ t('plan.matched', { n: report.matched }) }}<template v-if="report.excluded">
        {{ t('plan.excludedCount', { n: report.excluded }) }}</template>
      <template v-if="report.scannedFrom">
        {{ report.scannedFrom === report.scannedTo ? t('plan.scannedAt', { at: when(report.scannedFrom) })
          : t('plan.scannedRange', { from: when(report.scannedFrom), to: when(report.scannedTo) }) }}
      </template>
    </n-text>
    <n-alert v-if="unscanned" type="warning" :show-icon="false">
      {{ t('plan.unscanned', { n: unscanned }) }}
      <span class="mono">{{ report.unscanned.map((u) => `${u.datasource}: ${u.schemas.slice(0, 5).join(', ')}${u.schemas.length > 5 ? '…' : ''}`).join('；') }}</span>
    </n-alert>
    <div>
      <n-text strong>{{ t('plan.changes') }}</n-text>
      <n-text v-if="!report.changes.length" depth="3" class="block">{{ t('plan.noChanges') }}</n-text>
      <ul class="changes">
        <li v-for="(c, i) in report.changes" :key="i">
          {{ changeText(c) }}
          <n-button v-if="c.entities.length" text size="tiny" type="primary" @click="emit('drill', c)">{{ t('plan.drill') }}</n-button>
        </li>
      </ul>
    </div>
    <n-collapse v-if="report.objects.length">
      <n-collapse-item :title="t('plan.objects', { n: report.objects.length })" name="objects">
        <div v-for="o in report.objects" :key="o.datasource + o.schema" class="object mono">
          {{ o.datasource }}.{{ o.schema || '—' }}
          <n-text depth="3">{{ t('plan.objectCounts', { tables: o.tables, imported: o.imported, changed: o.changed }) }}</n-text>
        </div>
      </n-collapse-item>
    </n-collapse>
    <div v-if="report.exceptions.length">
      <div class="head">
        <n-text strong>{{ t('plan.exceptionsBy', counts) }}</n-text>
        <n-button text size="tiny" type="primary" @click="exportExceptions">{{ t('plan.export') }}</n-button>
      </div>
      <n-collapse class="exceptions" :default-expanded-names="groups.filter(([, xs]) => xs[0].level !== 'info').map(([k]) => k)">
        <n-collapse-item v-for="[key, xs] in groups" :key="key" :name="key">
          <template #header>
            <n-tag size="small" :bordered="false" :type="levelType[xs[0].level as 'block']">{{ t(`plan.level.${xs[0].level}`) }}</n-tag>
            <n-text class="kind">{{ t(`plan.exception.${xs[0].kind}.title`) }}</n-text>
            <n-text depth="3" class="count">{{ xs.length }}</n-text>
          </template>
          <template #header-extra>
            <n-button v-if="editable && refsOf(xs).length" size="tiny" @click.stop="emit('exclude', refsOf(xs))">
              {{ t('plan.excludeKind', { n: refsOf(xs).length }) }}
            </n-button>
          </template>
          <n-input v-if="xs.length > pageSize" v-model:value="search[key]" size="tiny" clearable class="find"
            :placeholder="t('plan.findException')" @update:value="pages[key] = 1" />
          <n-list size="small">
            <n-list-item v-for="(x, i) in shown(key, xs)" :key="i">
              <div class="item">
                <span>
                  <span v-if="x.entity" class="mono">{{ x.entity.includes(':') ? x.entity : short(x.entity) }}</span>
                  <n-text depth="3"> {{ t(`plan.exception.${x.kind}.${x.detail ? 'detail' : 'plain'}`,
                    { detail: reasonText(x.reasonDetail, x.detail ?? '') }) }}</n-text>
                </span>
                <n-space v-if="editable && (x.ref || x.entity)" :size="6" :wrap="false">
                  <n-button v-if="x.entity && fixable.has(x.kind)" size="tiny" @click="emit('locate', x.entity)">
                    {{ t('plan.locate') }}</n-button>
                  <template v-if="x.kind === 'renamed' && x.ref">
                    <n-input v-model:value="renaming[x.ref]" size="tiny" class="rename" :placeholder="x.detail ?? ''" />
                    <n-button size="tiny" :disabled="!renaming[x.ref]" @click="emit('rename', x.ref, renaming[x.ref])">
                      {{ t('plan.rename') }}
                    </n-button>
                  </template>
                  <n-button v-if="x.ref" size="tiny" @click="emit('exclude', [x.ref])">{{ t('plan.excludeTable') }}</n-button>
                </n-space>
              </div>
            </n-list-item>
          </n-list>
          <n-pagination v-if="matching(key, xs).length > pageSize" v-model:page="pages[key]" size="small" class="pager"
            :page-size="pageSize" :item-count="matching(key, xs).length" />
        </n-collapse-item>
      </n-collapse>
    </div>
  </div>
</template>

<style scoped>
.stack { display: flex; flex-direction: column; }
.changes { margin: 6px 0 0; padding-left: 20px; line-height: 1.8; }
.block { display: block; margin-top: 4px; }
.object { font-size: 12px; line-height: 1.8; }
.head { display: flex; justify-content: space-between; align-items: center; }
.exceptions { margin-top: 6px; }
.kind { margin-left: 8px; }
.count { margin-left: 8px; font-size: 12px; }
.find { width: 240px; margin-bottom: 4px; }
.pager { margin-top: 6px; }
.item { display: flex; justify-content: space-between; align-items: center; gap: 8px; font-size: 13px; }
.rename { width: 140px; }
</style>
