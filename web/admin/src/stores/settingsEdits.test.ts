import { beforeEach, describe, expect, it } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { useWorkspace } from './workspace'
import { useSettingsEdits } from './settingsEdits'

describe('settings edits', () => {
  beforeEach(() => setActivePinia(createPinia()))

  function setup() {
    const ws = useWorkspace()
    ws.setSettings({ cost: { maxRows: 10 }, cache: { enabled: false } })
    ws.baseline = JSON.stringify({ entities: [], roles: [], users: [], settings: ws.settings })
    const edits = useSettingsEdits()
    edits.sync()
    return { ws, edits }
  }

  it('puts valid JSON into the workspace as it is typed', () => {
    const { ws, edits } = setup()
    edits.edit('cost', '{"maxRows": 20}')
    expect(ws.settings.cost).toEqual({ maxRows: 20 })
    expect(ws.changes).toEqual([{ kind: 'settings', name: 'settings', type: 'modified' }])
    expect(edits.pending).toEqual([])
  })

  it('keeps invalid JSON pending and out of the workspace', () => {
    const { ws, edits } = setup()
    edits.edit('cache', '{oops')
    expect(edits.pending).toEqual(['cache'])
    expect(ws.settings.cache).toEqual({ enabled: false })
    ws.setSettings({ cost: { maxRows: 30 }, cache: { enabled: true } })
    edits.sync()
    expect(edits.texts.cache).toBe('{oops')
    expect(JSON.parse(edits.texts.cost)).toEqual({ maxRows: 30 })
  })

  it('keeps the user formatting when the value is unchanged and follows other changes', () => {
    const { ws, edits } = setup()
    edits.edit('cost', '{ "maxRows":   10 }')
    edits.sync()
    expect(edits.texts.cost).toBe('{ "maxRows":   10 }')
    ws.setSettings({ cost: { maxRows: 99 }, cache: { enabled: false } })
    edits.sync()
    expect(JSON.parse(edits.texts.cost)).toEqual({ maxRows: 99 })
    expect(edits.pending).toEqual([])
  })

  it('reverts a section to the base revision', () => {
    const { ws, edits } = setup()
    edits.edit('cost', '{"maxRows": 20}')
    edits.edit('cache', '{oops')
    edits.revert('cost')
    edits.revert('cache')
    expect(ws.settings).toEqual({ cost: { maxRows: 10 }, cache: { enabled: false } })
    expect(edits.pending).toEqual([])
    expect(ws.dirty).toBe(false)
  })
})
