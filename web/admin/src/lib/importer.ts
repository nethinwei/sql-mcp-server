import type { EntityInput } from '@/gql/graphql'

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
 * Renames candidates whose names are already used in the workspace (for
 * example by unsaved imports the server does not know about, or a candidate
 * of another schema scanned separately) and points relationships between them
 * at the new names. A relationship targets a candidate by its name: the one of
 * the same schema when candidates of several schemas share it.
 */
export function uniqueCandidates(candidates: (EntityInput & { schema?: string | null })[], used: string[]): EntityInput[] {
  const taken = new Set(used)
  const byName = new Map<string, number[]>()
  const names = candidates.map((c, i) => {
    byName.set(c.name, [...(byName.get(c.name) ?? []), i])
    let name = c.name
    if (taken.has(name)) {
      const base = c.schema && !name.startsWith(`${c.schema}_`) ? `${c.schema}_${name}` : name
      name = base
      for (let n = 2; taken.has(name); n++) name = `${base}_${n}`
    }
    taken.add(name)
    return name
  })
  const target = (name: string, schema?: string | null) => {
    const same = byName.get(name) ?? []
    const i = same.find((j) => candidates[j].schema === schema) ?? same[0]
    return i === undefined ? name : names[i]
  }
  return candidates.map((c, i) => ({
    ...c,
    name: names[i],
    relationships: c.relationships?.map((r) => ({ ...r, target: target(r.target, c.schema) })),
  }))
}
