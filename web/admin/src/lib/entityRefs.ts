// Entities are namespaced: an entity is identified as datasource.schema.name
// (datasource.name without a schema), and referred to like a partially
// qualified SQL name. These are the server's rules (core/entity,
// core/config.EntityRefs), checked against the same cases
// (core/config/testdata/entity_refs.json).

export interface Namespaced {
  name: string
  datasource?: string | null
  schema?: string | null
}

const datasourceOf = (e: Namespaced) => e.datasource || 'default'

/** The canonical identity of an entity. */
export function entityId(e: Namespaced): string {
  return e.schema ? `${datasourceOf(e)}.${e.schema}.${e.name}` : `${datasourceOf(e)}.${e.name}`
}

/**
 * The ways to refer to an entity, shortest first: name, schema.name,
 * datasource.name and datasource.schema.name (its ID).
 */
export function referencesOf(e: Namespaced): string[] {
  const ds = datasourceOf(e)
  if (!e.schema) return [e.name, `${ds}.${e.name}`]
  // schema.name and datasource.name are one
  if (e.schema === ds) return [e.name, `${e.schema}.${e.name}`, `${ds}.${e.schema}.${e.name}`]
  return [e.name, `${e.schema}.${e.name}`, `${ds}.${e.name}`, `${ds}.${e.schema}.${e.name}`]
}

/** What a reference names: one entity, several, or none. */
export type Resolution = { id: string } | { candidates: string[] } | null

/** Entities indexed by ID and by every reference to them. */
export class EntityIndex<E extends Namespaced = Namespaced> {
  private readonly byId = new Map<string, E>()
  private readonly byRef = new Map<string, string[]>()

  constructor(entities: E[]) {
    for (const e of entities) {
      const id = entityId(e)
      this.byId.set(id, e)
      for (const ref of referencesOf(e)) this.byRef.set(ref, [...(this.byRef.get(ref) ?? []), id])
    }
  }

  get(id: string): E | undefined {
    return this.byId.get(id)
  }

  /** The IDs ref may name: the entity it is the ID of, else every match. */
  match(ref: string): string[] {
    return this.byId.has(ref) ? [ref] : this.byRef.get(ref) ?? []
  }

  resolve(ref: string): Resolution {
    const ids = this.match(ref)
    if (ids.length === 1) return { id: ids[0] }
    return ids.length ? { candidates: ids } : null
  }

  /** The ID of the one entity ref names, or undefined. */
  idOf(ref: string): string | undefined {
    const ids = this.match(ref)
    return ids.length === 1 ? ids[0] : undefined
  }

  /** The shortest reference naming the entity id alone. */
  shortName(id: string): string {
    const e = this.byId.get(id)
    if (!e) return id
    const refs = referencesOf(e)
    return refs.find((ref) => this.idOf(ref) === id) ?? refs[refs.length - 1]
  }
}
