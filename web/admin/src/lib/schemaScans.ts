import { reactive } from 'vue'
import { run } from '@/api/client'
import {
  CancelScanBatchMutation, RetryScanBatchMutation, ScanBatchesQuery, SchemaListQuery, StartScanBatchMutation,
} from '@/api/ops'
import type { ImportTablePartsFragment, ScanBatchPartsFragment } from '@/gql/graphql'

// Scanning goes schema by schema, in batches the server runs a few at a time:
// a batch goes on when the console is closed or reloaded, which reads the
// batches again and follows them. The server keeps each schema's last scan in
// its catalog, which the page searches a page at a time (see CatalogQuery);
// `scans.version` changes whenever a schema is scanned, so a view of the
// catalog reads it again. The schema list belongs to the database the
// datasource reads (its source): when the datasource is pointed elsewhere,
// the list is dropped.

export type Table = ImportTablePartsFragment
export type ScanBatch = ScanBatchPartsFragment

export interface SchemaNode {
  /** '' when the datasource cannot list schemas: its default one. */
  name: string
  scannedAt: string | null
  tables: number | null
}

export interface DatasourceScans {
  /** The source the schemas were listed from. */
  source: string | null
  listed: boolean
  defaultSchema: string | null
  schemas: SchemaNode[]
  syncing: boolean
  /** Why the latest listing failed: what is shown may be stale. */
  error: string | null
  /** Bumped per listing; only the latest one's answer is shown. */
  seq: number
  /** Recent batches, running ones first. */
  batches: ScanBatch[]
}

export const scans = reactive<{ by: Record<string, DatasourceScans>; version: number }>({ by: {}, version: 0 })

export function scansOf(ds: string): DatasourceScans {
  scans.by[ds] ??= {
    source: null, listed: false, defaultSchema: null, schemas: [], syncing: false, error: null, seq: 0, batches: [],
  }
  return scans.by[ds]
}

const message = (e: unknown) => (e instanceof Error ? e.message : String(e))

/** Reads the schema list of ds (from the database with refresh). */
export async function sync(ds: string, refresh = false) {
  const s = scansOf(ds)
  const mine = ++s.seq
  s.syncing = true
  s.error = null
  try {
    const { schemaList } = await run(SchemaListQuery, { datasource: ds, refresh })
    if (mine !== s.seq) return
    if (schemaList.source !== s.source) Object.assign(s, { source: schemaList.source, schemas: [] })
    s.schemas = schemaList.schemas.map((x) => ({ name: x.name, scannedAt: x.scannedAt ?? null, tables: x.tables ?? null }))
    s.defaultSchema = schemaList.defaultSchema ?? null
    s.listed = true
  } catch (e) {
    if (mine === s.seq) s.error = message(e)
  } finally {
    if (mine === s.seq) s.syncing = false
  }
}

/** The latest state of a schema in the batches: running, failed, ... */
export function batchState(s: DatasourceScans, schema: string): { state: string; error: string | null } | null {
  for (const b of s.batches) {
    const item = b.items.find((i) => i.schema === schema)
    if (item) return { state: item.state, error: item.error ?? null }
  }
  return null
}

const running = (s: DatasourceScans) => s.batches.some((b) => b.state === 'running')
const doneCount = (s: DatasourceScans) => s.batches.reduce((n, b) => n + b.done, 0)
const polling = new Set<string>()
const sleep = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms))

/**
 * Reads the batches of ds, and follows them while one runs: each schema
 * scanned refreshes the schema list and the catalog.
 */
export async function followBatches(ds: string, interval = 1000) {
  const s = scansOf(ds)
  if (polling.has(ds)) return
  polling.add(ds)
  try {
    for (let first = true; first || running(s); first = false) {
      if (!first) await sleep(interval)
      const before = doneCount(s)
      s.batches = (await run(ScanBatchesQuery, { datasource: ds })).scanBatches
      if (doneCount(s) !== before) {
        await sync(ds)
        scans.version++
      }
    }
  } catch (e) {
    s.error = message(e)
  } finally {
    polling.delete(ds)
  }
}

/** Scans schemas of ds on the server as a batch. */
export async function startBatch(ds: string, schemas: string[]) {
  // One key per request: a retried request (a refreshed CSRF token) gets the
  // batch the first one started instead of starting another.
  const requestKey = Array.from(crypto.getRandomValues(new Uint8Array(16)), (b) => b.toString(16).padStart(2, '0')).join('')
  const { startScanBatch } = await run(StartScanBatchMutation, { input: { datasource: ds, schemas, requestKey } }, true)
  const s = scansOf(ds)
  s.batches = [startScanBatch, ...s.batches.filter((b) => b.id !== startScanBatch.id)]
  void followBatches(ds)
}

/** Stops a batch: what is waiting is not scanned, what runs is canceled. */
export async function cancelBatch(ds: string, id: string) {
  await run(CancelScanBatchMutation, { id }, true)
  await followBatches(ds)
}

/** Scans again what a batch failed to scan or did not get to. */
export async function retryBatch(ds: string, id: string) {
  const { retryScanBatch } = await run(RetryScanBatchMutation, { id }, true)
  const s = scansOf(ds)
  s.batches = [retryScanBatch, ...s.batches]
  void followBatches(ds)
}
