import { beforeEach, describe, expect, it, vi } from 'vitest'
import { CancelSchemaScanMutation, SchemaScanQuery, SchemaSyncQuery, StartSchemaScanMutation } from '@/api/ops'

// The server is a map from document to answer.
const calls: [unknown, unknown][] = []
let answer = new Map<unknown, (vars: Record<string, unknown>) => unknown>()
vi.mock('@/api/client', () => ({
  run: (doc: unknown, vars: Record<string, unknown>) => {
    calls.push([doc, vars])
    try {
      return Promise.resolve(answer.get(doc)?.(vars) ?? {})
    } catch (e) {
      return Promise.reject(e)
    }
  },
}))

const { cancelScan, scanSchema, scansOf, sync, tablesBySchema } = await import('./schemaScans')

/** A sync answer: schemas of source, and the scanned tables with their candidate names. */
const synced = (source: string, schemas: string[], tables: [string, string][] = []) => ({
  schemaList: { source, defaultSchema: 'public', listedAt: '', schemas: schemas.map((name) => ({ name })) },
  schemaTables: {
    datasource: 'shop', source,
    tables: tables.map(([schema, candidate]) => ({ schema, table: 't', candidate: { name: candidate } })),
  },
})

beforeEach(() => {
  calls.length = 0
  answer = new Map()
})

describe('cancelScan', () => {
  it('cancels a scan canceled before the server answered its start', async () => {
    let started: (v: unknown) => void = () => {}
    answer.set(StartSchemaScanMutation, () => new Promise((resolve) => { started = resolve }))
    const n = { name: 'public', scannedAt: null, tables: null, scanning: false, job: null, error: null, checked: [], seq: 0 }
    const scanning = scanSchema('shop', n)
    await cancelScan(n)
    started({ startSchemaScan: { id: 'j1' } })
    await scanning
    expect(calls).toContainEqual([CancelSchemaScanMutation, { id: 'j1' }])
  })
})

describe('sync', () => {
  it('drops what was read once the datasource reads another database', async () => {
    answer.set(SchemaSyncQuery, () => synced('a', ['public'], [['public', 'users']]))
    await sync('moved')
    scansOf('moved').schemas[0].checked = ['public.t']
    answer.set(SchemaSyncQuery, () => synced('b', ['public']))
    await sync('moved')
    const s = scansOf('moved')
    expect(s.result?.tables).toEqual([])
    expect(s.schemas[0].checked).toEqual([])
  })

  it('shows only the latest sync: one started after the last scan sees every scan', async () => {
    const pending: ((v: unknown) => void)[] = []
    answer.set(SchemaSyncQuery, () => new Promise((resolve) => pending.push(resolve)))
    const first = sync('both')
    const second = sync('both')
    pending[1](synced('a', ['public', 'sales'], [['public', 'public_users'], ['sales', 'sales_users']]))
    await second
    pending[0](synced('a', ['public', 'sales'], [['public', 'users']]))
    await first
    const s = scansOf('both')
    expect(s.syncing).toBe(false)
    expect(tablesBySchema(s.result).get('public')?.[0].candidate.name).toBe('public_users')
  })

  it('keeps what it showed and the error after a failed sync, until a retry succeeds', async () => {
    answer.set(SchemaSyncQuery, () => synced('a', ['public'], [['public', 'users']]))
    await sync('flaky')
    answer.set(SchemaSyncQuery, () => { throw new Error('connection reset') })
    await sync('flaky')
    const s = scansOf('flaky')
    expect(s.error).toBe('connection reset')
    expect(s.result?.tables).toHaveLength(1)
    answer.set(SchemaSyncQuery, () => synced('a', ['public'], [['public', 'public_users']]))
    await sync('flaky')
    expect(s.error).toBeNull()
    expect(s.result?.tables[0].candidate.name).toBe('public_users')
  })

  it('syncs once a scan is done', async () => {
    answer.set(SchemaSyncQuery, () => synced('a', ['public']))
    await sync('scan')
    answer.set(StartSchemaScanMutation, () => ({ startSchemaScan: { id: 'j2' } }))
    answer.set(SchemaScanQuery, () => ({ schemaScan: { state: 'DONE' } }))
    answer.set(SchemaSyncQuery, () => synced('a', ['public'], [['public', 'users']]))
    const s = scansOf('scan')
    await scanSchema('scan', s.schemas[0])
    expect(s.schemas[0].scanning).toBe(false)
    expect(s.result?.tables).toHaveLength(1)
  })
})
