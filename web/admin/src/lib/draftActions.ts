import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { useDialog, useMessage } from 'naive-ui'
import { run } from '@/api/client'
import { DiscardDraftMutation } from '@/api/ops'
import { draftsChanged } from '@/lib/draftFeed'
import { workspace } from '@/lib/workspaceSync'
import { itemsChanged, useWorkspace, WorkspaceSwitched, type Sections } from '@/stores/workspace'

/**
 * What a console does with a draft saved elsewhere: take it into the
 * workspace as unsaved changes, as if made here (each kept, edited or
 * reverted, then published at Review), or discard it.
 */
export function useDraftActions() {
  const { t } = useI18n()
  const router = useRouter()
  const message = useMessage()
  const dialog = useDialog()
  const ws = useWorkspace()

  /**
   * Takes draft id into the workspace on the server, keeping its unpublished
   * changes; goes to Review when asked or when something conflicts.
   */
  async function review(id: string, opts: { go?: boolean } = {}) {
    try {
      const sectionsNow = () => JSON.parse(ws.snapshot().sections) as Sections
      const before = sectionsNow()
      const had = ws.openConflicts.length
      await workspace.mergeDraft(id)
      const conflicts = ws.openConflicts.length - had
      const changed = itemsChanged(before, sectionsNow())
      if (conflicts > 0) message.warning(t('drafts.conflicts', { id, n: conflicts }))
      else if (!changed) message.info(t('drafts.alreadyIn', { id }))
      else message.success(t('drafts.taken', { id, n: changed }))
      if (opts.go || conflicts > 0) void router.push({ name: 'review' })
    } catch (e) {
      if (!(e instanceof WorkspaceSwitched)) message.error(e instanceof Error ? e.message : String(e))
    }
  }

  /** Discards draft id, after asking. */
  function discard(id: string) {
    dialog.warning({
      title: t('drafts.discardTitle', { id }),
      content: t('drafts.discardBody'),
      positiveText: t('drafts.discard'),
      negativeText: t('common.cancel'),
      onPositiveClick: async () => {
        try {
          await run(DiscardDraftMutation, { id }, true)
          draftsChanged.value++
          message.success(t('drafts.discarded', { id }))
        } catch (e) {
          message.error(e instanceof Error ? e.message : String(e))
        }
      },
    })
  }

  return { review, discard }
}
