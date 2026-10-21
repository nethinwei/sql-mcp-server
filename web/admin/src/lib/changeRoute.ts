import type { Change } from '@/stores/workspace'

/** Where a change is edited, or null for a removed item. The entity route
 *  takes the entity ID as id; the role and user routes take the name. */
export const routeFor = (c: Change) =>
  c.type === 'removed'
    ? null
    : c.kind === 'settings'
      ? { name: 'settings' }
      : c.kind === 'entity'
        ? { name: 'entity', params: { id: c.name } }
        : { name: c.kind, params: { name: c.name } }
