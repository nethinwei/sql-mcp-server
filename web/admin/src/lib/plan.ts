import type { CatalogFilter, TableStatus } from '@/gql/graphql'

// A plan as x/configplan decodes it: a scope of scanned tables and rules for
// them. The server evaluates it (previewPlan); the console only builds it,
// from a selection or a form, and saves it as a template.

export type PlanStatus = 'new' | 'workspace' | 'published'

export interface Scope {
  datasources?: string[]
  schemas?: string[]
  tables?: string[]
  search?: string
  status?: PlanStatus[]
  drift?: boolean
  refs?: string[]
  /** The tables of these configured entities, by ID. */
  entities?: string[]
}

export interface GrantRule {
  /** role:<name> or user:<name>. */
  principal: string
  actions: string[]
  fields?: string[]
  rows?: Record<string, unknown>
}

export interface FieldRule {
  columns: string[]
  types?: string[]
  exclude?: boolean
  mask?: string
}

export interface SyncRule {
  add?: boolean
  remove?: boolean
  hideNew?: boolean
  /** Removes the entities whose tables are gone. */
  removeTables?: boolean
  /** Sets keys and the table/view kind to the scan's; reports relationships. */
  keys?: boolean
}

export interface Plan {
  scope: Scope
  import?: boolean
  grants?: GrantRule[]
  fields?: FieldRule[]
  sync?: SyncRule
  exclude?: string[]
  names?: Record<string, string>
}

const nonEmpty = <T>(xs: T[] | undefined | null) => (xs && xs.length ? xs : undefined)

/** The plan scope selecting what a catalog filter selects. */
export function scopeOf(f: CatalogFilter): Scope {
  return clean({
    datasources: nonEmpty(f.datasources), schemas: nonEmpty(f.schemas), tables: nonEmpty(f.tables),
    search: f.search?.trim() || undefined, drift: f.drift || undefined,
    status: nonEmpty(f.status?.map((s) => s.toLowerCase() as PlanStatus)),
  })
}

/** The catalog filter selecting what a plan scope selects, but for refs. */
export function filterOf(s: Scope): CatalogFilter {
  return {
    datasources: nonEmpty(s.datasources), schemas: nonEmpty(s.schemas), tables: nonEmpty(s.tables),
    search: s.search || undefined, drift: s.drift || undefined,
    status: nonEmpty(s.status?.map((x) => x.toUpperCase() as TableStatus)),
  }
}

/** A plan without empty parts, as templates keep it. */
export function cleanPlan(p: Plan): Plan {
  const sync = p.sync && (p.sync.add || p.sync.remove || p.sync.removeTables || p.sync.keys)
    ? clean({ ...p.sync }) : undefined
  return clean({
    scope: clean({ ...p.scope, refs: nonEmpty(p.scope.refs), entities: nonEmpty(p.scope.entities) }) as Scope,
    import: p.import || undefined,
    grants: nonEmpty(p.grants?.map((g) => clean({ ...g, fields: nonEmpty(g.fields) }) as GrantRule)),
    fields: nonEmpty(p.fields?.map((f) => clean({ ...f, types: nonEmpty(f.types), mask: f.mask || undefined }) as FieldRule)),
    sync,
    exclude: nonEmpty(p.exclude),
    names: p.names && Object.keys(p.names).length ? p.names : undefined,
  }) as Plan
}

function clean<T extends object>(o: T): T {
  return Object.fromEntries(Object.entries(o).filter(([, v]) => v !== undefined && v !== false)) as T
}

/** Splits a list of glob patterns typed with commas or spaces. */
export function patterns(text: string): string[] {
  return text.split(/[\s,，]+/).map((x) => x.trim()).filter(Boolean)
}

/** The tasks a plan does, as the form offers them. */
export type PlanTask = 'import' | 'grant' | 'fields' | 'sync'

/** A new plan: importing the tables in scope is what most plans start with. */
export function newPlan(scope: Scope = {}): Plan {
  return { scope: { ...scope }, import: true, grants: [], fields: [], sync: {} }
}

/**
 * A saved plan (a template, or one handed over) ready for the form. What the
 * plan leaves out keeps the server's meaning: no import, no grants, no field
 * rules, no sync. A template that only grants never starts importing.
 */
export function loadPlan(p: Plan): Plan {
  const copy = JSON.parse(JSON.stringify(p)) as Plan
  return {
    ...copy, scope: copy.scope ?? {}, import: copy.import === true, grants: copy.grants ?? [],
    fields: copy.fields ?? [], sync: copy.sync ?? {},
  }
}

/** The tasks a plan does. */
export function tasksOf(p: Plan): PlanTask[] {
  const out: PlanTask[] = []
  if (p.import) out.push('import')
  if (p.grants?.length) out.push('grant')
  if (p.fields?.length) out.push('fields')
  if (p.sync?.add || p.sync?.remove || p.sync?.removeTables) out.push('sync')
  return out
}
