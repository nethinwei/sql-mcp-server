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
  it('renames clashing candidates and their relationship targets', () => {
    const out = uniqueCandidates([
      { name: 'users', schema: 'archive' },
      { name: 'orders', schema: 'archive', relationships: [{ name: 'users', target: 'users', cardinality: 'belongs-to', joinOn: {} }] },
    ], ['users'])
    expect(out.map((e) => e.name)).toEqual(['archive_users', 'orders'])
    expect(out[1].relationships![0].target).toBe('archive_users')
  })

  it('keeps same-named candidates of different schemas apart', () => {
    const rel = { name: 'users', target: 'users', cardinality: 'belongs-to', joinOn: {} }
    const out = uniqueCandidates([
      { name: 'users', schema: 'public' },
      { name: 'users', schema: 'archive' },
      { name: 'orders', schema: 'archive', relationships: [rel] },
      { name: 'carts', schema: 'public', relationships: [rel] },
    ], [])
    expect(out.map((e) => e.name)).toEqual(['users', 'archive_users', 'orders', 'carts'])
    expect(out[2].relationships![0].target).toBe('archive_users')
    expect(out[3].relationships![0].target).toBe('users')
  })
})
