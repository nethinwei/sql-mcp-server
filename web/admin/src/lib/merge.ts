// Three-way merge of workspace items (entities, roles, users, settings) when
// the workspace moves onto a newer published revision. Objects merge key by
// key, and lists of named elements element by element: fields and
// relationships by name, grants by the entity they name. Elements sharing a
// key (several grants on one entity) have no identity of their own, so they
// merge as one group: matching them by position would pair a grant with
// another once one side removes one. A value both sides changed differently is a
// conflict: the merged value keeps the local one until the conflict is
// resolved explicitly (see the workspace's resolveConflict); nothing is
// overwritten silently.

/** A step into a value: an object property, or the element of a list with a key. */
export type PathStep = string | { element: string }
export type Path = PathStep[]

export interface ValueConflict {
  path: Path
  /** The local value, kept for now; undefined when removed locally. */
  mine: unknown
  /** The newer revision's value; undefined when removed there. */
  theirs: unknown
}

type Item = Record<string, unknown>
const isObject = (v: unknown): v is Item => typeof v === 'object' && v !== null && !Array.isArray(v)

// Optional text where empty and unset mean the same, as in the server model;
// tokenHash is excluded on purpose: an empty hash revokes the token.
const optionalText = new Set(['source', 'datasource', 'schema', 'kind', 'description', 'alias', 'mask'])

function normalize(v: unknown, key = ''): unknown {
  if (optionalText.has(key) && (v === '' || v === null)) return undefined
  if (Array.isArray(v)) return v.map((x) => normalize(x))
  if (!isObject(v)) return v
  const out: Item = {}
  for (const k of Object.keys(v).sort()) {
    const n = normalize(v[k], k)
    if (n !== undefined) out[k] = n
  }
  return out
}

/** Whether two values mean the same, regardless of key order or unset optional text. */
export function same(a: unknown, b: unknown): boolean {
  return JSON.stringify(normalize(a) ?? null) === JSON.stringify(normalize(b) ?? null)
}

/** The key of a list element: its name, or the entity it grants. */
function elementKey(el: unknown): string | null {
  if (!isObject(el)) return null
  if (typeof el.name === 'string') return el.name
  return typeof el.entity === 'string' ? el.entity : null
}

/**
 * A list of keyed elements as a map by key, or null for any other list;
 * elements sharing a key map to the list of them (a group).
 */
function keyed(v: unknown): Map<string, unknown> | null {
  if (!Array.isArray(v)) return null
  const groups = new Map<string, unknown[]>()
  for (const el of v) {
    const key = elementKey(el)
    if (key === null) return null
    groups.set(key, [...(groups.get(key) ?? []), el])
  }
  return new Map([...groups].map(([key, els]) => [key, els.length === 1 ? els[0] : els]))
}

/** Merges base → local and base → next; see the module comment. */
export function merge(base: unknown, local: unknown, next: unknown, path: Path = []):
  { value: unknown; conflicts: ValueConflict[] } {
  // Compared in place: an optional text property unset equals it empty.
  const last = path[path.length - 1]
  const eq = (a: unknown, b: unknown) => (typeof last === 'string' ? same({ [last]: a }, { [last]: b }) : same(a, b))
  if (eq(local, base)) return { value: next, conflicts: [] }
  if (eq(next, base) || eq(next, local)) return { value: local, conflicts: [] }
  if (isObject(base) && isObject(local) && isObject(next)) {
    const value: Item = {}
    const conflicts: ValueConflict[] = []
    for (const key of new Set([...Object.keys(local), ...Object.keys(next), ...Object.keys(base)])) {
      const m = merge(base[key], local[key], next[key], [...path, key])
      if (m.value !== undefined) value[key] = m.value
      conflicts.push(...m.conflicts)
    }
    return { value, conflicts }
  }
  const [b, l, n] = [keyed(base ?? []), keyed(local), keyed(next)]
  if (b && l && n) {
    // Local order, then what only the newer revision added.
    const order = [...l.keys(), ...[...n.keys()].filter((k) => !l.has(k))]
    const value: unknown[] = []
    const conflicts: ValueConflict[] = []
    for (const key of order) {
      const at = [...path, { element: key }]
      const [be, le, ne] = [b.get(key), l.get(key), n.get(key)]
      const m = [be, le, ne].some(Array.isArray) ? whole(be, le, ne, at) : merge(be, le, ne, at)
      if (Array.isArray(m.value)) value.push(...m.value)
      else if (m.value !== undefined) value.push(m.value)
      conflicts.push(...m.conflicts)
    }
    return { value, conflicts }
  }
  return { value: local, conflicts: [{ path, mine: local, theirs: next }] }
}

/** Merges a value as a whole: either side's change, or a conflict. */
function whole(base: unknown, local: unknown, next: unknown, path: Path) {
  if (same(local, base)) return { value: next, conflicts: [] }
  if (same(next, base) || same(next, local)) return { value: local, conflicts: [] }
  return { value: local, conflicts: [{ path, mine: local, theirs: next }] }
}

/** The value at path, or undefined; a group for elements sharing a key. */
export function getAt(root: unknown, path: Path): unknown {
  let v = root
  for (const step of path) {
    if (typeof step === 'string') v = isObject(v) ? v[step] : undefined
    else v = keyed(v)?.get(step.element)
  }
  return v
}

/**
 * Returns root with the value at path replaced, or removed when value is
 * undefined; missing objects on the way are created, and a missing list
 * element is appended.
 */
export function setAt(root: unknown, path: Path, value: unknown): unknown {
  if (path.length === 0) return value
  const [step, ...rest] = path
  if (typeof step === 'string') {
    const obj: Item = isObject(root) ? { ...root } : {}
    const v = setAt(obj[step], rest, value)
    if (v === undefined) delete obj[step]
    else obj[step] = v
    return obj
  }
  // The elements with the key are replaced where the first of them was.
  const list = Array.isArray(root) ? root : []
  const mine = list.filter((el) => elementKey(el) === step.element)
  const v = setAt(mine.length > 1 ? mine : mine[0], rest, value)
  const replacement = v === undefined ? [] : Array.isArray(v) ? v : [v]
  const at = list.findIndex((el) => elementKey(el) === step.element)
  const others = list.filter((el) => elementKey(el) !== step.element)
  const i = at < 0 ? others.length : list.slice(0, at).filter((el) => elementKey(el) !== step.element).length
  return [...others.slice(0, i), ...replacement, ...others.slice(i)]
}

/** A readable path, e.g. fields[email].mask. */
export function formatPath(path: Path): string {
  return path.map((s, i) => (typeof s === 'string' ? (i ? `.${s}` : s) : `[${s.element}]`)).join('')
}
