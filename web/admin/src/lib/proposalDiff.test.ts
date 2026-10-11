import { describe, expect, it } from 'vitest'
import { EntityIndex } from './entityRefs'
import { fieldDiffs, grantDiffs } from './proposalDiff'

describe('proposal diffs', () => {
  const entities = [{ name: 'orders', datasource: 'shop', schema: 'sales' }, { name: 'logs', datasource: 'shop', schema: 'sales' }]
  const index = new EntityIndex(entities)
  const grant = (entity: string, actions: string[], fields?: string[]) => ({
    entity, actions: actions as never, fieldsRestricted: !!fields, readFields: fields ?? [], writeFields: [], rows: null,
  })

  it('shows a principal\'s grants on each entity before and after', () => {
    const workspace = { roles: [{ name: 'analyst', grants: [grant('orders', ['READ'], ['id'])] }], users: [] }
    const proposal = { roles: [{ name: 'analyst', grants: [grant('shop.sales.orders', ['READ'], ['id']),
      grant('shop.sales.orders', ['READ', 'UPDATE']), grant('shop.sales.logs', ['READ'])] }], users: [] }
    const diffs = grantDiffs('role:analyst', ['shop.sales.orders', 'shop.sales.logs'], index, workspace, proposal as never)
    expect(diffs[0].before).toEqual([{ actions: ['read'], readFields: ['id'], writeFields: [], rows: null }])
    expect(diffs[0].after.map((g) => g.actions)).toEqual([['read'], ['read', 'update']])
    expect(diffs[1]).toMatchObject({ before: [], after: [{ actions: ['read'], readFields: null }] })
  })

  it('lists the fields that differ', () => {
    expect(fieldDiffs(
      { name: 'o', fields: [{ name: 'id' }, { name: 'phone' }, { name: 'gone' }] },
      { name: 'o', fields: [{ name: 'id', exclude: false }, { name: 'phone', mask: 'phone' }, { name: 'new', exclude: true }] },
    ).map((d) => [d.field, d.before?.name ?? null, d.after?.mask ?? d.after?.exclude ?? null]))
      .toEqual([['phone', 'phone', 'phone'], ['gone', 'gone', null], ['new', null, true]])
  })
})
