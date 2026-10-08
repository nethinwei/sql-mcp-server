import { beforeEach, describe, expect, it } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { useWorkspace } from './workspace'
import { useSettingsEdits } from './settingsEdits'

describe('settings edits', () => {
  beforeEach(() => setActivePinia(createPinia()))

  function setup() {
    const ws = useWorkspace()
    ws.setSettings({ cost: { maxRows: 10 }, cache: { enabled: false } })
    const edits = useSettingsEdits()
    edits.sync()
    return { ws, edits }
  }

  it('keeps other sections being edited when one is applied', () => {
    const { ws, edits } = setup()
    edits.texts.cost = '{"maxRows": 20}'
    edits.texts.cache = '{"enabled": true}'
    expect(edits.pending).toEqual(['cost', 'cache'])
    expect(edits.apply(['cost'])).toBe(true)
    edits.sync()
    expect(ws.settings.cost).toEqual({ maxRows: 20 })
    expect(edits.texts.cache).toBe('{"enabled": true}')
    expect(edits.pending).toEqual(['cache'])
  })

  it('follows workspace changes only in sections not being edited', () => {
    const { ws, edits } = setup()
    edits.texts.cache = '{"enabled": true}'
    ws.setSettings({ cost: { maxRows: 30 }, cache: { enabled: false, ttl: '1m' } })
    edits.sync()
    expect(JSON.parse(edits.texts.cost)).toEqual({ maxRows: 30 })
    expect(edits.texts.cache).toBe('{"enabled": true}')
  })

  it('does not report unedited sections when the workspace moves on', () => {
    const { ws, edits } = setup()
    ws.setSettings({ cost: { maxRows: 99 }, cache: { enabled: true } })
    expect(edits.pending).toEqual([])
    edits.sync()
    expect(JSON.parse(edits.texts.cost)).toEqual({ maxRows: 99 })
  })

  it('applies nothing when any section is invalid', () => {
    const { ws, edits } = setup()
    edits.texts.cost = '{"maxRows": 20}'
    edits.texts.cache = '{oops'
    expect(edits.apply(['cost', 'cache'])).toBe(false)
    expect(ws.settings.cost).toEqual({ maxRows: 10 })
    expect(edits.errors.cache).toBeTruthy()
  })
})
