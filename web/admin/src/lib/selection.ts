import type { CatalogFilter } from '@/gql/graphql'
import { scopeOf, type Plan } from './plan'

// Selecting tables across pages of a catalog searched on the server. A
// selection is either tables picked one by one (refs), or every table a
// filter matches with the tables unpicked from it: selecting thousands of
// tables never moves their refs around. A filter selection is dynamic: it is
// whatever the filter matches when it is acted on. When new scans or
// workspace edits change how many tables it matches, the new count must be
// confirmed before acting on it.

export type Selection =
  | { mode: 'refs'; refs: Set<string> }
  | {
    mode: 'filter'; filter: CatalogFilter; excluded: Set<string>
    /** What the filter matches now, and what the administrator confirmed. */
    total: number; confirmed: number
  }

export const emptySelection = (): Selection => ({ mode: 'refs', refs: new Set() })

/** A key equal for filters selecting the same tables. */
export function filterKey(f: CatalogFilter): string {
  const norm = (xs?: readonly string[] | null) => (xs && xs.length ? [...xs].sort() : undefined)
  return JSON.stringify({
    d: norm(f.datasources), s: norm(f.schemas), t: norm(f.tables), q: f.search?.trim() || undefined,
    st: norm(f.status as string[] | undefined), dr: f.drift || undefined,
  })
}

/** How many tables are selected. */
export function selectedCount(s: Selection): number {
  return s.mode === 'refs' ? s.refs.size : Math.max(s.total - s.excluded.size, 0)
}

/**
 * Whether a table shown under filter f is selected; null when it cannot be
 * told (a filter selection seen through another filter).
 */
export function isSelected(s: Selection, f: CatalogFilter, ref: string): boolean | null {
  if (s.mode === 'refs') return s.refs.has(ref)
  return filterKey(f) === filterKey(s.filter) ? !s.excluded.has(ref) : null
}

/**
 * The selection with the tables of a page (refs) set to the checked ones
 * among them, as seen under filter f; unchanged when it cannot be adjusted
 * there.
 */
export function checkPage(s: Selection, f: CatalogFilter, page: string[], checked: Set<string>): Selection {
  if (s.mode === 'refs') {
    const refs = new Set(s.refs)
    for (const ref of page) {
      if (checked.has(ref)) refs.add(ref)
      else refs.delete(ref)
    }
    return { mode: 'refs', refs }
  }
  if (filterKey(f) !== filterKey(s.filter)) return s
  const excluded = new Set(s.excluded)
  for (const ref of page) {
    if (checked.has(ref)) excluded.delete(ref)
    else excluded.add(ref)
  }
  return { ...s, excluded }
}

/** Every table filter f matches (total of them). */
export function selectMatched(f: CatalogFilter, total: number): Selection {
  return { mode: 'filter', filter: JSON.parse(JSON.stringify(f)) as CatalogFilter, total, confirmed: total, excluded: new Set() }
}

/** The selection with what its filter matches now. */
export function withTotal(s: Selection, total: number): Selection {
  return s.mode === 'filter' && s.total !== total ? { ...s, total } : s
}

/** Whether the count of a filter selection changed since it was confirmed. */
export function needsConfirm(s: Selection): boolean {
  return s.mode === 'filter' && s.total !== s.confirmed
}

/** The selection with its current count confirmed. */
export function confirm(s: Selection): Selection {
  return s.mode === 'filter' ? { ...s, confirmed: s.total } : s
}

/** The plan scope and exclusions selecting what s selects. */
export function planOf(s: Selection): Plan {
  if (s.mode === 'refs') return { scope: { refs: [...s.refs] } }
  return { scope: scopeOf(s.filter), exclude: s.excluded.size ? [...s.excluded] : undefined }
}

/**
 * The selected tables the filter hides, given every table it matches; none
 * while the matches are unknown or for a filter selection.
 */
export function hiddenSelected(s: Selection, matched: string[] | null): string[] {
  if (s.mode !== 'refs' || !matched) return []
  const visible = new Set(matched)
  return [...s.refs].filter((ref) => !visible.has(ref))
}

/** Rows as CSV, quoting every cell. */
export function toCSV(rows: (string | number)[][]): string {
  return rows.map((r) => r.map((c) => `"${String(c).replace(/"/g, '""')}"`).join(',')).join('\n') + '\n'
}

/** Offers text as a file download. */
export function download(name: string, text: string, type = 'text/csv') {
  const url = URL.createObjectURL(new Blob(['﻿' + text], { type: `${type};charset=utf-8` }))
  const a = document.createElement('a')
  a.href = url
  a.download = name
  a.click()
  URL.revokeObjectURL(url)
}
