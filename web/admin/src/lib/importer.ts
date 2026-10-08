import type { EntityInput } from '@/gql/graphql'

// Tables of different schemas may share a name, so imported tables are
// identified by schema and table, as the server's schemaImport does.

export interface TableRef {
  schema: string
  table: string
}

/**
 * Whether entity e of datasource reads table tb, resolved like the server: an
 * entity without a schema reads defaultSchema (where unqualified names
 * resolve); when that is unknown, a table name unique among tables.
 */
export function readsTable(
  e: EntityInput, datasource: string, tb: TableRef, tables: TableRef[], defaultSchema?: string | null,
): boolean {
  if ((e.datasource ?? 'default') !== datasource || (e.source ?? e.name) !== tb.table) return false
  const schema = e.schema || defaultSchema
  if (schema) return schema === tb.schema
  return tables.filter((x) => x.table === tb.table).length === 1
}

/**
 * Renames candidates whose names are already used in the workspace (for
 * example by unsaved imports the server does not know about) and points
 * relationships between them at the new names.
 */
export function uniqueCandidates(candidates: (EntityInput & { schema?: string | null })[], used: string[]): EntityInput[] {
  const taken = new Set(used)
  const renamed = new Map<string, string>()
  for (const c of candidates) {
    let name = c.name
    if (taken.has(name)) {
      const base = c.schema && !name.startsWith(`${c.schema}_`) ? `${c.schema}_${name}` : name
      name = base
      for (let i = 2; taken.has(name); i++) name = `${base}_${i}`
    }
    taken.add(name)
    renamed.set(c.name, name)
  }
  return candidates.map((c) => ({
    ...c,
    name: renamed.get(c.name)!,
    relationships: c.relationships?.map((r) => ({ ...r, target: renamed.get(r.target) ?? r.target })),
  }))
}
