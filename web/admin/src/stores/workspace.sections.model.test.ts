import { beforeEach, describe, expect, it } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { useWorkspace, type Conflict } from './workspace'
import { getAt, same, type Path } from '@/lib/merge'
import type { UserInput, WorkspaceQuery } from '@/gql/graphql'

// Model-based test of users and settings across publishes, the sections the
// role model (workspace.model.test.ts) leaves out: users hold role lists,
// a flag, a subject and grants; settings are nested objects whose sections
// and fields come and go. Random local edits, publishes by another
// administrator with a rebase, and conflict choices; after every step, the
// same invariants as for roles. A failure names its seed and steps.

function rng(seed: number) {
  let s = seed >>> 0
  return () => {
    s = (s * 1664525 + 1013904223) >>> 0
    return s / 2 ** 32
  }
}

type Rand = ReturnType<typeof rng>
type Settings = Record<string, Record<string, unknown>>
interface Side { users: UserInput[]; settings: Settings }

const pick = <T>(r: Rand, xs: readonly T[]) => xs[Math.floor(r() * xs.length)]
const userNames = ['alice', 'bob'] as const
const roleNames = ['analyst', 'support'] as const
const sections = { cost: ['maxRows', 'softScore'], cache: ['ttl', 'maxSize'] } as const

function edit(r: Rand, side: Side): Side {
  const out = JSON.parse(JSON.stringify(side)) as Side
  const user = out.users.length ? pick(r, out.users) : undefined
  switch (Math.floor(r() * 9)) {
    case 0: {
      const name = pick(r, userNames)
      if (!out.users.some((u) => u.name === name)) out.users.push({ name, roles: [], disabled: false, grants: [] })
      break
    }
    case 1: if (user) out.users = out.users.filter((u) => u !== user); break
    case 2: if (user) user.roles = roleNames.filter(() => r() < 0.5); break
    case 3: if (user) user.disabled = !user.disabled; break
    case 4: if (user) user.subject = r() < 0.3 ? undefined : { tenant_id: pick(r, [1, 2]) }; break
    case 5: if (user) user.grants = r() < 0.5 ? [] : [{ entity: 'orders', actions: [pick(r, ['READ', 'AGGREGATE'])] }]; break
    case 6: { // set a settings field, adding its section
      const key = pick(r, Object.keys(sections)) as keyof typeof sections
      out.settings[key] = { ...(out.settings[key] ?? {}), [pick(r, sections[key])]: pick(r, [1, 2, 3]) }
      break
    }
    case 7: { // remove a settings field
      const key = pick(r, Object.keys(sections)) as keyof typeof sections
      if (out.settings[key]) delete out.settings[key][pick(r, sections[key])]
      break
    }
    default: delete out.settings[pick(r, Object.keys(sections))] // remove a section
  }
  return out
}

function revision(id: number, side: Side): WorkspaceQuery {
  return {
    published: { id: String(id) },
    revision: {
      id: String(id), parent: null, state: 'PUBLISHED', contentHash: `sha256:${id}`, author: 'other', comment: '',
      createdAt: '', publishedAt: '',
      config: {
        datasources: [],
        entities: [{
          name: 'orders', source: 'orders', datasource: 'default', kind: 'table', primaryKey: [], uniqueKeys: [],
          params: [], affects: [], allowCascade: false, mcp: { dmlTools: true, customTool: false, trustedProcedure: false },
          fields: [{ name: 'id', exclude: false }], relationships: [],
        }],
        roles: roleNames.map((name) => ({ name, members: [], grants: [] })),
        users: side.users.map((u) => ({
          name: u.name, roles: [...(u.roles ?? [])], disabled: Boolean(u.disabled), hasToken: false,
          subject: u.subject ?? null,
          grants: (u.grants ?? []).map((g) => ({
            entity: g.entity, actions: [...g.actions], fieldsRestricted: false, readFields: [], writeFields: [],
          })),
        })),
        settings: JSON.parse(JSON.stringify(side.settings)),
      },
    },
  } as unknown as WorkspaceQuery
}

/** Each value the merge compares separately, by kind, name and path. */
function units(side: Side): Map<string, unknown> {
  const out = new Map<string, unknown>()
  for (const u of side.users) {
    out.set(JSON.stringify(['user', u.name, []]), true)
    out.set(JSON.stringify(['user', u.name, ['roles']]), u.roles ?? [])
    out.set(JSON.stringify(['user', u.name, ['disabled']]), Boolean(u.disabled))
    out.set(JSON.stringify(['user', u.name, ['subject']]), u.subject ?? null)
    const grants = (u.grants ?? []).map((g) => [...g.actions].sort())
    if (grants.length) out.set(JSON.stringify(['user', u.name, ['grants', { element: 'orders' }]]), grants)
  }
  for (const [key, fields] of Object.entries(side.settings)) {
    out.set(JSON.stringify(['settings', 'settings', [key]]), true)
    for (const [f, v] of Object.entries(fields ?? {})) out.set(JSON.stringify(['settings', 'settings', [key, f]]), v)
  }
  return out
}

function changed(a: Side, b: Side): Set<string> {
  const [ua, ub] = [units(a), units(b)]
  return new Set([...new Set([...ua.keys(), ...ub.keys()])].filter((k) => !same(ua.get(k), ub.get(k))))
}

const prefix = (a: Path, b: Path) => a.length <= b.length && same(a, b.slice(0, a.length))

function covered(open: Conflict[], unit: string): boolean {
  const [kind, name, path] = JSON.parse(unit) as [string, string, Path]
  return open.some((c) => c.kind === kind && c.name === name && (prefix(c.path, path) || prefix(path, c.path)))
}

function sameAt(c: Conflict, a: unknown, b: unknown) {
  const last = c.path[c.path.length - 1]
  return typeof last === 'string' ? same({ [last]: a }, { [last]: b }) : same(a, b)
}

function itemOf(side: { users: UserInput[]; settings: Record<string, unknown> }, c: Conflict): unknown {
  return c.kind === 'settings' ? side.settings : side.users.find((u) => u.name === c.name)
}

describe('users and settings across publishes (model-based)', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('keeps the invariants through random edits, publishes and choices', () => {
    for (let seed = 1; seed <= 1500; seed++) {
      setActivePinia(createPinia())
      const r = rng(seed)
      const ws = useWorkspace()
      let id = 1
      let published: Side = { users: [], settings: {} }
      ws.apply(revision(id, published), String(id))
      const edited = new Set<string>()
      const contested = new Set<string>()
      const trace: string[] = []
      for (let step = 0; step < 40; step++) {
        const at = () => `seed ${seed} step ${step}\n${trace.join('\n')}`
        const op = r()
        if (op < 0.5) {
          const before: Side = JSON.parse(JSON.stringify({ users: ws.users, settings: ws.settings }))
          const next = edit(r, before)
          ws.users = next.users
          ws.setSettings(next.settings)
          const after = units(next)
          for (const u of changed(before, next)) {
            edited.add(u)
            // What is removed here is an edit on the newer revision.
            if (!after.has(u)) {
              const [kind, name, path] = JSON.parse(u) as [string, string, Path]
              for (const c of [...contested]) {
                const [k, n, p] = JSON.parse(c) as [string, string, Path]
                if (k === kind && n === name && prefix(path, p)) contested.delete(c)
              }
            }
          }
          trace.push(`local ${JSON.stringify(next)}`)
        } else if (op < 0.85) {
          const before = published
          published = edit(r, published)
          for (const u of changed(before, published)) if (edited.has(u)) contested.add(u)
          ws.rebaseOnto(revision(++id, published), String(id))
          trace.push(`publish ${id} ${JSON.stringify(published)} -> conflicts ${JSON.stringify(ws.openConflicts.map((c) => [c.kind, c.name, c.path]))}`)
        } else if (ws.openConflicts.length) {
          const c = pick(r, ws.openConflicts)
          const choice = r() < 0.5 ? 'mine' : 'theirs'
          ws.resolveConflict(c, choice)
          trace.push(`resolve ${c.kind} ${c.name} ${JSON.stringify(c.path)} ${choice}`)
          const value = getAt(itemOf(ws, c), c.path)
          expect(sameAt(c, value, choice === 'mine' ? c.mine : c.theirs), `${at()}: ${choice} not taken`).toBe(true)
          for (const u of [...contested]) if (covered([c], u)) contested.delete(u)
        }
        const open = ws.openConflicts
        const base = JSON.parse(ws.baseline) as Side
        const diverged = changed({ users: ws.users, settings: ws.settings as Settings }, base)
        for (const u of [...edited]) if (!diverged.has(u)) { edited.delete(u); contested.delete(u) }
        for (const u of contested) {
          if (!covered(open, u)) expect.fail(`${at()}: ${u} changed on both sides, not under a conflict`)
        }
        for (const c of open) {
          expect(sameAt(c, c.mine, getAt(itemOf(ws, c), c.path)), `${at()}: mine is not current`).toBe(true)
          expect(sameAt(c, c.theirs, getAt(itemOf(base, c), c.path)), `${at()}: theirs is not the newest`).toBe(true)
          expect(sameAt(c, c.mine, c.theirs), `${at()}: equal sides`).toBe(false)
        }
        for (const u of ws.users) expect(typeof u.name, `${at()}: ${JSON.stringify(ws.users)}`).toBe('string')
        expect(new Set(ws.users.map((u) => u.name)).size, at()).toBe(ws.users.length)
      }
      for (let guard = 0; ws.openConflicts.length && guard < 50; guard++) {
        ws.resolveConflict(ws.openConflicts[0], r() < 0.5 ? 'mine' : 'theirs')
      }
      expect(ws.openConflicts, `seed ${seed}: unresolvable conflicts`).toEqual([])
    }
  }, 120_000)
})
