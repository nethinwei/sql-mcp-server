import type { Action, EntityInput, GrantInput, RoleInput, UserInput } from '@/gql/graphql'
import { EntityIndex, entityId } from './entityRefs'

// A summary of how a change edits the authorization rules, shown before the
// YAML diff at review: per user, the entity actions its rules grant or stop
// granting and the actions whose field or row scope changes, and which
// entities and fields change visibility. It compares rules, not effective
// access (the server's simulation is authoritative for that): an action's
// scopes are deduplicated, and an unrestricted rule covers the others, so
// adding a rule that changes nothing reports nothing. Access is derived from top-level grants (a user's direct grants
// plus its roles' grants) and entity-level legacy access (roles, fieldACL and
// rowPolicies of the user's roles). Changes this cannot evaluate, because they
// depend on data or on subject values (tenant policies, user subjects), are
// listed with the users they may affect, to be checked by simulation. The
// server stays authoritative. Entities are compared by ID (a reference may
// name another entity after the change) and shown by their shortest
// reference.

export interface Config {
  entities: EntityInput[]
  roles: RoleInput[]
  users: UserInput[]
}

/** One entity action, e.g. orders · READ. */
export interface Access {
  entity: string
  action: Action
}

export interface UserChange {
  user: string
  gained: Access[]
  lost: Access[]
  /** Actions kept whose field or row scope changed. */
  rescoped: Access[]
  /** The subject (attributes row scopes and tenant policies resolve) changed. */
  subjectChanged: boolean
}

/** A change to an entity's tenant policy, and who may be affected. */
export interface BoundaryChange {
  entity: string
  kind: 'tenantPolicy'
  users: string[]
}

export interface FieldChange {
  entity: string
  shown: string[]
  hidden: string[]
  masked: string[]
  unmasked: string[]
}

export interface Summary {
  users: UserChange[]
  boundaries: BoundaryChange[]
  entitiesAdded: string[]
  entitiesRemoved: string[]
  fields: FieldChange[]
}

const key = (a: Access) => `${a.entity}\u0000${a.action}`
const json = (v: unknown) => JSON.stringify(v ?? null)

const unrestricted = 'unrestricted'
const sorted = (v: unknown) => (Array.isArray(v) ? [...(v as string[])].sort() : null)

interface Legacy {
  roles?: Partial<Record<string, string[]>>
  fieldACL?: Record<string, unknown>
  rowPolicies?: Record<string, unknown>
}

/**
 * Each entity action a user holds, with the scopes giving it: top-level
 * grants, and legacy access of each role the user has (its fieldACL and row
 * policy are that role's scope).
 */
function accessOf(cfg: Config, user: UserInput): Map<string, string> {
  if (user.disabled) return new Map()
  const scopes = new Map<string, Set<string>>()
  const add = (entity: string, action: string, read: unknown, write: unknown, rows: unknown) => {
    const k = key({ entity, action: action as Action })
    const scope = read == null && write == null && rows == null ? unrestricted : json([sorted(read), sorted(write), rows])
    scopes.set(k, (scopes.get(k) ?? new Set()).add(scope))
  }
  const grants: GrantInput[] = [
    ...(user.grants ?? []),
    ...(user.roles ?? []).flatMap((r) => cfg.roles.find((x) => x.name === r)?.grants ?? []),
  ]
  const index = new EntityIndex(cfg.entities)
  for (const g of grants) {
    for (const action of g.actions) {
      add(index.idOf(g.entity) ?? g.entity, action, g.fieldsRestricted ? g.readFields ?? [] : null,
        g.fieldsRestricted ? g.writeFields ?? [] : null, g.rows ?? null)
    }
  }
  const roles = new Set((user.roles ?? []).map((r) => r.toLowerCase()))
  for (const e of cfg.entities) {
    const legacy = (e.legacyAccess ?? {}) as Legacy
    for (const [action, holders] of Object.entries(legacy.roles ?? {})) {
      for (const role of (holders ?? []).map((r) => r.toLowerCase()).filter((r) => roles.has(r))) {
        const acl = legacy.fieldACL?.[role] as { read?: string[]; write?: string[] } | undefined
        add(entityId(e), action.toUpperCase(), acl ? acl.read ?? [] : null, acl ? acl.write ?? [] : null,
          legacy.rowPolicies?.[role] ?? null)
      }
    }
  }
  return new Map([...scopes].map(([k, set]) => [k, set.has(unrestricted) ? unrestricted : json([...set].sort())]))
}

function access(k: string): Access {
  const [entity, action] = k.split('\u0000')
  return { entity, action: action as Action }
}

function userChanges(before: Config, after: Config): UserChange[] {
  const names = [...new Set([...before.users, ...after.users].map((u) => u.name))].sort()
  const out: UserChange[] = []
  for (const name of names) {
    const was = before.users.find((u) => u.name === name)
    const now = after.users.find((u) => u.name === name)
    const a = was ? accessOf(before, was) : new Map<string, string>()
    const b = now ? accessOf(after, now) : new Map<string, string>()
    const change: UserChange = {
      user: name,
      gained: [...b.keys()].filter((k) => !a.has(k)).map(access),
      lost: [...a.keys()].filter((k) => !b.has(k)).map(access),
      rescoped: [...b.keys()].filter((k) => a.has(k) && a.get(k) !== b.get(k)).map(access),
      subjectChanged: Boolean(was && now && !now.disabled && json(was.subject) !== json(now.subject)),
    }
    if (change.gained.length || change.lost.length || change.rescoped.length || change.subjectChanged) out.push(change)
  }
  return out
}

function fieldChanges(before: Config, after: Config): FieldChange[] {
  const out: FieldChange[] = []
  const was = new EntityIndex(before.entities)
  for (const e of after.entities) {
    const old = was.get(entityId(e))
    if (!old) continue
    const visible = (ent: EntityInput) => new Set((ent.fields ?? []).filter((f) => !f.exclude).map((f) => f.name))
    const masks = (ent: EntityInput) => new Map((ent.fields ?? []).map((f) => [f.name, f.mask ?? '']))
    const [v0, v1, m0, m1] = [visible(old), visible(e), masks(old), masks(e)]
    const change: FieldChange = {
      entity: entityId(e),
      shown: [...v1].filter((f) => !v0.has(f)),
      hidden: [...v0].filter((f) => !v1.has(f)),
      masked: [...m1].filter(([f, m]) => m && m !== (m0.get(f) ?? '')).map(([f]) => f),
      unmasked: [...m0].filter(([f, m]) => m && !m1.get(f) && v1.has(f)).map(([f]) => f),
    }
    if (change.shown.length || change.hidden.length || change.masked.length || change.unmasked.length) out.push(change)
  }
  return out
}

/** Users who may reach entity in either configuration. */
function usersReaching(entity: string, configs: Config[]): string[] {
  const out = new Set<string>()
  for (const cfg of configs) {
    for (const u of cfg.users) {
      if ([...accessOf(cfg, u).keys()].some((k) => k.startsWith(`${entity}\u0000`))) out.add(u.name)
    }
  }
  return [...out].sort()
}

function boundaryChanges(before: Config, after: Config): BoundaryChange[] {
  const out: BoundaryChange[] = []
  const was = new EntityIndex(before.entities)
  for (const e of after.entities) {
    const old = was.get(entityId(e))
    if (old && json(old.tenantPolicy) !== json(e.tenantPolicy)) {
      out.push({ entity: entityId(e), kind: 'tenantPolicy', users: usersReaching(entityId(e), [before, after]) })
    }
  }
  return out
}

export function summarize(before: Config, after: Config): Summary {
  const ids = (c: Config) => new Set(c.entities.map(entityId))
  const [b, a] = [ids(before), ids(after)]
  const [was, now] = [new EntityIndex(before.entities), new EntityIndex(after.entities)]
  const label = (id: string) => (now.get(id) ? now.shortName(id) : was.get(id) ? was.shortName(id) : id)
  const labelled = (x: Access) => ({ ...x, entity: label(x.entity) })
  return {
    users: userChanges(before, after).map((u) => ({
      ...u, gained: u.gained.map(labelled), lost: u.lost.map(labelled), rescoped: u.rescoped.map(labelled),
    })),
    boundaries: boundaryChanges(before, after).map((c) => ({ ...c, entity: label(c.entity) })),
    entitiesAdded: [...a].filter((n) => !b.has(n)).map(label),
    entitiesRemoved: [...b].filter((n) => !a.has(n)).map(label),
    fields: fieldChanges(before, after).map((c) => ({ ...c, entity: label(c.entity) })),
  }
}

/** Whether the summary has nothing to show. */
export function isEmpty(s: Summary): boolean {
  return !s.users.length && !s.boundaries.length && !s.entitiesAdded.length && !s.entitiesRemoved.length &&
    !s.fields.length
}
