import type { Action, GrantInput } from '@/gql/graphql'
import type { EntityIndex } from './entityRefs'

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
