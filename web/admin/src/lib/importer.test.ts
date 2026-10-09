import { describe, expect, it } from 'vitest'
import { entityByTable, uniqueCandidates } from './importer'

describe('entityByTable', () => {
  const tables = [{ schema: 'public', table: 'users' }, { schema: 'archive', table: 'users' }, { schema: 'public', table: 'orders' }]
  const owner = (e: { name: string, schema?: string, datasource?: string }, defaultSchema?: string) =>
    [...entityByTable([e], 'shop', tables, defaultSchema).keys()]

  it('matches schema-qualified entities exactly', () => {
    expect(owner({ name: 'users', schema: 'archive', datasource: 'shop' })).toEqual(['archive.users'])
  })

  it('resolves an entity without schema in the default schema', () => {
    expect(owner({ name: 'users', datasource: 'shop' }, 'public')).toEqual(['public.users'])
  })

  it('without a default schema, matches an entity without schema only when the name is unique', () => {
    expect(owner({ name: 'users', datasource: 'shop' })).toEqual([])
    expect(owner({ name: 'orders', datasource: 'shop' })).toEqual(['public.orders'])
    expect(owner({ name: 'orders' })).toEqual([])
  })

  it('keeps the first entity reading a table, by source', () => {
    const first = { name: 'a', source: 'orders', datasource: 'shop' }
    const map = entityByTable([first, { name: 'orders', datasource: 'shop' }], 'shop', tables, 'public')
    expect(map.get('public.orders')).toBe(first)
  })
})

describe('uniqueCandidates', () => {
  const rel = (target: string) => ({ name: 'users', target, cardinality: 'belongs-to', joinOn: {} })

  it('numbers candidates whose IDs are taken and moves relationships to them', () => {
    const out = uniqueCandidates([
      { name: 'users', datasource: 'shop', schema: 'archive' },
      { name: 'orders', datasource: 'shop', schema: 'archive', relationships: [rel('shop.archive.users')] },
    ], ['shop.archive.users'])
    expect(out.map((e) => [e.name, e.source])).toEqual([['users_2', 'users'], ['orders', undefined]])
    expect(out[1].relationships![0].target).toBe('shop.archive.users_2')
  })

  it('keeps same-named tables of other schemas and datasources as they are', () => {
    const out = uniqueCandidates([
      { name: 'users', datasource: 'shop', schema: 'public' },
      { name: 'users', datasource: 'shop', schema: 'archive' },
      { name: 'users', datasource: 'warehouse' },
    ], ['shop.crm.users'])
    expect(out.map((e) => e.name)).toEqual(['users', 'users', 'users'])
  })
})
