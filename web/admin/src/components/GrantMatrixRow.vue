<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { NButton, NCheckbox, NIcon, NSpace, NTag, NTooltip } from 'naive-ui'
import { AddOutline, TrashOutline } from '@vicons/ionicons5'
import { describeFilter, type Filter } from '@/lib/filter'
import { sourceIfRenamed } from '@/lib/names'
import { applicable, capabilityOf, grantable, type CapabilityIndex } from '@/lib/capabilities'
import type { GrantRow } from '@/lib/grants'
import type { Action } from '@/gql/graphql'

// One row of GrantMatrix: an entity's grant (or none). A row of its own so
// that toggling a grant renders only the rows it changed (the matrix keeps
// the others' Row objects); events carry the row back.
const props = defineProps<{ row: GrantRow; caps: CapabilityIndex; readonly?: boolean }>()
const emit = defineEmits<{
  toggle: [row: GrantRow, action: Action, on: boolean]
  toggleRow: [row: GrantRow, on: boolean]
  fields: [row: GrantRow]
  rows: [row: GrantRow]
  add: [row: GrantRow]
  remove: [row: GrantRow]
}>()
const { t } = useI18n()
const actions: Action[] = ['READ', 'AGGREGATE', 'CREATE', 'UPDATE', 'DELETE', 'EXECUTE']

const can = (a: Action) => applicable(props.row.entity.kind, a)
const cap = (a: Action) => capabilityOf(props.caps, props.row.id, a)
const allowed = (a: Action) => grantable(props.caps, props.row.id, props.row.entity.kind, a)
const has = (a: Action) => props.row.grant?.actions.includes(a) ?? false

function capabilityHint(a: Action) {
  const c = cap(a)
  if (!c) return ''
  if (c.privilege === 'DENIED') return t('grants.capDenied', { connection: c.connection, reason: c.reason ?? '' })
  if (c.privilege === 'UNKNOWN') return t('grants.capUnknown', { connection: c.connection })
  if (c.columns?.length) return t('grants.capColumns', { connection: c.connection, columns: c.columns.join(', ') })
  return ''
}

function rowState() {
  const all = actions.filter(allowed)
  const n = all.filter(has).length
  return { checked: n > 0 && n === all.length, partial: n > 0 && n < all.length }
}

function fieldsLabel() {
  const g = props.row.grant
  if (!g?.fieldsRestricted) return t('grants.allFields')
  const writes = g.actions.some((a) => a === 'CREATE' || a === 'UPDATE')
  const total = (props.row.entity.fields ?? []).filter((f) => !f.exclude).length
  return t('grants.readCount', { n: g.readFields?.length ?? 0, total }) +
    (writes ? t('grants.writeCount', { n: g.writeFields?.length ?? 0 }) : '')
}
</script>

<template>
  <tr :class="{ granted: row.grant, sub: row.sub > 0 }">
    <td class="name">
      <template v-if="row.sub === 0">
        <router-link :to="{ name: 'entity', params: { id: row.id } }" class="entity mono">
          {{ row.entity.name }}
        </router-link>
        <span v-if="sourceIfRenamed(row.entity)" class="loc mono">← {{ sourceIfRenamed(row.entity) }}</span>
        <div v-if="row.entity.description" class="desc" :title="row.entity.description">
          {{ row.entity.description }}
        </div>
      </template>
      <n-tag v-else size="tiny" :bordered="false">{{ t('grants.another', { n: row.sub + 1 }) }}</n-tag>
    </td>
    <td class="act all">
      <n-checkbox :disabled="readonly" :checked="rowState().checked" :indeterminate="rowState().partial"
        :aria-label="t('grants.allRow')" :title="t('grants.allRow')"
        @update:checked="(on: boolean) => emit('toggleRow', row, on)" />
    </td>
    <td v-for="a in actions" :key="a" class="act"
      :class="{ denied: can(a) && !allowed(a), unknown: cap(a)?.privilege === 'UNKNOWN' }">
      <n-tooltip v-if="can(a)" :disabled="!capabilityHint(a)">
        <template #trigger>
          <n-checkbox :checked="has(a)" :disabled="readonly || (!allowed(a) && !has(a))"
            @update:checked="(on: boolean) => emit('toggle', row, a, on)" />
        </template>
        {{ capabilityHint(a) }}
      </n-tooltip>
    </td>
    <td class="scope">
      <n-button v-if="row.grant" size="tiny" :type="row.grant.fieldsRestricted ? 'info' : 'default'"
        secondary :disabled="readonly" @click="emit('fields', row)">
        {{ fieldsLabel() }}
      </n-button>
    </td>
    <td class="scope">
      <n-button v-if="row.grant" size="tiny" :type="row.grant.rows ? 'info' : 'default'" secondary
        :disabled="readonly" :title="describeFilter(row.grant.rows as Filter)" @click="emit('rows', row)">
        <span class="rows-text">{{ describeFilter(row.grant.rows as Filter) }}</span>
      </n-button>
    </td>
    <td class="ops">
      <n-space v-if="row.grant && !readonly" :size="2" :wrap="false">
        <n-tooltip>
          <template #trigger>
            <n-button size="tiny" quaternary circle :aria-label="t('grants.addAnother')" @click="emit('add', row)">
              <template #icon><n-icon><add-outline /></n-icon></template>
            </n-button>
          </template>
          {{ t('grants.addAnother') }}
        </n-tooltip>
        <n-button size="tiny" quaternary circle type="error" :aria-label="t('common.remove')" @click="emit('remove', row)">
          <template #icon><n-icon><trash-outline /></n-icon></template>
        </n-button>
      </n-space>
    </td>
  </tr>
</template>

<style scoped>
td { padding: 8px 10px; border-bottom: 1px solid rgba(128,128,128,.16); text-align: left; vertical-align: middle; }
.act { text-align: center; width: 56px; }
td.act.denied { background: rgba(208, 48, 80, .08); }
td.act.unknown { background: rgba(240, 160, 32, .08); }
.act.all { border-right: 1px solid rgba(128,128,128,.16); }
.name { min-width: 200px; }
.scope { white-space: nowrap; }
.ops { width: 70px; }
tr.granted td.name .entity { font-weight: 600; }
.entity { color: inherit; text-decoration: none; }
.entity:hover { color: #2f6fed; }
.loc { font-size: 11px; opacity: .55; margin-left: 6px; }
.desc { font-size: 12px; opacity: .6; max-width: 280px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; margin-top: 2px; }
.rows-text { display: inline-block; max-width: 220px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; vertical-align: bottom; }
</style>
