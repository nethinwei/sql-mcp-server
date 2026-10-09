import type { EntityInput } from '@/gql/graphql'
import { entityId } from './entityRefs'

// Tables of different schemas may share a name, so imported tables are
// identified by schema and table, as the server's schemaImport does.

export interface TableRef {
  schema: string
  table: string
}

export const tableKey = (tb: TableRef) => `${tb.schema}.${tb.table}`

/**
 * The entity of datasource that reads each table (by tableKey), resolved like
 * the server: an entity without a schema reads defaultSchema (where
 * unqualified names resolve); when that is unknown, a table name unique among
 * tables. The first such entity wins. It indexes tables by name once, so a
 * scan of thousands of tables does not compare every entity with every table.
 */
export function entityByTable(
  entities: EntityInput[], datasource: string, tables: TableRef[], defaultSchema?: string | null,
): Map<string, EntityInput> {
  const byName = new Map<string, TableRef[]>()
  for (const tb of tables) {
    const named = byName.get(tb.table)
    if (named) named.push(tb)
    else byName.set(tb.table, [tb])
  }
  const out = new Map<string, EntityInput>()
  for (const e of entities) {
    if ((e.datasource ?? 'default') !== datasource) continue
    const named = byName.get(e.source ?? e.name) ?? []
    const schema = e.schema || defaultSchema
    const tb = schema ? named.find((x) => x.schema === schema) : named.length === 1 ? named[0] : undefined
    if (tb && !out.has(tableKey(tb))) out.set(tableKey(tb), e)
  }
  return out
}

/**
 * Renames candidates whose IDs are already used in the workspace (for example
 * by unsaved imports the server does not know about) by numbering them, and
 * points relationships between candidates, which target each other by ID, at
 * the new IDs. Entities of other datasources or schemas may share a name.
 */
export function uniqueCandidates(candidates: EntityInput[], used: string[]): EntityInput[] {
  const taken = new Set(used)
  const renamed = new Map<string, string>()
  const named = candidates.map((c) => {
    let name = c.name
    for (let n = 2; taken.has(entityId({ ...c, name })); n++) name = `${c.name}_${n}`
    taken.add(entityId({ ...c, name }))
    if (name !== c.name) renamed.set(entityId(c), entityId({ ...c, name }))
    return name === c.name ? c : { ...c, name, source: c.source ?? c.name }
  })
  return named.map((c) => ({
    ...c,
    relationships: c.relationships?.map((r) => ({ ...r, target: renamed.get(r.target) ?? r.target })),
  }))
}
