import type { EntityInput } from '@/gql/graphql'

// Tables of different schemas may share a name, so imported tables are
// identified by schema and table, as the server's schemaImport does.

export interface TableRef {
  schema: string
  table: string
}

/**
 * Whether entity e of datasource reads table tb. An entity without a schema
 * matches by name only when no other listed table shares that name.
 */
export function readsTable(e: EntityInput, datasource: string, tb: TableRef, tables: TableRef[]): boolean {
  if ((e.datasource ?? 'default') !== datasource || (e.source ?? e.name) !== tb.table) return false
  if (e.schema) return e.schema === tb.schema
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
