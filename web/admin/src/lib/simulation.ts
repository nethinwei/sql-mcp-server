import type { RouteLocationRaw } from 'vue-router'
import type { Action } from '@/gql/graphql'

// Links into the simulation page: who (a user, or role:<name>), the
// configuration to use, and optionally one call to simulate right away. Pages
// that preview access (role, user, review) hand a picked call over this way.

export interface SimulationLink {
  who: string
  source?: 'workspace' | 'published'
  entity?: string
  action?: Action
  fields?: string[]
}

export function simulationRoute(l: SimulationLink): RouteLocationRaw {
  const query: Record<string, string> = { user: l.who }
  if (l.source) query.source = l.source
  if (l.entity && l.action) {
    query.entity = l.entity
    query.action = l.action
    if (l.fields?.length) query.fields = l.fields.join(',')
  }
  return { name: 'simulate', query }
}

const actions: Action[] = ['READ', 'AGGREGATE', 'CREATE', 'UPDATE', 'DELETE', 'EXECUTE']

/** The link a simulation route carries; unknown or malformed parts are dropped. */
export function parseSimulationQuery(query: Record<string, unknown>): Partial<SimulationLink> {
  const str = (k: string) => (typeof query[k] === 'string' ? (query[k] as string) : undefined)
  const action = str('action') as Action | undefined
  return {
    who: str('user'),
    source: str('source') === 'workspace' ? 'workspace' : str('source') === 'published' ? 'published' : undefined,
    entity: str('entity'),
    action: action && actions.includes(action) ? action : undefined,
    fields: str('fields')?.split(',').filter(Boolean) ?? [],
  }
}
