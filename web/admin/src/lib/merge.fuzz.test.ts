import { describe, expect, it } from 'vitest'
import { getAt, merge, same, setAt, type Path, type ValueConflict } from './merge'

// Property tests: random configuration items edited at random on two sides,
// merged, and checked leaf by leaf against the merge rules. Seeded, so a
// failure names a seed that reproduces it.

function rng(seed: number) {
  let s = seed >>> 0
  return () => {
    s = (s * 1664525 + 1013904223) >>> 0
    return s / 2 ** 32
  }
}

type Rand = ReturnType<typeof rng>
const pick = <T>(r: Rand, xs: readonly T[]) => xs[Math.floor(r() * xs.length)]
const chance = (r: Rand, p: number) => r() < p

const actions = ['READ', 'AGGREGATE', 'CREATE', 'UPDATE', 'DELETE'] as const
const texts = ['', 'a', 'b', 'c', undefined] as const

type Obj = Record<string, unknown>

function field(r: Rand, name: string): Obj {
  const f: Obj = { name, exclude: chance(r, 0.3) }
  if (chance(r, 0.5)) f.mask = pick(r, ['email', 'phone', ''])
  if (chance(r, 0.3)) f.alias = pick(r, texts)
  return f
}

function grant(r: Rand, entity: string): Obj {
  return { entity, actions: actions.filter(() => chance(r, 0.5)) }
}

/** A random item shaped like an entity with grants. */
function item(r: Rand): Obj {
  const names = ['id', 'email', 'phone', 'region', 'amount'].filter(() => chance(r, 0.7))
  return {
    name: 'orders',
    description: pick(r, texts),
    fields: names.map((n) => field(r, n)),
    grants: ['a', 'b', 'a'].filter(() => chance(r, 0.6)).map((e) => grant(r, e)),
    mcp: { dmlTools: chance(r, 0.5) },
    tenantPolicy: chance(r, 0.5) ? { op: 'eq', field: 'tenant_id', value: pick(r, [1, 2]) } : undefined,
  }
}

/** Applies a few random edits, the way an administrator would. */
function edit(r: Rand, base: Obj): Obj {
  let v: unknown = structuredClone(base)
  for (let n = 1 + Math.floor(r() * 3); n > 0; n--) {
    const o = v as Obj
    const fields = (o.fields as Obj[]) ?? []
    const grants = (o.grants as Obj[]) ?? []
    switch (Math.floor(r() * 9)) {
      case 0: o.description = pick(r, texts); break
      case 1: if (fields.length) v = setAt(v, ['fields', { element: pick(r, fields).name as string }, 'mask'], pick(r, ['email', 'secret', undefined])); break
      case 2: if (fields.length) v = setAt(v, ['fields', { element: pick(r, fields).name as string }], undefined); break
      case 3: { const key = pick(r, ['tax', 'note', 'email']); v = setAt(v, ['fields', { element: key }], field(r, key)); break }
      case 4: if (fields.length) v = setAt(v, ['fields', { element: pick(r, fields).name as string }, 'exclude'], chance(r, 0.5)); break
      case 5: o.grants = [...grants, grant(r, pick(r, ['a', 'b', 'c']))]; break
      case 6: if (grants.length) o.grants = grants.filter((_, i) => i !== Math.floor(r() * grants.length)); break
      case 7: o.mcp = { dmlTools: chance(r, 0.5) }; break
      default: o.tenantPolicy = chance(r, 0.5) ? undefined : { op: 'eq', field: 'tenant_id', value: pick(r, [1, 2, 3]) }
    }
  }
  return v as Obj
}

// The keys of a list the merge treats element by element: by name, else by
// the entity granted; null for any other list. Elements sharing a key form a
// group the merge treats as one value.
function listKeys(list: unknown[]): string[] | null {
  const keys: string[] = []
  for (const el of list) {
    if (typeof el !== 'object' || el === null || Array.isArray(el)) return null
    const o = el as Obj
    const key = typeof o.name === 'string' ? o.name : typeof o.entity === 'string' ? o.entity : null
    if (key === null) return null
    if (!keys.includes(key)) keys.push(key)
  }
  return keys
}

/** Every path to a value the merge does not descend into, in any of values. */
function leaves(values: unknown[], path: Path = [], out = new Map<string, Path>()): Map<string, Path> {
  const here = values.map((v) => getAt(v, path))
  const defined = here.filter((v) => v !== undefined)
  const group = path.length > 0 && typeof path[path.length - 1] !== 'string' && defined.some(Array.isArray)
  if (group) return out.set(JSON.stringify(path), path)
  if (defined.length && defined.every((v) => typeof v === 'object' && v !== null && !Array.isArray(v))) {
    for (const key of new Set(defined.flatMap((v) => Object.keys(v as Obj)))) leaves(values, [...path, key], out)
    return out
  }
  const lists = defined.map((v) => (Array.isArray(v) ? listKeys(v) : null))
  if (defined.length && lists.every((k) => k !== null)) {
    for (const key of new Set(lists.flat() as string[])) leaves(values, [...path, { element: key }], out)
    return out
  }
  out.set(JSON.stringify(path), path)
  return out
}

// Values at path compared as same() does in place: unset optional text such as
// mask equals empty, which needs the property name.
function sameAt(path: Path, a: unknown, b: unknown) {
  const last = path[path.length - 1]
  return typeof last === 'string' ? same({ [last]: a }, { [last]: b }) : same(a, b)
}

const under = (path: Path, prefix: Path) =>
  prefix.length <= path.length && JSON.stringify(path.slice(0, prefix.length)) === JSON.stringify(prefix)

export function sides(seed: number) {
  const r = rng(seed)
  const base = item(r)
  const local = chance(r, 0.1) ? base : edit(r, base)
  const next = chance(r, 0.1) ? base : edit(r, base)
  return { base, local, next }
}

function check(seed: number) {
  const r = rng(seed)
  const base = item(r)
  const local = chance(r, 0.1) ? base : edit(r, base)
  const next = chance(r, 0.1) ? base : edit(r, base)
  const { value, conflicts } = merge(base, local, next)
  const inConflict = (p: Path) => conflicts.some((c) => under(p, c.path))
  const where = `seed ${seed}`

  for (const c of conflicts) {
    // A conflict holds both sides as they are, differing, and keeps mine.
    expect(sameAt(c.path, c.mine, getAt(local, c.path)), where).toBe(true)
    expect(sameAt(c.path, c.theirs, getAt(next, c.path)), where).toBe(true)
    expect(sameAt(c.path, c.mine, c.theirs), where).toBe(false)
    expect(sameAt(c.path, getAt(value, c.path), c.mine), where).toBe(true)
  }
  for (const p of leaves([base, local, next]).values()) {
    if (inConflict(p)) continue
    const [b, l, n] = [getAt(base, p), getAt(local, p), getAt(next, p)]
    // A side's change survives unless the other changed the same value.
    const want = sameAt(p, l, b) ? n : l
    expect(sameAt(p, getAt(value, p), want), `${where} at ${JSON.stringify(p)}`).toBe(true)
    if (!sameAt(p, l, b) && !sameAt(p, n, b)) {
      expect(sameAt(p, l, n), `${where}: an unreported conflict at ${JSON.stringify(p)}`).toBe(true)
    }
  }
  // Taking theirs everywhere leaves no local value that changed the same place.
  const theirs = conflicts.reduce<unknown>((v, c) => setAt(v, c.path, c.theirs), value)
  for (const c of conflicts) expect(sameAt(c.path, getAt(theirs, c.path), getAt(next, c.path)), where).toBe(true)
  // Symmetric: swapping the sides swaps mine and theirs.
  const swapped = merge(base, next, local)
  const key = (c: ValueConflict) => JSON.stringify(c.path)
  expect(swapped.conflicts.map(key).sort(), where).toEqual(conflicts.map(key).sort())
}

describe('merge properties (random)', () => {
  it('holds for 3000 random edit pairs', () => {
    for (let seed = 1; seed <= 3000; seed++) check(seed)
  })

  it('is the identity where one side did not change', () => {
    for (let seed = 1; seed <= 300; seed++) {
      const r = rng(seed)
      const base = item(r)
      const changed = edit(r, base)
      // Equal up to unset optional text, which means the same as empty.
      for (const m of [merge(base, base, changed), merge(base, changed, base)]) {
        expect(same(m.value, changed), `seed ${seed}`).toBe(true)
        expect(m.conflicts).toEqual([])
      }
      expect(merge(base, changed, structuredClone(changed)).conflicts).toEqual([])
    }
  })
})

describe('merge edge cases', () => {
  const g = (entity: string, ...acts: string[]) => ({ entity, actions: acts })

  it.each([
    ['both add the same element', [], [{ name: 'x' }], [{ name: 'x' }], [{ name: 'x' }], 0],
    ['both add an element with different values', [], [{ name: 'x', a: 1 }], [{ name: 'x', a: 2 }], [{ name: 'x', a: 1 }], 1],
    ['one removes, the other keeps', [{ name: 'x' }], [], [{ name: 'x' }], [], 0],
    ['one removes, the other changes', [{ name: 'x', a: 1 }], [], [{ name: 'x', a: 2 }], [], 1],
    ['both remove', [{ name: 'x' }], [], [], [], 0],
    ['elements sharing a name merge as one group', [{ name: 'x' }], [{ name: 'x' }, { name: 'x' }], [{ name: 'y' }],
      [{ name: 'x' }, { name: 'x' }, { name: 'y' }], 1],
    ['a list of values is one value', ['a'], ['a', 'b'], ['a', 'c'], ['a', 'b'], 1],
    ['a second grant on one entity joins its group', [g('a', 'READ')], [g('a', 'READ'), g('a', 'CREATE')],
      [g('a', 'READ', 'AGGREGATE')], [g('a', 'READ'), g('a', 'CREATE')], 1],
    ['a group one side kept and the other changed takes the change', [g('a', 'READ'), g('a', 'CREATE')],
      [g('a', 'READ'), g('a', 'CREATE')], [g('a', 'READ')], [g('a', 'READ')], 0],
    ['local order wins, new elements are appended', [{ name: 'a' }, { name: 'b' }], [{ name: 'b' }, { name: 'a' }],
      [{ name: 'a' }, { name: 'b' }, { name: 'c' }], [{ name: 'b' }, { name: 'a' }, { name: 'c' }], 0],
  ])('%s', (_, base, local, next, want, conflicts) => {
    const m = merge(base, local, next)
    expect(m.value).toEqual(want)
    expect(m.conflicts).toHaveLength(conflicts as number)
  })

  it('treats empty and unset optional text alike, and ignores key order', () => {
    expect(merge({ description: 'a' }, { description: '' }, {}).conflicts).toEqual([])
    expect(merge({ a: 1, b: 2 }, { b: 2, a: 1 }, { a: 1, b: 3 })).toEqual({ value: { a: 1, b: 3 }, conflicts: [] })
  })

  it('reports a type change against a nested change at the value', () => {
    const m = merge({ p: { a: 1 } }, { p: 'x' }, { p: { a: 2 } })
    expect(m.conflicts).toEqual([{ path: ['p'], mine: 'x', theirs: { a: 2 } }])
  })

  it('handles a whole item added or removed on either side', () => {
    expect(merge(undefined, { name: 'r' }, undefined)).toEqual({ value: { name: 'r' }, conflicts: [] })
    expect(merge(undefined, undefined, { name: 'r' })).toEqual({ value: { name: 'r' }, conflicts: [] })
    expect(merge({ name: 'r' }, undefined, undefined)).toEqual({ value: undefined, conflicts: [] })
    expect(merge(undefined, { name: 'r', a: 1 }, { name: 'r', a: 2 }).conflicts).toHaveLength(1)
  })
})
