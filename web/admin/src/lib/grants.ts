import type { Action } from '@/gql/graphql'
import type { EntityIndex } from './entityRefs'
import type { EntityInput, GrantInput } from '@/gql/schema'

/** A row of the grant matrix: one grant of an entity, or none. */
export interface GrantRow {
  entity: EntityInput
  id: string // the entity's ID
  grant: GrantInput | null
  index: number // position in the grants, -1 when ungranted
  sub: number // 0 for the first grant of an entity
  // The namespace (datasource · schema) of the entity; empty when every
  // entity shares one.
  namespace: string
}

/** A grant with its position in its list. */
export interface PlacedGrant {
  grant: GrantInput
  index: number
}

/**
 * The grants of each entity, in order, keyed by the ID of the entity their
 * reference resolves to (idOf); a grant naming no entity (unresolved or
 * ambiguous) is left out.
 */
export function grantsByEntity(
  grants: GrantInput[], idOf: (ref: string) => string | undefined,
): Map<string, PlacedGrant[]> {
  const out = new Map<string, PlacedGrant[]>()
  grants.forEach((grant, index) => {
    const id = idOf(grant.entity)
    if (id === undefined) return
    const own = out.get(id)
    if (own) own.push({ grant, index })
    else out.set(id, [{ grant, index }])
  })
  return out
}

/**
 * Grants or revokes one action on every named entity. Granting extends an
 * entity's first grant (keeping its field and row scope) or adds a new grant;
 * revoking removes the action from every grant and drops grants left empty.
 * With index, entities are IDs: grants are matched by the entity their
 * reference names, and new grants use the shortest reference.
 */
export function setActionOn(
  grants: GrantInput[], entities: string[], action: Action, on: boolean, index?: EntityIndex,
): GrantInput[] {
  const list = grants.map((g) => ({ ...g, actions: [...g.actions] }))
  // The grants of each entity, resolved once (a column toggle names every
  // entity of the page).
  const byId = grantsByEntity(list, (ref) => (index ? index.idOf(ref) : ref))
  for (const id of entities) {
    const name = index ? index.shortName(id) : id
    const own = (byId.get(id) ?? []).map((p) => p.grant)
    if (!on) {
      for (const g of own) g.actions = g.actions.filter((a) => a !== action)
    } else if (!own.some((g) => g.actions.includes(action))) {
      if (own.length) own[0].actions.push(action)
      else {
        const g = { entity: name, actions: [action] }
        byId.set(id, [{ grant: g, index: list.push(g) - 1 }])
      }
    }
  }
  return list.filter((g) => g.actions.length > 0)
}

/**
 * The position of grant in grants: the same object or else the only grant
 * equal to it, so an editor opened on a grant still finds it after the list
 * was replaced (a write of another administrator, an agent, a reload) and
 * never writes another grant in its place. -1 when it is gone or ambiguous.
 */
export function locateGrant(grants: GrantInput[], grant: GrantInput): number {
  const same = grants.indexOf(grant)
  if (same >= 0) return same
  const text = JSON.stringify(grant)
  const equal = grants.flatMap((g, i) => (JSON.stringify(g) === text ? [i] : []))
  return equal.length === 1 ? equal[0] : -1
}
