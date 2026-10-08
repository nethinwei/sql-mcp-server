import { t } from '@/i18n'

// Row filters use the rowPolicies JSON shape:
//   {op, field, value} | {and: Filter[]} | {or: Filter[]}

export type Op = 'eq' | 'ne' | 'gt' | 'gte' | 'lt' | 'lte' | 'in' | 'not_in' | 'like' | 'is_null' | 'is_not_null'

export interface Condition {
  op: Op
  field: string
  value?: unknown
}
export interface Group {
  and?: Filter[]
  or?: Filter[]
}
export type Filter = Condition | Group

/** Operators; labels are message keys under filter.ops. */
export const ops: { value: Op; arity: 0 | 1 | 'list' }[] = [
  { value: 'eq', arity: 1 },
  { value: 'ne', arity: 1 },
  { value: 'gt', arity: 1 },
  { value: 'gte', arity: 1 },
  { value: 'lt', arity: 1 },
  { value: 'lte', arity: 1 },
  { value: 'in', arity: 'list' },
  { value: 'not_in', arity: 'list' },
  { value: 'like', arity: 1 },
  { value: 'is_null', arity: 0 },
  { value: 'is_not_null', arity: 0 },
]

const symbols: Record<Op, string> = {
  eq: '=', ne: '≠', gt: '>', gte: '≥', lt: '<', lte: '≤', in: '∈', not_in: '∉', like: '~', is_null: '', is_not_null: '',
}

export function isCondition(f: Filter): f is Condition {
  return typeof (f as Condition).op === 'string'
}

const subjectRe = /^\$\{subject\.([^}]+)\}$/

function describeValue(v: unknown): string {
  if (typeof v === 'string') {
    const m = subjectRe.exec(v)
    return m ? `「${t('filter.currentUser')}.${m[1]}」` : JSON.stringify(v)
  }
  if (Array.isArray(v)) return `[${v.map(describeValue).join(', ')}]`
  return String(v)
}

/** A compact human-readable rendering, e.g. `region = "CN" and amount > 100`. */
export function describeFilter(f: Filter | null | undefined): string {
  if (!f) return t('filter.allRows')
  if (isCondition(f)) {
    if (f.op === 'is_null') return `${f.field} ${t('filter.isNull')}`
    if (f.op === 'is_not_null') return `${f.field} ${t('filter.isNotNull')}`
    return `${f.field} ${symbols[f.op]} ${describeValue(f.value)}`
  }
  const [joiner, items] = f.and ? [t('filter.and'), f.and] : [t('filter.or'), f.or ?? []]
  if (items.length === 0) return t('filter.allRows')
  const parts = items.map((i) => (isCondition(i) ? describeFilter(i) : `(${describeFilter(i)})`))
  return parts.join(joiner)
}

/** Fields a filter references. */
export function filterFields(f: Filter | null | undefined): string[] {
  if (!f) return []
  if (isCondition(f)) return [f.field]
  return [...(f.and ?? []), ...(f.or ?? [])].flatMap(filterFields)
}
