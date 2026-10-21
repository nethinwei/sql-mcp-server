import { describe, expect, it } from 'vitest'
import { locateGrant, setActionOn } from './grants'
import type { GrantInput } from '@/gql/schema'
import type { EntityIndex } from './entityRefs'

const base: GrantInput[] = [
  { entity: 'orders', actions: ['READ'], rows: { field: 'region', op: 'eq', value: 'CN' } },
  { entity: 'orders', actions: ['READ', 'AGGREGATE'], fieldsRestricted: true, readFields: ['id'] },
]

describe('setActionOn', () => {
  it('extends the first grant and adds grants for ungranted entities', () => {
    const out = setActionOn(base, ['orders', 'refunds'], 'DELETE', true)
    expect(out[0]).toEqual({ ...base[0], actions: ['READ', 'DELETE'] })
    expect(out[1]).toEqual(base[1])
    expect(out[2]).toEqual({ entity: 'refunds', actions: ['DELETE'] })
    expect(base[0].actions).toEqual(['READ'])
  })

  it('leaves entities that already have the action unchanged', () => {
    expect(setActionOn(base, ['orders'], 'READ', true)).toEqual(base)
  })

  it('revokes from every grant and drops empty grants', () => {
    const out = setActionOn(base, ['orders'], 'READ', false)
    expect(out).toEqual([{ ...base[1], actions: ['AGGREGATE'] }])
  })
})

describe('setActionOn with an entity index', () => {
  it('resolves each grant once and keeps the grants in order', () => {
    const resolved: string[] = []
    const index = {
      idOf: (ref: string) => (resolved.push(ref), `pg.${ref}`),
      shortName: (id: string) => id.slice(3),
    } as unknown as EntityIndex
    const grants: GrantInput[] = [
      { entity: 'b', actions: ['READ'] }, { entity: 'a', actions: ['READ'] }, { entity: 'b', actions: ['DELETE'] },
    ]
    const entities = ['pg.a', 'pg.b', 'pg.c', 'pg.d']
    const out = setActionOn(grants, entities, 'DELETE', true, index)
    expect(resolved).toHaveLength(grants.length)
    expect(out).toEqual([
      { entity: 'b', actions: ['READ'] }, { entity: 'a', actions: ['READ', 'DELETE'] },
      { entity: 'b', actions: ['DELETE'] }, { entity: 'c', actions: ['DELETE'] }, { entity: 'd', actions: ['DELETE'] },
    ])
    expect(setActionOn(out, entities, 'DELETE', false, index)).toEqual([
      { entity: 'b', actions: ['READ'] }, { entity: 'a', actions: ['READ'] },
    ])
  })
})

describe('locateGrant', () => {
  it('finds the grant itself, or its only equal copy after the list was replaced', () => {
    expect(locateGrant(base, base[1])).toBe(1)
    const reloaded = JSON.parse(JSON.stringify([base[1], base[0]])) as GrantInput[]
    expect(locateGrant(reloaded, base[0])).toBe(1)
    expect(locateGrant(reloaded, base[1])).toBe(0)
  })

  it('finds nothing when the grant is gone, changed or ambiguous', () => {
    expect(locateGrant([base[1]], base[0])).toBe(-1)
    expect(locateGrant([{ ...base[0], actions: ['READ', 'DELETE'] }], base[0])).toBe(-1)
    const twice = JSON.parse(JSON.stringify([base[0], base[0]])) as GrantInput[]
    expect(locateGrant(twice, base[0])).toBe(-1)
  })
})
