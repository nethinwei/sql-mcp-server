import { computed, ref, watch, type Ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { run } from '@/api/client'
import { WorkspaceQuery } from '@/api/ops'
import { revisionSections, useWorkspace, type Sections } from '@/stores/workspace'
import { useStatus } from '@/stores/status'

// The configuration a simulation runs against: the workspace (kept on the
// server, what was edited here written first) or the published revision
// (loaded here). The page takes its user,
// role, entity and field options, its requests and the version it reports
// from the same context, so what can be picked is what is simulated.

export interface SimulationConfig extends Pick<Sections, 'entities' | 'roles' | 'users'> {
  /** The revision the configuration is or is based on. */
  revision: string | null
  /** Sent with requests: the revision simulated; null simulates the workspace. */
  against: string | null
  /** Changes whenever what is simulated does. */
  key: string
  label: string
}

export function useSimulationConfig(source: Ref<'workspace' | 'published'>) {
  const { t } = useI18n()
  const ws = useWorkspace()
  const status = useStatus()
  const published = ref<(Sections & { id: string }) | null>(null)
  const loading = ref(false)
  const error = ref('')

  async function loadPublished() {
    if (!status.loaded) await status.refresh()
    const id = status.publishedId
    if (!id || published.value?.id === id) return
    loading.value = true
    try {
      const data = await run(WorkspaceQuery, { id })
      if (data.revision && status.publishedId === id) published.value = { id, ...revisionSections(data.revision) }
      error.value = ''
    } catch (e) {
      error.value = e instanceof Error ? e.message : String(e)
    } finally {
      loading.value = false
    }
  }
  watch([source, () => status.publishedId], () => {
    if (source.value === 'published') void loadPublished()
  }, { immediate: true })

  const config = computed<SimulationConfig | null>(() => {
    if (source.value === 'workspace') {
      return {
        revision: ws.baseId, entities: ws.entities, roles: ws.roles, users: ws.users, against: null,
        key: ws.contextKey,
        label: t(ws.dirty ? 'simulate.sourceWorkspace' : 'simulate.sourceWorkspaceClean', { id: ws.baseId ?? '' }),
      }
    }
    const p = published.value
    if (!p || p.id !== status.publishedId) return null
    return {
      revision: p.id, entities: p.entities, roles: p.roles, users: p.users, against: p.id, key: p.id,
      label: t('simulate.sourcePublished', { id: p.id }),
    }
  })
  return { config, loading, error }
}
