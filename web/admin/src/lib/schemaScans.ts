import { reactive } from 'vue'
import type { DataTableRowKey } from 'naive-ui'
import { run } from '@/api/client'
import { CancelSchemaScanMutation, SchemaScanQuery, SchemaSyncQuery, StartSchemaScanMutation } from '@/api/ops'
import type { ImportPartsFragment } from '@/gql/graphql'
import { tableKey } from './importer'

// Importing goes database by database: the page lists a datasource's schemas
// and scans one when it is opened. The server keeps each schema's last scan
// until the next one, and works out candidate names and relationships over
// every scanned schema of the datasource, so the page reads the list and the
// tables of every scanned schema in one answer (a sync) and shows only the
// latest one: a sync started after the last scan finished sees every scan.
// The state lives here, outside the page, so leaving and coming back keeps
// it. It belongs to the database the datasource reads (its source): when the
// datasource is pointed elsewhere, the page drops it.

export type SchemaImport = ImportPartsFragment
export type Table = SchemaImport['tables'][number]

export interface SchemaNode {
  /** '' when the datasource cannot list schemas: its default one. */
  name: string
  scannedAt: string | null
  tables: number | null
  scanning: boolean
  job: string | null
  /** Why the last scan failed. */
  error: string | null
  checked: DataTableRowKey[]
  /** Bumped per scan; an answer about an older one is dropped. */
  seq: number
}

export interface DatasourceScans {
  /** The source the schemas were listed from. */
  source: string | null
  listed: boolean
  defaultSchema: string | null
  schemas: SchemaNode[]
  expanded: string[]
  /** The tables of every scanned schema, from the latest sync. */
  result: SchemaImport | null
  syncing: boolean
  /** Why the latest sync failed: what is shown may be stale. */
  error: string | null
  /** Bumped per sync; only the latest one's answer is shown. */
  seq: number
}

export const scans = reactive<Record<string, DatasourceScans>>({})

export function scansOf(ds: string): DatasourceScans {
  scans[ds] ??= {
    source: null, listed: false, defaultSchema: null, schemas: [], expanded: [], result: null, syncing: false,
    error: null, seq: 0,
  }
  return scans[ds]
}

const message = (e: unknown) => (e instanceof Error ? e.message : String(e))

function node(name: string): SchemaNode {
  return { name, scannedAt: null, tables: null, scanning: false, job: null, error: null, checked: [], seq: 0 }
}

/** The tables read, by schema; "" (the default schema of a datasource that cannot list them) has all. */
export function tablesBySchema(result: SchemaImport | null): Map<string, Table[]> {
  const out = new Map<string, Table[]>([['', result?.tables ?? []]])
  for (const tb of result?.tables ?? []) {
    const same = out.get(tb.schema)
    if (same && tb.schema) same.push(tb)
    else if (tb.schema) out.set(tb.schema, [tb])
  }
  return out
}

/**
 * Reads the schema list of ds (from the database with refresh) and the
 * tables of every scanned schema in one answer, keeping the checked tables.
 */
export async function sync(ds: string, refresh = false) {
  const s = scansOf(ds)
  const mine = ++s.seq
  s.syncing = true
  s.error = null
  try {
    const { schemaList, schemaTables } = await run(SchemaSyncQuery, { datasource: ds, refresh })
    if (mine !== s.seq) return
    if (schemaList.source !== s.source || schemaTables.source !== s.source) {
      for (const n of s.schemas) n.seq++
      Object.assign(s, { source: schemaList.source, schemas: [], expanded: [], result: null })
      // The configuration changed while answering: the tables may be the
      // other database's.
      if (schemaTables.source !== schemaList.source) return void sync(ds)
    }
    const known = new Map(s.schemas.map((n) => [n.name, n]))
    s.schemas = schemaList.schemas.map((x) => Object.assign(known.get(x.name) ?? node(x.name),
      { scannedAt: x.scannedAt ?? null, tables: x.tables ?? null }))
    s.defaultSchema = schemaList.defaultSchema ?? null
    s.expanded = s.expanded.filter((name) => s.schemas.some((n) => n.name === name))
    s.listed = true
    s.result = schemaTables
    const keys = new Set(schemaTables.tables.map(tableKey))
    for (const n of s.schemas) n.checked = n.checked.filter((k) => keys.has(String(k)))
    // A schema left open whose scan the server no longer keeps (it restarted,
    // or the datasource reads another database) is scanned again.
    for (const n of s.schemas) {
      if (s.expanded.includes(n.name) && !n.scannedAt && !n.scanning && !n.error) void scanSchema(ds, n)
    }
  } catch (e) {
    if (mine === s.seq) s.error = message(e)
  } finally {
    if (mine === s.seq) s.syncing = false
  }
}

const sleep = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms))

/** Scans a schema in the background on the server, then syncs. */
export async function scanSchema(ds: string, n: SchemaNode) {
  const mine = ++n.seq
  n.scanning = true
  n.error = null
  try {
    const { startSchemaScan } = await run(StartSchemaScanMutation, { datasource: ds, schemas: n.name ? [n.name] : null }, true)
    // Canceled while starting: the job has an id only now.
    if (mine !== n.seq) return await cancelJob(startSchemaScan.id)
    n.job = startSchemaScan.id
    for (let delay = 200; mine === n.seq; delay = Math.min(delay * 2, 2000)) {
      await sleep(delay)
      if (mine !== n.seq) return
      const { schemaScan: job } = await run(SchemaScanQuery, { id: startSchemaScan.id })
      if (mine !== n.seq) return
      if (!job || job.state === 'CANCELED') return
      if (job.state === 'FAILED') throw new Error(job.error ?? '')
      if (job.state === 'DONE') return await sync(ds)
    }
  } catch (e) {
    if (mine === n.seq) n.error = message(e)
  } finally {
    if (mine === n.seq) {
      n.scanning = false
      n.job = null
    }
  }
}

/** Stops a running scan, or one starting; the schema keeps what it showed before. */
export async function cancelScan(n: SchemaNode) {
  const id = n.job
  n.seq++
  n.scanning = false
  n.job = null
  if (id) await cancelJob(id)
}

async function cancelJob(id: string) {
  try {
    await run(CancelSchemaScanMutation, { id }, true)
  } catch {
    // The server ends the scan on its own at its timeout.
  }
}
