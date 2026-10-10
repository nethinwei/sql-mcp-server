import type { EntityInput, FieldInput, GrantInput } from '@/gql/schema'
import type { Proposal } from '@/stores/workspace'
import type { EntityIndex } from './entityRefs'

// What a proposal changes, entity by entity, against the workspace: for a
// principal, the grants on an entity before and after; for an entity, the
// fields that differ. The report counts changes; this shows them.

export interface GrantView {
  actions: string[]
  /** Restricted field lists, or null for every field. */
  readFields: string[] | null
  writeFields: string[] | null
  rows: unknown
}

export interface GrantDiff {
  entity: string
  before: GrantView[]
  after: GrantView[]
}

const view = (g: GrantInput): GrantView => ({
  actions: [...g.actions].map((a) => a.toLowerCase()).sort(),
  readFields: g.fieldsRestricted ? [...(g.readFields ?? [])] : null,
  writeFields: g.fieldsRestricted ? [...(g.writeFields ?? [])] : null,
  rows: g.rows ?? null,
})

/**
 * The grants of principal (role:<name> or user:<name>) on each of entities
 * (IDs), in the workspace and in the proposal.
 */
export function grantDiffs(
  principal: string, entities: string[], index: EntityIndex<EntityInput>,
  workspace: { roles: { name: string; grants?: GrantInput[] | null }[]; users: { name: string; grants?: GrantInput[] | null }[] },
  p: Proposal,
): GrantDiff[] {
  const [kind, name] = principal.split(':', 2)
  const before = (kind === 'role' ? workspace.roles : workspace.users).find((x) => x.name === name)?.grants ?? []
  const after = ((kind === 'role' ? p.roles : p.users) as { name: string; grants: GrantInput[] }[])
    .find((x) => x.name === name)?.grants ?? before
  return entities.map((entity) => ({
    entity,
    before: before.filter((g) => index.idOf(g.entity) === entity).map(view),
    // The proposal names entities by ID.
    after: after.filter((g) => g.entity === entity || index.idOf(g.entity) === entity).map(view),
  }))
}

export interface FieldDiff {
  field: string
  before: FieldInput | null
  after: FieldInput | null
}

const same = (a: FieldInput | null, b: FieldInput | null) =>
  !!a && !!b && !!a.exclude === !!b.exclude && (a.mask || '') === (b.mask || '') && (a.alias || '') === (b.alias || '')

/** The fields that differ between an entity before and after. */
export function fieldDiffs(before: EntityInput | undefined, after: EntityInput | undefined): FieldDiff[] {
  const b = new Map((before?.fields ?? []).map((f) => [f.name, f]))
  const a = new Map((after?.fields ?? []).map((f) => [f.name, f]))
  const names = [...new Set([...b.keys(), ...a.keys()])]
  return names.map((field) => ({ field, before: b.get(field) ?? null, after: a.get(field) ?? null }))
    .filter((d) => !same(d.before, d.after))
}
