import type { Action, GrantInput } from '@/gql/graphql'

/**
 * Grants or revokes one action on every named entity. Granting extends an
 * entity's first grant (keeping its field and row scope) or adds a new grant;
 * revoking removes the action from every grant and drops grants left empty.
 */
export function setActionOn(grants: GrantInput[], entities: string[], action: Action, on: boolean): GrantInput[] {
  const list = grants.map((g) => ({ ...g, actions: [...g.actions] }))
  for (const name of entities) {
    const own = list.filter((g) => g.entity === name)
    if (!on) {
      for (const g of own) g.actions = g.actions.filter((a) => a !== action)
    } else if (!own.some((g) => g.actions.includes(action))) {
      if (own.length) own[0].actions.push(action)
      else list.push({ entity: name, actions: [action] })
    }
  }
  return list.filter((g) => g.actions.length > 0)
}
