import { describe, expect, it } from 'vitest'
import { setActionOn } from './grants'
import type { GrantInput } from '@/gql/graphql'

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
