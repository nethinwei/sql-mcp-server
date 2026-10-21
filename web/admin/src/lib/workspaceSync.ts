import { h, onScopeDispose, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { NButton, useMessage, useNotification } from 'naive-ui'
import type { TypedDocumentNode } from '@urql/vue'
import { run } from '@/api/client'
import {
  CheckpointWorkspaceMutation, CommitWorkspaceMutation, EditWorkspaceMutation, FieldReferencesQuery, LoadRevisionMutation,
  MergeDraftMutation, ResetWorkspaceMutation, ResolveConflictMutation, UpdateWorkspaceMutation, WorkspaceQuery, WorkspaceStateQuery,
} from '@/api/ops'
import { can, session } from '@/api/session'
import type { CommitInput, WorkspacePartsFragment } from '@/gql/graphql'
import { currentLifetime, owned, serially, useWorkspace, WorkspaceSwitched } from '@/stores/workspace'

/**
 * Where saving the workspace on the server stands: synced (the server keeps
 * what is here), pending (edits wait to be sent), saving, or failed (the
 * last attempt failed; it is tried again, sooner once the server answers a
 * read).
 */
export type SaveState = 'synced' | 'pending' | 'saving' | 'failed'
export const saveState = ref<SaveState>('synced')

const retryDelays = [2000, 5000, 15000, 30000]

/** The signed-in console's sync (see useWorkspaceSync). */
let active: ReturnType<typeof createSync> | null = null

/**
 * The version of the workspace this console holds, to be answered only the
 * items changed since; null (every item) before it holds one.
 */
const since = () => {
  const ws = useWorkspace()
  return ws.copy ? ws.version : null
}

/**
 * Runs op (a request on the server workspace) for the administrator signed
 * in now: none is made, and WorkspaceSwitched thrown, once another signed in.
 */
function operation(op: () => Promise<WorkspacePartsFragment>) {
  const at = currentLifetime()
  return active ? active.operate(op, at) : Promise.reject(new WorkspaceSwitched())
}

/**
 * The workspace's operations on the server, for any page: each writes what
 * was edited here first and takes in the workspace the server answers, all
 * for the administrator signed in when it was asked (see operation).
 */
export const workspace = {
  /** Reads the workspace again, after something else wrote it on the server. */
  refresh: () => active?.pull() ?? Promise.resolve(true),
  /** Writes what was edited here. */
  flush: () => active?.push() ?? Promise.resolve(),
  /**
   * Runs a query on the workspace kept on the server (catalog, plans,
   * validation, simulation): what was edited here is written first, so the
   * answer covers it.
   */
  read: async <Data, Vars extends object>(doc: TypedDocumentNode<Data, Vars>, variables: Vars) => {
    await active?.push()
    return run(doc, variables)
  },
  /** Where the workspace names the fields of an entity (by its ID). */
  fieldReferences: async (entity: string) =>
    (await workspace.read(FieldReferencesQuery, { entity })).fieldReferences,
  /** Whether edits made here are not on the server yet (waiting, being sent, or failed to send). */
  unsaved: () => active?.unsent() ?? false,
  /**
   * Follows a newly published revision to: moves the workspace onto it, or
   * for an administrator who cannot write, reads it again (an unwritten
   * workspace is the published revision). Whether the workspace is on to.
   */
  follow: async (to: string) => {
    if (can('admin:write')) await workspace.update(to)
    else await workspace.refresh()
    return useWorkspace().basePublished === to
  },
  /** Moves the workspace onto revision to, re-applying what was changed in it. */
  update: (to: string) => operation(async () => (await run(UpdateWorkspaceMutation, { to, since: since() }, true)).updateWorkspace),
  /** Makes the workspace revision to as it is, dropping what was changed in it. */
  reset: (to: string) => operation(async () => (await run(ResetWorkspaceMutation, { to, since: since() }, true)).resetWorkspace),
  /**
   * Makes the content of revision to (any revision) the workspace, as changes
   * to the published revision; keep re-applies what was changed here onto it.
   */
  load: (to: string, keep: boolean) =>
    operation(async () => (await run(LoadRevisionMutation, { to, keep, since: since() }, true)).loadRevision),
  /** Takes draft id into the workspace. */
  mergeDraft: (id: string) => operation(async () => (await run(MergeDraftMutation, { id, since: since() }, true)).mergeDraft),
  /** Resolves a conflict: theirs takes the other side's value. */
  resolve: (id: string, theirs: boolean) =>
    operation(async () => (await run(ResolveConflictMutation, { id, theirs, since: since() }, true)).resolveConflict),
  /** Saves the workspace as a draft to share; returns its ID. */
  checkpoint: async (comment: string | null) => {
    const at = currentLifetime()
    await active?.push()
    const { checkpointWorkspace } = await owned(() => run(CheckpointWorkspaceMutation, { comment }, true), at)
    await owned(() => active?.pull() ?? Promise.resolve(true), at)
    return checkpointWorkspace.id
  },
  /** Publishes the workspace at the version reviewed; returns the revision. */
  commit: async (input: CommitInput) => {
    const at = currentLifetime()
    const { commitWorkspace } = await owned(() => run(CommitWorkspaceMutation, { input }, true), at)
    await owned(() => active?.pull() ?? Promise.resolve(true), at)
    return commitWorkspace.id
  },
}

/**
 * Runs a workspace operation from a page: an error is shown, except that
 * another administrator signed in meanwhile (nothing was done, and nothing
 * is told to them). The operation's result, or undefined when it failed.
 */
export function useWorkspaceAction() {
  const message = useMessage()
  return async <T>(fn: () => Promise<T>): Promise<T | undefined> => {
    try {
      return await fn()
    } catch (e) {
      if (!(e instanceof WorkspaceSwitched)) message.error(e instanceof Error ? e.message : String(e))
      return undefined
    }
  }
}

/**
 * Keeps this console's copy of the workspace in step with the one kept on
 * the server, the one copy the administrator (in any console) and their
 * agents write, like a working tree: each edit here is written as the items
 * it changed, with the values they were edited from, so the server merges
 * what was written there meanwhile (a value both changed differently
 * becomes a conflict, keeping this write's); what changed there is read
 * again on the server's signal.
 */
export function useWorkspaceSync() {
  const sync = createSync()
  active = sync
  onScopeDispose(() => {
    sync.stop()
    if (active === sync) active = null
  })
  return sync
}

function createSync() {
  const ws = useWorkspace()
  const { t } = useI18n()
  const notification = useNotification()
  const router = useRouter()
  let timer: ReturnType<typeof setTimeout> | undefined
  let failures = 0
  // This sync is the signed-in administrator's: once they sign out (the
  // layout goes) or the workspace is another's, it neither reads nor writes.
  const owner = session.username
  let stopped = false
  const live = () => !stopped && ws.owner === owner && session.username === owner
  const unsent = () => ws.copy !== null && ws.unsavedItems().length > 0

  /** Takes in the workspace as the server answered it, telling of what an agent wrote. */
  async function takeIn(w: WorkspacePartsFragment) {
    if (!w.items.complete && (!ws.copy || w.base !== ws.baseId)) {
      // Changed since a version this console no longer holds: read it whole.
      const { workspace } = await run(WorkspaceStateQuery, { since: null })
      if (!workspace || !live()) return
      w = workspace
    }
    const baseData = w.base !== ws.baseId ? await run(WorkspaceQuery, { id: w.base }) : undefined
    if (!live()) return
    const was = ws.copy ? { version: ws.version, notes: ws.notes } : null
    ws.take(w, baseData)
    if (!was || w.version === was.version || !w.updatedBy.startsWith('agent:')) return
    const notes = w.notes.filter((n) => !was.notes.some((b) => b.source === n.source && b.by === n.by && b.text === n.text))
    announce(w.updatedBy.slice('agent:'.length), notes.map((n) => n.text).join('\n'))
  }

  /** Reads the workspace kept on the server; false when nothing is published. */
  function pull(): Promise<boolean> {
    return serially(async () => {
      if (!live()) return true
      const { workspace: w } = await run(WorkspaceStateQuery, { since: since() })
      if (!live()) return true
      if (!w) return false
      await takeIn(w)
      // The server answered: what failed to save, or is still unsent, goes now.
      if (unsent()) schedulePush(0)
      return true
    })
  }

  /** Writes what was edited here. */
  function push(): Promise<void> {
    return serially(async () => {
      if (!live() || !unsent()) return
      saveState.value = 'saving'
      const items = ws.unsavedItems()
      try {
        const { editWorkspace } = await run(EditWorkspaceMutation, { input: { items }, since: since() }, true)
        if (!live()) return
        ws.wrote(items)
        await takeIn(editWorkspace)
      } catch (e) {
        saveState.value = 'failed'
        throw e
      }
      saveState.value = unsent() ? 'pending' : 'synced'
    })
  }

  /**
   * Runs an operation on the server workspace, after writing what was edited
   * here, while the workspace is still the one of lifetime at: until its
   * answer is taken in (which may read the base revision), else
   * WorkspaceSwitched, so the caller does not go on for another
   * administrator.
   */
  async function operate(op: () => Promise<WorkspacePartsFragment>, at: number) {
    await push()
    await serially(async () => {
      if (!live()) throw new WorkspaceSwitched()
      const w = await owned(op, at)
      await owned(async () => {
        if (!live()) throw new WorkspaceSwitched()
        await takeIn(w)
        if (!live()) throw new WorkspaceSwitched()
      }, at)
    })
  }

  /** Sends the workspace after delay; a failure is tried again later. */
  function schedulePush(delay = 300) {
    clearTimeout(timer)
    if (!live()) return
    if (!unsent()) {
      if (saveState.value !== 'saving') saveState.value = 'synced'
      return
    }
    if (saveState.value !== 'failed') saveState.value = 'pending'
    timer = setTimeout(() => void push().then(() => {
      failures = 0
      if (unsent()) schedulePush()
    }, () => schedulePush(retryDelays[Math.min(failures++, retryDelays.length - 1)])), delay)
  }

  /** Tells that an agent wrote the workspace, with what it said. */
  function announce(agent: string, text: string) {
    const n = notification.info({
      title: t('workspaceSync.written', { agent }),
      content: text || t('common.noDescription'),
      duration: 10000,
      action: () => h(NButton, { size: 'small', type: 'primary', onClick: () => {
        n.destroy()
        void router.push({ name: 'review' })
      } }, () => t('workspaceSync.seeChanges')),
    })
  }

  function stop() {
    stopped = true
    clearTimeout(timer)
  }

  return { pull, push, schedulePush, operate, stop, unsent }
}
