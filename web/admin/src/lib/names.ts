import { t } from '@/i18n'

// Role and user names: lower-case letters, digits, '-' and '_' (core/config).
const accessNameRe = /^[a-z0-9][a-z0-9_-]*$/

export function nameError(name: string, taken: string[]): string | null {
  if (!name) return t('common.required')
  if (!accessNameRe.test(name)) return t('names.invalid')
  if (taken.includes(name)) return t('names.taken')
  return null
}

interface Located {
  name: string
  source?: string | null
  schema?: string | null
  datasource?: string | null
}

/**
 * The namespace an entity is named in, "datasource · schema" (the datasource
 * alone without a schema): with its name, it identifies the entity.
 */
export function entityNamespace(e: Located): string {
  const ds = e.datasource || 'default'
  return e.schema ? `${ds} · ${e.schema}` : ds
}

/** The table an entity reads when it is named differently, else null. */
export function sourceIfRenamed(e: Located): string | null {
  return e.source && e.source !== e.name ? e.source : null
}

/**
 * Splits items into groups by namespace, in order of first appearance; one
 * group with an empty namespace when every item shares one, which needs no
 * heading.
 */
export function byNamespace<T extends Located>(items: T[]): { namespace: string; items: T[] }[] {
  const groups = new Map<string, T[]>()
  for (const item of items) {
    const ns = entityNamespace(item)
    const group = groups.get(ns)
    if (group) group.push(item)
    else groups.set(ns, [item])
  }
  if (groups.size === 1) return [{ namespace: '', items }]
  return [...groups].map(([namespace, items]) => ({ namespace, items }))
}
