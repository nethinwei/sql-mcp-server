import { describe, expect, it } from 'vitest'
import type { EntityInput } from '@/gql/schema'
import { schemaScans, syncPlan, syncScope } from './entitySync'

const entity = (name: string, extra: Partial<EntityInput> = {}) =>
  ({ name, fields: [], relationships: [], params: [], affects: [], ...extra }) as unknown as EntityInput

describe('syncScope', () => {
  it('leaves out procedures and unreachable datasources', () => {
    const scope = syncScope([
      entity('orders', { datasource: 'shop', schema: 'sales' }),
      entity('users', { datasource: 'shop' }),
      entity('refresh', { datasource: 'shop', kind: 'procedure' }),
      entity('logs', { datasource: 'down' }),
    ], new Set(['down']))
    expect(scope.ids).toEqual(['shop.sales.orders', 'shop.users'])
    expect(scope.procedures).toBe(1)
    expect(scope.unreachable).toEqual({ down: ['down.logs'] })
    expect(scope.schemas).toEqual({ shop: ['sales', ''] })
  })
})

describe('schemaScans', () => {
  it('marks schemas scanned over an hour ago, or never, as stale', () => {
    const now = Date.parse('2026-10-19T12:00:00Z')
    const scope = syncScope([
      entity('orders', { datasource: 'shop', schema: 'sales' }),
      entity('users', { datasource: 'shop' }),
      entity('items', { datasource: 'shop', schema: 'stock' }),
    ], new Set())
    const scans = schemaScans(scope, { shop: { defaultSchema: 'public', schemas: [
      { name: 'public', scannedAt: '2026-10-19T11:30:00Z' },
      { name: 'sales', scannedAt: '2026-10-19T09:00:00Z' },
    ] } }, now)
    expect(scans.map((s) => [s.schema, s.stale])).toEqual([['public', false], ['sales', true], ['stock', true]])
  })
})

describe('syncPlan', () => {
  it('syncs fields, keys and gone tables of the entities, new columns hidden', () => {
    expect(syncPlan(['shop.users'])).toEqual({
      scope: { entities: ['shop.users'] },
      sync: { add: true, remove: true, hideNew: true, removeTables: true, keys: true },
    })
  })
})
