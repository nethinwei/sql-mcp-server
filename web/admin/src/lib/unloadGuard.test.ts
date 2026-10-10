import { describe, expect, it, vi } from 'vitest'
import { guardUnload, type UnloadState } from './unloadGuard'

function state(over: Partial<UnloadState> = {}): UnloadState {
  return {
    leaving: () => false, workspaceUnsaved: () => false, settingsUnsaved: () => false,
    flush: vi.fn(async () => {}), ...over,
  }
}

describe('guardUnload', () => {
  it('lets a saved page go', () => {
    const e = { preventDefault: vi.fn() }
    guardUnload(e, state())
    expect(e.preventDefault).not.toHaveBeenCalled()
  })
  it('sends unsent edits and asks first', () => {
    const e = { preventDefault: vi.fn() }
    const s = state({ workspaceUnsaved: () => true })
    guardUnload(e, s)
    expect(s.flush).toHaveBeenCalled()
    expect(e.preventDefault).toHaveBeenCalled()
  })
  it('asks for settings text not applied, without sending', () => {
    const e = { preventDefault: vi.fn() }
    const s = state({ settingsUnsaved: () => true })
    guardUnload(e, s)
    expect(s.flush).not.toHaveBeenCalled()
    expect(e.preventDefault).toHaveBeenCalled()
  })
  it('does not hold a page that is signing out', () => {
    const e = { preventDefault: vi.fn() }
    const s = state({ leaving: () => true, workspaceUnsaved: () => true })
    guardUnload(e, s)
    expect(s.flush).not.toHaveBeenCalled()
    expect(e.preventDefault).not.toHaveBeenCalled()
  })
  it('survives a failed send', async () => {
    const e = { preventDefault: vi.fn() }
    guardUnload(e, state({ workspaceUnsaved: () => true, flush: () => Promise.reject(new Error('offline')) }))
    await Promise.resolve()
    expect(e.preventDefault).toHaveBeenCalled()
  })
})
