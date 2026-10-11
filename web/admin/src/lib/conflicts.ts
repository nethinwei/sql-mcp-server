import { useI18n } from 'vue-i18n'
import type { Conflict } from '@/stores/workspace'

/**
 * Names the other side of conflicts: a newer published revision, a draft
 * taken into the workspace, or a write made to it meanwhile.
 */
export function useConflictSources() {
  const { t } = useI18n()
  const source = (c: Conflict) => {
    if (c.source === 'draft') return t('conflicts.fromDraft', { id: c.fromId })
    if (c.source === 'workspace') return t('conflicts.fromWorkspace', { by: c.fromBy ?? '' })
    return t('conflicts.fromPublished', { id: c.fromId })
  }
  const sources = (list: Conflict[]) => [...new Set(list.map(source))].join(t('common.listSep'))
  return { source, sources }
}
