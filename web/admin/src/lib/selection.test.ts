import { describe, expect, it } from 'vitest'
import {
  checkPage, confirm, emptySelection, filterKey, hiddenSelected, isSelected, needsConfirm, planOf, selectMatched,
  selectedCount, toCSV, withTotal,
} from './selection'
import { cleanPlan, filterOf, loadPlan, newPlan, patterns, scopeOf, tasksOf } from './plan'

describe('selection', () => {
  it('keeps what other pages selected', () => {
    let s = checkPage(emptySelection(), {}, ['a', 'b'], new Set(['a', 'b']))
    s = checkPage(s, {}, ['c', 'd'], new Set(['c', 'd']))
    s = checkPage(s, {}, ['c', 'd'], new Set(['d']))
    expect(s.mode === 'refs' && [...s.refs]).toEqual(['a', 'b', 'd'])
    expect(selectedCount(s)).toBe(3)
    expect(hiddenSelected(s, ['a', 'z'])).toEqual(['b', 'd'])
    expect(hiddenSelected(s, null)).toEqual([])
  })

  it('selects every match of a filter as a snapshot with exclusions', () => {
    const f = { schemas: ['pay_*'], status: ['NEW' as const] }
    let s = selectMatched(f, 5000)
    expect(isSelected(s, { status: ['NEW'], schemas: ['pay_*'] }, 'x')).toBe(true)
    s = checkPage(s, f, ['x', 'y'], new Set(['y']))
    expect(selectedCount(s)).toBe(4999)
    expect(isSelected(s, f, 'x')).toBe(false)
    // Seen through another filter, the snapshot cannot be told or adjusted.
    expect(isSelected(s, {}, 'y')).toBeNull()
    expect(checkPage(s, {}, ['y'], new Set())).toBe(s)
    expect(planOf(s)).toEqual({ scope: { schemas: ['pay_*'], status: ['new'] }, exclude: ['x'] })
    expect(planOf(checkPage(emptySelection(), {}, ['a'], new Set(['a'])))).toEqual({ scope: { refs: ['a'] } })
  })

  it('follows what the filter matches and asks to confirm a new count', () => {
    let s = selectMatched({ status: ['NEW'] }, 10)
    expect(needsConfirm(s)).toBe(false)
    expect(withTotal(s, 10)).toBe(s)
    s = withTotal(s, 14)
    expect(selectedCount(s)).toBe(14)
    expect(needsConfirm(s)).toBe(true)
    s = confirm(s)
    expect(needsConfirm(s)).toBe(false)
    expect(needsConfirm(confirm(emptySelection()))).toBe(false)
  })

  it('compares filters by what they select', () => {
    expect(filterKey({ schemas: ['b', 'a'], search: ' x ' })).toBe(filterKey({ schemas: ['a', 'b'], search: 'x', tables: [] }))
    expect(filterKey({ drift: true })).not.toBe(filterKey({}))
  })

  it('writes CSV', () => {
    expect(toCSV([['a', 'b"c'], [1, 2]])).toBe('"a","b""c"\n"1","2"\n')
  })
})

describe('plan', () => {
  it('maps a catalog filter to a scope and back', () => {
    const scope = scopeOf({ schemas: ['pay_*'], status: ['NEW', 'PUBLISHED'], search: ' order ', tables: [] })
    expect(scope).toEqual({ schemas: ['pay_*'], status: ['new', 'published'], search: 'order' })
    expect(filterOf(scope)).toMatchObject({ schemas: ['pay_*'], status: ['NEW', 'PUBLISHED'], search: 'order' })
  })

  it('leaves out empty parts', () => {
    expect(cleanPlan({
      scope: { refs: [] }, import: true, grants: [{ principal: 'role:a', actions: ['read'], fields: [] }],
      fields: [], sync: { add: false, remove: false, hideNew: true }, exclude: [], names: {},
    })).toEqual({ scope: {}, import: true, grants: [{ principal: 'role:a', actions: ['read'] }] })
  })

  it('loads a saved plan with its own meaning, not the defaults of a new one', () => {
    const saved = cleanPlan({ ...newPlan(), import: false, grants: [{ principal: 'role:a', actions: ['read'] }] })
    expect(saved.import).toBeUndefined()
    const loaded = loadPlan(saved)
    expect(loaded.import).toBe(false)
    expect(tasksOf(loaded)).toEqual(['grant'])
    expect(cleanPlan(loaded)).toEqual(saved)
    expect(newPlan().import).toBe(true)
  })

  it('splits patterns', () => {
    expect(patterns('pay_*, crm_*  ，log?')).toEqual(['pay_*', 'crm_*', 'log?'])
  })
})
