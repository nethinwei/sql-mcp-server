import type { Action, CapabilitiesQuery } from '@/gql/graphql'

export type Capability = CapabilitiesQuery['capabilities'][number]

/** Capabilities by entity and action. */
export type CapabilityIndex = Map<string, Map<Action, Capability>>

export function indexCapabilities(list: Capability[]): CapabilityIndex {
  const out: CapabilityIndex = new Map()
  for (const c of list) {
    if (!out.has(c.entity)) out.set(c.entity, new Map())
    out.get(c.entity)!.set(c.action, c)
  }
  return out
}

/**
 * Whether the connection an entity action routes to may run it. Entities the
 * running service has not assessed (new in the workspace, or no privilege
 * report) are unknown and stay editable.
 */
export function capabilityOf(index: CapabilityIndex, entity: string, action: Action): Capability | undefined {
  return index.get(entity)?.get(action)
}

export const denied = (c: Capability | undefined) => c?.privilege === 'DENIED'

/** Whether action applies to an entity: EXECUTE to procedures, the others to the rest. */
export function applicable(kind: string | null | undefined, action: Action) {
  return (action === 'EXECUTE') === (kind === 'procedure')
}

/** Whether action may be newly granted on entity id: it applies and its connection is not denied it. */
export const grantable = (index: CapabilityIndex, id: string, kind: string | null | undefined, action: Action) =>
  applicable(kind, action) && !denied(capabilityOf(index, id, action))
