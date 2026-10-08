<script setup lang="ts">
import { computed } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { NButton, NSpace, NTable, NTag, NText } from 'naive-ui'
import { isEmpty, type Access, type Summary } from '@/lib/changeSummary'
import { simulationRoute } from '@/lib/simulation'

// What a change does to people and data, before the YAML diff: access gained
// and lost per user, rescoped actions, boundary changes to verify by
// simulation, entities and field visibility.
const props = defineProps<{ summary: Summary }>()
const { t } = useI18n()
const router = useRouter()
/** Simulates a user against the workspace (which holds the changes under review). */
const simulate = (user: string) => router.push(simulationRoute({ who: user, source: 'workspace' }))
const empty = computed(() => isEmpty(props.summary))
const label = (a: Access) => `${a.entity} · ${t(`grants.actions.${a.action}`)}`
</script>

<template>
  <n-text v-if="empty" depth="3">{{ t('review.summaryEmpty') }}</n-text>
  <n-space v-else vertical :size="14">
    <div v-if="summary.users.length">
      <n-text strong>{{ t('review.usersTitle') }}</n-text>
      <n-table size="small" :single-line="false" class="table">
        <thead>
          <tr>
            <th>{{ t('review.user') }}</th>
            <th>{{ t('review.gained') }}</th>
            <th>{{ t('review.lost') }}</th>
            <th>{{ t('review.rescoped') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="u in summary.users" :key="u.user">
            <td>
              <n-button text type="primary" class="mono" :title="t('review.simulateUser')" @click="simulate(u.user)">
                {{ u.user }}
              </n-button>
            </td>
            <td><n-tag v-for="a in u.gained" :key="label(a)" size="small" type="success" :bordered="false" class="tag">+ {{ label(a) }}</n-tag></td>
            <td><n-tag v-for="a in u.lost" :key="label(a)" size="small" type="error" :bordered="false" class="tag">− {{ label(a) }}</n-tag></td>
            <td>
              <n-tag v-for="a in u.rescoped" :key="label(a)" size="small" type="warning" :bordered="false" class="tag">{{ label(a) }}</n-tag>
              <n-tag v-if="u.subjectChanged" size="small" type="warning" :bordered="false" class="tag pickable"
                @click="simulate(u.user)">{{ t('review.subjectChanged') }}</n-tag>
            </td>
          </tr>
        </tbody>
      </n-table>
    </div>
    <div v-if="summary.boundaries.length">
      <n-text strong>{{ t('review.boundariesTitle') }}</n-text>
      <div v-for="b in summary.boundaries" :key="b.entity + b.kind" class="line">
        <span class="mono entity">{{ b.entity }}</span>
        <n-tag size="small" type="warning" :bordered="false" class="tag">{{ t(`review.boundary.${b.kind}`) }}</n-tag>
        <n-text depth="3">{{ t('review.affects') }}</n-text>
        <n-tag v-for="u in b.users" :key="u" size="small" :bordered="false" class="tag pickable" @click="simulate(u)">{{ u }}</n-tag>
        <n-text v-if="!b.users.length" depth="3">{{ t('review.nobodyYet') }}</n-text>
      </div>
      <n-text depth="3" class="hint">{{ t('review.boundaryHint') }}</n-text>
    </div>
    <div v-if="summary.entitiesAdded.length || summary.entitiesRemoved.length">
      <n-text strong>{{ t('review.entitiesTitle') }}</n-text>
      <div class="line">
        <n-tag v-for="e in summary.entitiesAdded" :key="e" size="small" type="success" :bordered="false" class="tag">+ {{ e }}</n-tag>
        <n-tag v-for="e in summary.entitiesRemoved" :key="e" size="small" type="error" :bordered="false" class="tag">− {{ e }}</n-tag>
      </div>
    </div>
    <div v-if="summary.fields.length">
      <n-text strong>{{ t('review.fieldsTitle') }}</n-text>
      <div v-for="f in summary.fields" :key="f.entity" class="line">
        <span class="mono entity">{{ f.entity }}</span>
        <n-tag v-for="n in f.shown" :key="'s' + n" size="small" type="success" :bordered="false" class="tag">{{ t('review.shown') }} {{ n }}</n-tag>
        <n-tag v-for="n in f.hidden" :key="'h' + n" size="small" :bordered="false" class="tag">{{ t('review.hidden') }} {{ n }}</n-tag>
        <n-tag v-for="n in f.masked" :key="'m' + n" size="small" type="info" :bordered="false" class="tag">{{ t('review.masked') }} {{ n }}</n-tag>
        <n-tag v-for="n in f.unmasked" :key="'u' + n" size="small" type="warning" :bordered="false" class="tag">{{ t('review.unmasked') }} {{ n }}</n-tag>
      </div>
    </div>
    <n-text depth="3" class="hint">{{ t('review.summaryHint') }}</n-text>
  </n-space>
</template>

<style scoped>
.table { margin-top: 6px; }
.tag { margin: 2px 4px 2px 0; }
.line { margin-top: 6px; display: flex; flex-wrap: wrap; align-items: center; gap: 4px; }
.entity { margin-right: 6px; }
.hint { font-size: 12px; display: block; margin-top: 4px; }
.pickable { cursor: pointer; }
</style>
