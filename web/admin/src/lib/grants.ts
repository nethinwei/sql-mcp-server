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
  const idOf = (ref: string) => (index ? index.idOf(ref) : ref)
  for (const id of entities) {
    const name = index ? index.shortName(id) : id
    const own = list.filter((g) => idOf(g.entity) === id)
    if (!on) {
      for (const g of own) g.actions = g.actions.filter((a) => a !== action)
    } else if (!own.some((g) => g.actions.includes(action))) {
      if (own.length) own[0].actions.push(action)
      else list.push({ entity: name, actions: [action] })
    }
  }
  return list.filter((g) => g.actions.length > 0)
}
