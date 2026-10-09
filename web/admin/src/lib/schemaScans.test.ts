import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ScanBatchesQuery, SchemaListQuery, StartScanBatchMutation } from '@/api/ops'

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

const { batchState, followBatches, scans, scansOf, startBatch, sync } = await import('./schemaScans')

const listed = (source: string, schemas: string[], scanned: string[] = []) => ({
  schemaList: {
    source, defaultSchema: 'public', listedAt: '',
    schemas: schemas.map((name) => ({ name, scannedAt: scanned.includes(name) ? 'now' : null, tables: null })),
  },
})

const batch = (id: string, state: string, items: [string, string][]) => ({
  id, datasource: 'shop', state, total: items.length, startedBy: 'root', startedAt: '', finishedAt: null,
  done: items.filter(([, s]) => s === 'done').length, failed: items.filter(([, s]) => s === 'failed').length,
  canceled: 0, pending: items.filter(([, s]) => s === 'pending' || s === 'running').length,
  items: items.map(([schema, state]) => ({ schema, state, error: state === 'failed' ? 'boom' : null })),
})

beforeEach(() => {
  calls.length = 0
  answer = new Map()
})

describe('sync', () => {
  it('drops the list once the datasource reads another database', async () => {
    answer.set(SchemaListQuery, () => listed('a', ['public', 'sales']))
    await sync('moved')
    answer.set(SchemaListQuery, () => listed('b', ['public']))
    await sync('moved')
    expect(scansOf('moved').schemas.map((n) => n.name)).toEqual(['public'])
  })

  it('shows only the latest listing', async () => {
    const pending: ((v: unknown) => void)[] = []
    answer.set(SchemaListQuery, () => new Promise((resolve) => pending.push(resolve)))
    const first = sync('both')
    const second = sync('both')
    pending[1](listed('a', ['public', 'sales'], ['sales']))
    await second
    pending[0](listed('a', ['public']))
    await first
    const s = scansOf('both')
    expect(s.syncing).toBe(false)
    expect(s.schemas.map((n) => [n.name, n.scannedAt])).toEqual([['public', null], ['sales', 'now']])
  })

  it('keeps what it showed and the error after a failed listing, until a retry succeeds', async () => {
    answer.set(SchemaListQuery, () => listed('a', ['public']))
    await sync('flaky')
    answer.set(SchemaListQuery, () => { throw new Error('connection reset') })
    await sync('flaky')
    const s = scansOf('flaky')
    expect(s.error).toBe('connection reset')
    expect(s.schemas).toHaveLength(1)
  })
})

describe('batches', () => {
  it('follows a batch on the server until it ends, refreshing the catalog as schemas are scanned', async () => {
    const answers = [
      batch('b1', 'running', [['s1', 'done'], ['s2', 'running']]),
      batch('b1', 'done', [['s1', 'done'], ['s2', 'failed']]),
    ]
    answer.set(StartScanBatchMutation, () => ({ startScanBatch: batch('b1', 'running', [['s1', 'pending'], ['s2', 'pending']]) }))
    answer.set(ScanBatchesQuery, () => ({ scanBatches: [answers.shift() ?? answers[0]] }))
    answer.set(SchemaListQuery, () => listed('a', ['s1', 's2'], ['s1']))
    const version = scans.version
    await startBatch('shop', ['s1', 's2'])
    await vi.waitFor(() => expect(scansOf('shop').batches[0].state).toBe('done'), { timeout: 5000 })
    expect(scans.version).toBeGreaterThan(version)
    expect(batchState(scansOf('shop'), 's2')).toEqual({ state: 'failed', error: 'boom' })
    expect(scansOf('shop').schemas.find((n) => n.name === 's1')?.scannedAt).toBe('now')
  })

  it('reads the running batches again when the console comes back', async () => {
    answer.set(ScanBatchesQuery, () => ({ scanBatches: [batch('b9', 'done', [['x', 'done']])] }))
    answer.set(SchemaListQuery, () => listed('a', ['x'], ['x']))
    await followBatches('again')
    expect(scansOf('again').batches.map((b) => b.id)).toEqual(['b9'])
  })
})
