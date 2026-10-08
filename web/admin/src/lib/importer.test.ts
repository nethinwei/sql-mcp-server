import { describe, expect, it } from 'vitest'
import { readsTable, uniqueCandidates } from './importer'

describe('readsTable', () => {
  const tables = [{ schema: 'public', table: 'users' }, { schema: 'archive', table: 'users' }, { schema: 'public', table: 'orders' }]

  it('matches schema-qualified entities exactly', () => {
    const e = { name: 'users', schema: 'archive', datasource: 'shop' }
    expect(readsTable(e, 'shop', tables[1], tables)).toBe(true)
    expect(readsTable(e, 'shop', tables[0], tables)).toBe(false)
  })

  it('resolves an entity without schema in the default schema', () => {
    const e = { name: 'users', datasource: 'shop' }
    expect(readsTable(e, 'shop', tables[0], tables, 'public')).toBe(true)
    expect(readsTable(e, 'shop', tables[1], tables, 'public')).toBe(false)
  })

  it('without a default schema, matches an entity without schema only when the name is unique', () => {
    expect(readsTable({ name: 'users', datasource: 'shop' }, 'shop', tables[0], tables)).toBe(false)
    expect(readsTable({ name: 'orders', datasource: 'shop' }, 'shop', tables[2], tables)).toBe(true)
    expect(readsTable({ name: 'orders' }, 'shop', tables[2], tables)).toBe(false)
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
})
