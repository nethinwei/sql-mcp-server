// Leaving the page loses what is not on the server yet: settings text not
// applied to the workspace, and workspace edits waiting to be sent, being
// sent or failed to send. Those edits are sent right away, in case the
// administrator stays; the browser asks before leaving either way.

export interface UnloadState {
  /** Signed out, or the session ended: nothing can be saved. */
  leaving: () => boolean
  workspaceUnsaved: () => boolean
  settingsUnsaved: () => boolean
  flush: () => Promise<unknown>
}

export function guardUnload(e: Pick<BeforeUnloadEvent, 'preventDefault'>, s: UnloadState): void {
  if (s.leaving()) return
  const workspace = s.workspaceUnsaved()
  if (workspace) void s.flush().catch(() => {})
  if (workspace || s.settingsUnsaved()) e.preventDefault()
}
