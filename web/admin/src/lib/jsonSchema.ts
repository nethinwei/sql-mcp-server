// The JSON Schema subset core/config/schema.json uses (it is generated from
// the Go configuration structs): type, properties, additionalProperties,
// items, enum, minimum/maximum, minItems, minLength, pattern, required, local
// $ref, default, plus x-restart, x-format and localized x-description-<locale>
// / x-title-<locale>. Enough for completion, hover and checks in the settings
// editor without a general-purpose validator.

export type Schema = {
  $ref?: string
  type?: string
  title?: string
  description?: string
  default?: unknown
  'x-restart'?: boolean
  'x-format'?: string
  'x-minimum'?: string
  'x-maximum'?: string
  properties?: Record<string, Schema>
  additionalProperties?: Schema | boolean
  items?: Schema
  enum?: unknown[]
  examples?: unknown[]
  minimum?: number
  maximum?: number
  minItems?: number
  minLength?: number
  pattern?: string
  required?: string[]
  $defs?: Record<string, Schema>
  [extension: `x-${string}`]: unknown
}

export type Path = (string | number)[]

/** Follows local $refs ("#/$defs/name"); keywords next to a $ref win. */
export function resolve(root: Schema, s: Schema | undefined): Schema | undefined {
  let cur = s
  for (let i = 0; cur?.$ref && i < 16; i++) {
    const { $ref, ...rest } = cur
    const target = root.$defs?.[$ref.replace('#/$defs/', '')]
    cur = target && { ...target, ...rest }
  }
  return cur
}

/** The schema of one child of s. */
export function child(root: Schema, s: Schema | undefined, key: string | number): Schema | undefined {
  const r = resolve(root, s)
  if (!r) return undefined
  if (typeof key === 'number') return resolve(root, r.items)
  const ap = r.additionalProperties
  return resolve(root, r.properties?.[key] ?? (typeof ap === 'object' ? ap : undefined))
}

export function at(root: Schema, s: Schema | undefined, path: Path): Schema | undefined {
  return path.reduce<Schema | undefined>((cur, key) => child(root, cur, key), resolve(root, s))
}

/** Whether keys outside `properties` are unknown (the server rejects them). */
export function closed(s: Schema): boolean {
  return s.additionalProperties === false
}

/** Whether s or anything below it needs a restart when changed. */
export function restartsBelow(root: Schema, s: Schema | undefined, depth = 0): boolean {
  const r = resolve(root, s)
  if (!r || depth > 8) return false
  if (r['x-restart']) return true
  return Object.values(r.properties ?? {}).some((p) => restartsBelow(root, p, depth + 1))
}

/**
 * Returns a copy of root whose description and title are in locale, taken
 * from x-description-<locale> / x-title-<locale> when present.
 */
export function localize(root: Schema, locale: string): Schema {
  const visit = (s: Schema): Schema => {
    const out: Schema = { ...s }
    const d = s[`x-description-${locale}`]
    const t = s[`x-title-${locale}`]
    if (typeof d === 'string') out.description = d
    if (typeof t === 'string') out.title = t
    if (s.properties) out.properties = Object.fromEntries(Object.entries(s.properties).map(([k, v]) => [k, visit(v)]))
    if (typeof s.additionalProperties === 'object') out.additionalProperties = visit(s.additionalProperties)
    if (s.items) out.items = visit(s.items)
    if (s.$defs) out.$defs = Object.fromEntries(Object.entries(s.$defs).map(([k, v]) => [k, visit(v)]))
    return out
  }
  return visit(root)
}

export interface Problem {
  path: Path
  /** Message code: type, enum, minimum, maximum, minItems, minLength, pattern, required, unknown. */
  code: string
  params: Record<string, unknown>
  /** On the key rather than the value (unknown and required properties). */
  onKey?: boolean
  severity: 'error' | 'warning'
}

function typeOf(v: unknown): string {
  if (v === null) return 'null'
  if (Array.isArray(v)) return 'array'
  if (typeof v === 'number') return Number.isInteger(v) ? 'integer' : 'number'
  return typeof v
}

function typeMatches(want: string, got: string) {
  return want === got || (want === 'number' && got === 'integer')
}

export function validate(root: Schema, schema: Schema | undefined, value: unknown, path: Path = []): Problem[] {
  const s = resolve(root, schema)
  if (!s) return []
  const out: Problem[] = []
  const got = typeOf(value)
  const problem = (code: string, params: Record<string, unknown> = {}) =>
    out.push({ path, code, params, severity: 'error' })
  if (s.type && !typeMatches(s.type, got)) {
    problem('type', { type: s.type })
    return out
  }
  if (s.enum && !s.enum.some((e) => e === value)) problem('enum', { values: s.enum.join(', ') })
  if (typeof value === 'number') {
    if (s.minimum !== undefined && value < s.minimum) problem('minimum', { min: s.minimum })
    if (s.maximum !== undefined && value > s.maximum) problem('maximum', { max: s.maximum })
  }
  if (typeof value === 'string') {
    if (s.minLength !== undefined && value.length < s.minLength) problem('minLength', { min: s.minLength })
    if (s.pattern && !new RegExp(s.pattern).test(value)) problem('pattern', { pattern: s.pattern })
  }
  if (Array.isArray(value)) {
    if (s.minItems !== undefined && value.length < s.minItems) problem('minItems', { min: s.minItems })
    value.forEach((v, i) => out.push(...validate(root, s.items, v, [...path, i])))
  }
  if (got === 'object') {
    const obj = value as Record<string, unknown>
    for (const name of s.required ?? []) {
      if (!(name in obj)) problem('required', { name })
    }
    for (const [k, v] of Object.entries(obj)) {
      const sub = child(root, s, k)
      if (sub) out.push(...validate(root, sub, v, [...path, k]))
      else if (closed(s)) out.push({ path: [...path, k], code: 'unknown', params: { name: k }, onKey: true, severity: 'error' })
    }
  }
  return out
}

export interface Context {
  /** Path of the innermost object or array around the position. */
  container: Path
  /** Whether a property name or a value goes at the position. */
  expect: 'key' | 'value'
  /** The property or array index whose value is written, when expect is value. */
  key?: string | number
  /** Where the token under the position starts (its opening quote, if any). */
  from: number
  /** The text typed so far, without the opening quote. */
  prefix: string
}

interface Frame { kind: 'obj' | 'arr'; key: string | null; state: 'key' | 'colon' | 'value'; index: number }

/**
 * Scans text up to pos with a tolerant tokenizer (the document may be
 * incomplete while typing) and reports what goes at pos.
 */
export function contextAt(text: string, pos: number): Context {
  const stack: Frame[] = []
  let i = 0
  let open: { from: number } | null = null
  const top = () => stack[stack.length - 1]
  while (i < pos) {
    const c = text[i]
    if (c === '"') {
      const from = i
      i++
      while (i < pos && text[i] !== '"') i += text[i] === '\\' ? 2 : 1
      if (i >= pos) {
        open = { from }
        break
      }
      const str = JSON.parse(text.slice(from, i + 1)) as string
      const f = top()
      if (f?.kind === 'obj' && f.state === 'key') {
        f.key = str
        f.state = 'colon'
      }
      i++
      continue
    }
    if (c === '{') stack.push({ kind: 'obj', key: null, state: 'key', index: 0 })
    else if (c === '[') stack.push({ kind: 'arr', key: null, state: 'value', index: 0 })
    else if (c === '}' || c === ']') stack.pop()
    else if (c === ':' && top()?.kind === 'obj') top().state = 'value'
    else if (c === ',') {
      const f = top()
      if (f?.kind === 'obj') {
        f.state = 'key'
        f.key = null
      } else if (f) f.index++
    }
    i++
  }
  const f = top()
  // Each enclosing frame contributes the child being written inside it.
  const container: Path = stack.slice(0, -1).map((fr) => (fr.kind === 'obj' ? fr.key ?? '' : fr.index))
  let from = pos
  let prefix = ''
  if (open) {
    from = open.from
    prefix = text.slice(open.from + 1, pos)
  } else {
    prefix = /[\w.-]*$/.exec(text.slice(0, pos))?.[0] ?? ''
    from = pos - prefix.length
  }
  if (!f) return { container, expect: 'value', from, prefix }
  if (f.kind === 'arr') return { container, expect: 'value', key: f.index, from, prefix }
  return f.state === 'value'
    ? { container, expect: 'value', key: f.key ?? '', from, prefix }
    : { container, expect: 'key', from, prefix }
}
