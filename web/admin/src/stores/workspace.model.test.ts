import { beforeEach, describe, expect, it } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { useWorkspace, type Conflict } from './workspace'
import { getAt, same } from '@/lib/merge'
import type { RoleInput, WorkspaceQuery } from '@/gql/graphql'

// Model-based test of the workspace across publishes: random sequences of
// local edits, publishes by another administrator (each followed by a
// rebase) and conflict choices, with conflicts left open across rebases and
// edited after they arise. After every step the invariants below must hold;
// a failure names the seed and step that reproduce it.

function rng(seed: number) {
  let s = seed >>> 0
  return () => {
    s = (s * 1664525 + 1013904223) >>> 0
    return s / 2 ** 32
  }
}

type Rand = ReturnType<typeof rng>
const pick = <T>(r: Rand, xs: readonly T[]) => xs[Math.floor(r() * xs.length)]
const names = ['analyst', 'finance'] as const
const entities = ['orders', 'refunds'] as const
const actions = ['READ', 'AGGREGATE', 'CREATE'] as const
const texts = ['', 'a', 'b', 'c'] as const

/** A random edit of a list of roles, as either administrator might make. */
function editRoles(r: Rand, roles: RoleInput[]): RoleInput[] {
  const out = JSON.parse(JSON.stringify(roles)) as RoleInput[]
  const role = out.length ? pick(r, out) : undefined
  switch (Math.floor(r() * 7)) {
    case 0: { // add a role
      const name = pick(r, names)
      if (!out.some((x) => x.name === name)) out.push({ name, description: pick(r, texts), grants: [] })
      break
    }
    case 1: // remove a role
      if (role) return out.filter((x) => x !== role)
      break
    case 2: if (role) role.description = pick(r, texts); break
    case 3: // grant an entity
      if (role) role.grants = [...(role.grants ?? []), { entity: pick(r, entities), actions: [pick(r, actions)] }]
      break
    case 4: // drop a grant
      if (role?.grants?.length) role.grants = role.grants.filter((_, i) => i !== Math.floor(r() * role.grants!.length))
      break
    default: { // change a grant's actions
      const g = role?.grants?.length ? pick(r, role.grants) : undefined
      if (g) g.actions = actions.filter(() => r() < 0.5)
    }
  }
  return out
}

function revision(id: number, roles: RoleInput[]): WorkspaceQuery {
  return {
    published: { id: String(id) },
    revision: {
      id: String(id), parent: null, state: 'PUBLISHED', contentHash: `sha256:${id}`, author: 'other', comment: '',
      createdAt: '', publishedAt: '',
      config: {
        datasources: [],
        entities: entities.map((name) => ({
          name, source: name, datasource: 'default', kind: 'table', primaryKey: [], uniqueKeys: [], params: [],
          affects: [], allowCascade: false, mcp: { dmlTools: true, customTool: false, trustedProcedure: false },
          fields: [{ name: 'id', exclude: false }], relationships: [],
        })),
        roles: roles.map((role) => ({
          name: role.name, description: role.description, members: [],
          grants: (role.grants ?? []).map((g) => ({
            entity: g.entity, actions: [...g.actions], fieldsRestricted: false, readFields: [], writeFields: [],
          })),
        })),
        users: [],
        settings: {},
      },
    },
  } as unknown as WorkspaceQuery
}

/** The workspace's value of a conflict, compared as the merge does. */
function sameAt(c: Conflict, a: unknown, b: unknown) {
  const last = c.path[c.path.length - 1]
  return typeof last === 'string' ? same({ [last]: a }, { [last]: b }) : same(a, b)
}

function nested(a: Conflict, b: Conflict) {
  return a !== b && a.kind === b.kind && a.name === b.name && a.path.length >= b.path.length &&
    same(a.path.slice(0, b.path.length), b.path)
}

/**
 * The values of a role list the merge compares separately: a role's
 * existence, its description, and its grants on each entity, by key.
 */
function units(roles: RoleInput[]): Map<string, unknown> {
  const out = new Map<string, unknown>()
  for (const role of roles) {
    out.set(`${role.name}|role`, true)
    out.set(`${role.name}|description`, role.description || '')
    for (const e of entities) {
      const grants = (role.grants ?? []).filter((g) => g.entity === e).map((g) => [...g.actions].sort())
      if (grants.length) out.set(`${role.name}|grants:${e}`, grants)
    }
  }
  return out
}

/** The units whose value differs between two role lists. */
function changed(a: RoleInput[], b: RoleInput[]): Set<string> {
  const [ua, ub] = [units(a), units(b)]
  const keys = new Set([...ua.keys(), ...ub.keys()])
  return new Set([...keys].filter((k) => !same(ua.get(k), ub.get(k))))
}

/** Whether an open conflict covers a unit: on its role, at or around its path. */
function covered(open: Conflict[], unit: string): boolean {
  const [name, what] = unit.split('|')
  return open.some((c) => {
    if (c.name !== name || c.path.length === 0) return c.name === name
    const top = c.path[0]
    if (what === 'description') return top === 'description'
    if (what.startsWith('grants:')) {
      const step = c.path[1]
      return top === 'grants' && (step === undefined || (typeof step !== 'string' && step.element === what.slice(7)))
    }
    return false
  })
}

describe('workspace across publishes (model-based)', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('keeps its invariants through random edits, publishes and choices', () => {
    for (let seed = 1; seed <= 1500; seed++) {
      setActivePinia(createPinia())
      const r = rng(seed)
      const ws = useWorkspace()
      let id = 1
      let published: RoleInput[] = []
      ws.apply(revision(id, published), String(id))
      // Units edited here since they last matched what is published, and
      // those of them the other administrator changed meanwhile.
      const edited = new Set<string>()
      const contested = new Set<string>()
      const trace: string[] = []
      for (let step = 0; step < 40; step++) {
        const at = () => `seed ${seed} step ${step}\n${trace.join('\n')}`
        const op = r()
        if (op < 0.5) {
          const before = JSON.parse(JSON.stringify(ws.roles)) as RoleInput[]
          ws.roles = editRoles(r, ws.roles)
          const after = units(ws.roles)
          for (const u of changed(before, ws.roles)) {
            edited.add(u)
            // Removing a role or a grant here is an edit on the newer
            // revision: what it held is no longer contested.
            if (!after.has(u)) contested.delete(u)
            if (u.endsWith('|role') && !after.has(u)) {
              for (const c of [...contested]) if (c.startsWith(u.slice(0, -4))) contested.delete(c)
            }
          }
          trace.push(`local: ${JSON.stringify(ws.roles.map((x) => [x.name, x.description, x.grants?.map((g) => g.entity + ':' + g.actions.join('+'))]))}`)
        } else if (op < 0.85) {
          const before = published
          published = editRoles(r, published)
          for (const u of changed(before, published)) if (edited.has(u)) contested.add(u)
          ws.rebaseOnto(revision(++id, published), String(id))
          trace.push(`publish ${id}: ${JSON.stringify(published.map((x) => [x.name, x.description, x.grants?.map((g) => g.entity + ':' + g.actions.join('+'))]))} -> local ${JSON.stringify(ws.roles.map((x) => [x.name, x.description]))} conflicts ${JSON.stringify(ws.openConflicts.map((c) => [c.name, c.path]))}`)
        } else if (ws.openConflicts.length) {
          const c = pick(r, ws.openConflicts)
          const choice = r() < 0.5 ? 'mine' : 'theirs'
          ws.resolveConflict(c, choice)
          trace.push(`resolve ${c.name} ${JSON.stringify(c.path)} ${choice} -> ${JSON.stringify(ws.roles.map((x) => [x.name, x.description]))}`)
          // Either choice settles what the conflict covered.
          for (const u of [...contested]) if (covered([c], u)) contested.delete(u)
          // The choice took effect: the value is the side chosen, as shown.
          const role = ws.roles.find((x) => x.name === c.name)
          expect(sameAt(c, getAt(role, c.path), choice === 'mine' ? c.mine : c.theirs), `${at()}: ${choice}`).toBe(true)
        }
        const open = ws.openConflicts
        // No silent overwrite: a value both sides changed that now differs
        // from what is published is under an open conflict.
        const diverged = changed(ws.roles, (JSON.parse(ws.baseline) as { roles: RoleInput[] }).roles)
        for (const u of [...edited]) {
          if (!diverged.has(u)) { edited.delete(u); contested.delete(u) }
        }
        for (const u of contested) {
          if (!covered(open, u)) {
            expect.fail(`${at()}: ${u} was changed on both sides and is not under a conflict: ${JSON.stringify(open)}`)
          }
        }
        // The baseline is the newest published revision, as the workspace holds it.
        const base = (JSON.parse(ws.baseline) as { roles: RoleInput[] }).roles
        for (const c of open) {
          const role = ws.roles.find((x) => x.name === c.name)
          // Mine is what the workspace holds, theirs what is published now,
          // and they differ.
          expect(sameAt(c, c.mine, getAt(role, c.path)), `${at()}: mine is current`).toBe(true)
          expect(sameAt(c, c.theirs, getAt(base.find((x) => x.name === c.name), c.path)),
            `${at()}: theirs is the newest ${JSON.stringify(c)}`).toBe(true)
          expect(sameAt(c, c.mine, c.theirs), `${at()}: an open conflict with equal sides`).toBe(false)
          expect(open.some((o) => nested(c, o)), `${at()}: nested conflicts ${JSON.stringify(open)}`).toBe(false)
        }
        // Every role is a whole one, named once.
        for (const role of ws.roles) expect(typeof role.name, `${at()}: ${JSON.stringify(ws.roles)}`).toBe('string')
        expect(new Set(ws.roles.map((x) => x.name)).size, at()).toBe(ws.roles.length)
      }
      // Whatever is left can always be resolved, and then nothing blocks.
      for (let guard = 0; ws.openConflicts.length && guard < 50; guard++) {
        ws.resolveConflict(ws.openConflicts[0], r() < 0.5 ? 'mine' : 'theirs')
      }
      expect(ws.openConflicts, `seed ${seed}: unresolvable conflicts`).toEqual([])
    }
  }, 60_000)
})
