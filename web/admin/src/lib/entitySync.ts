import type { EntityInput } from '@/gql/schema'
import type { Plan } from './plan'
import { entityId } from './entityRefs'

// Entity sync from the entity list: the entities in view, less procedures
// (no table) and those of unreachable datasources (their tables are not
// known to be gone), are brought in line with the database by one plan. Its
// schemas scanned over an hour ago, or never, are scanned first by default,
// as drift and gone tables are judged from the last scan.

export const staleAfterMs = 60 * 60 * 1000

export interface SyncScope {
  /** The IDs of the entities synced. */
  ids: string[]
  /** Entities left out as their datasource is unreachable, by datasource. */
  unreachable: Record<string, string[]>
  procedures: number
  /** The schemas the synced entities read, by datasource; '' is the default one. */
  schemas: Record<string, string[]>
}

export function syncScope(entities: EntityInput[], unreachable: Set<string>): SyncScope {
  const out: SyncScope = { ids: [], unreachable: {}, procedures: 0, schemas: {} }
  for (const e of entities) {
    const ds = e.datasource ?? 'default'
    if (e.kind === 'procedure') {
      out.procedures++
      continue
    }
    if (unreachable.has(ds)) {
      ;(out.unreachable[ds] ??= []).push(entityId(e))
      continue
    }
    out.ids.push(entityId(e))
    const schemas = (out.schemas[ds] ??= [])
    const schema = e.schema ?? ''
    if (!schemas.includes(schema)) schemas.push(schema)
  }
  return out
}

export interface SchemaScan {
  datasource: string
  schema: string
  /** When it was last scanned; null when never. */
  scannedAt: string | null
  stale: boolean
}

/**
 * The schemas of scope with their last scans: '' resolves to the
 * datasource's default schema (from its schema list).
 */
export function schemaScans(
  scope: SyncScope,
  lists: Record<string, { defaultSchema: string | null; schemas: { name: string; scannedAt: string | null }[] }>,
  now: number,
): SchemaScan[] {
  const out: SchemaScan[] = []
  for (const [ds, schemas] of Object.entries(scope.schemas)) {
    const list = lists[ds]
    const names = [...new Set(schemas.map((s) => s || list?.defaultSchema || ''))]
    for (const schema of names.sort()) {
      const scannedAt = list?.schemas.find((s) => s.name === schema)?.scannedAt ?? null
      out.push({ datasource: ds, schema, scannedAt,
        stale: !scannedAt || now - new Date(scannedAt).getTime() > staleAfterMs })
    }
  }
  return out
}

/** The plan syncing the entities: new columns come in hidden. */
export function syncPlan(ids: string[]): Plan {
  return { scope: { entities: ids }, sync: { add: true, remove: true, hideNew: true, removeTables: true, keys: true } }
}
