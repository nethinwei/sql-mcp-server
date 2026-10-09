import { describe, expect, it } from 'vitest'
import { formatPath, getAt, merge, same, setAt } from './merge'

describe('merge', () => {
  it('takes each side where only it changed', () => {
    const base = { name: 'r', description: 'a', grants: [] }
    expect(merge(base, { ...base, description: 'mine' }, { ...base, grants: [{ entity: 'x', actions: ['READ'] }] }))
      .toEqual({ value: { name: 'r', description: 'mine', grants: [{ entity: 'x', actions: ['READ'] }] }, conflicts: [] })
  })

  it('reports a value both changed differently and keeps mine', () => {
    const base = { cost: { maxRows: 10, softScore: 1 } }
    const m = merge(base, { cost: { maxRows: 20, softScore: 1 } }, { cost: { maxRows: 30, softScore: 5 } })
    expect(m.value).toEqual({ cost: { maxRows: 20, softScore: 5 } })
    expect(m.conflicts).toEqual([{ path: ['cost', 'maxRows'], mine: 20, theirs: 30 }])
  })

  it('merges grants by entity, several grants on one entity as one group', () => {
    const g = (entity: string, actions: string[]) => ({ entity, actions })
    const base = [g('a', ['READ']), g('a', ['AGGREGATE'])]
    const m = merge(base, [g('a', ['READ', 'CREATE']), g('a', ['AGGREGATE'])], [...base, g('b', ['READ'])])
    expect(m).toEqual({ value: [g('a', ['READ', 'CREATE']), g('a', ['AGGREGATE']), g('b', ['READ'])], conflicts: [] })
    // Removing one grant of the group on one side and changing another on the
    // other side is a conflict on the group, not a change to the wrong grant.
    expect(merge(base, [g('a', ['READ']), g('a', ['DELETE'])], [g('a', ['AGGREGATE'])]).conflicts)
      .toEqual([{ path: [{ element: 'a' }], mine: [g('a', ['READ']), g('a', ['DELETE'])], theirs: g('a', ['AGGREGATE']) }])
  })

  it('treats a removal against a change as a conflict', () => {
    expect(merge({ name: 'r', d: 1 }, undefined, { name: 'r', d: 2 }).conflicts)
      .toEqual([{ path: [], mine: undefined, theirs: { name: 'r', d: 2 } }])
    expect(merge({ name: 'r' }, undefined, { name: 'r' }).value).toBeUndefined()
  })

  it('ignores key order and unset optional text', () => {
    expect(same({ a: 1, description: '' }, { a: 1 })).toBe(true)
    expect(same({ a: 1, b: 2 }, { b: 2, a: 1 })).toBe(true)
  })
})

describe('paths', () => {
  const role = { fields: [{ name: 'id', mask: 'x' }] }
  it('reads, sets and removes along keyed lists', () => {
    const path = ['fields', { element: 'id' }, 'mask']
    expect(getAt(role, path)).toBe('x')
    expect(setAt(role, path, 'y')).toEqual({ fields: [{ name: 'id', mask: 'y' }] })
    expect(setAt(role, path, undefined)).toEqual({ fields: [{ name: 'id' }] })
    expect(setAt(role, ['fields', { element: 'email' }], { name: 'email' }))
      .toEqual({ fields: [{ name: 'id', mask: 'x' }, { name: 'email' }] })
    expect(formatPath(path)).toBe('fields[id].mask')
  })
})
