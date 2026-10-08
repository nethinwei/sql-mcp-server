import { reactive } from 'vue'
import { run } from '@/api/client'
import { TableCommentsQuery } from '@/api/ops'

// Database comments are the default descriptions of entities and fields: the
// server uses them whenever the configuration leaves a description empty.
// They are fetched once per table for the session and shown as placeholders.

export interface TableComments {
  description: string
  columns: Map<string, string>
}

interface EntityRef {
  name: string
  source?: string | null
  datasource?: string | null
  schema?: string | null
}

const cache = reactive(new Map<string, TableComments | null>())
const pending = new Set<string>()

function ref(e: EntityRef) {
  return { datasource: e.datasource || 'default', schema: e.schema || null, table: e.source || e.name }
}

function key(e: EntityRef) {
  const r = ref(e)
  return `${r.datasource}\u0000${r.schema ?? ''}\u0000${r.table}`
}

/** Loads the comments of the entities not loaded yet, in one request. */
export async function loadTableComments(entities: EntityRef[]) {
  const missing = [...new Map(entities.map((e) => [key(e), e])).entries()]
    .filter(([k]) => !cache.has(k) && !pending.has(k))
  if (!missing.length) return
  missing.forEach(([k]) => pending.add(k))
  try {
    const data = await run(TableCommentsQuery, { tables: missing.map(([, e]) => ref(e)) })
    data.tableComments.forEach((c, i) => {
      cache.set(missing[i][0], c && {
        description: c.description,
        columns: new Map(c.columns.map((col) => [col.name, col.description])),
      })
    })
  } catch {
    // Comments only fill placeholders; without them the page still works.
  } finally {
    missing.forEach(([k]) => pending.delete(k))
  }
}

/** The loaded comments of an entity's table, or null. */
export function tableComments(e: EntityRef): TableComments | null {
  return cache.get(key(e)) ?? null
}
