import type { EntityInput } from '@/gql/schema'
import type { Table } from './schemaScans'

// What syncing an entity's fields with its table would do: the columns
// without a field, and the fields without a column. Only a table that was
// read says which columns are gone: without one (not scanned, not found)
// nothing is proposed, never "every field is gone".

export interface FieldSync {
  added: Table['columns']
  removed: string[]
}

export function fieldSync(entity: EntityInput | undefined, table: Table | null | undefined): FieldSync | null {
  if (!entity || !table) return null
  const declared = new Set((entity.fields ?? []).map((f) => f.name))
  const columns = new Set(table.columns.map((c) => c.name))
  return {
    added: table.columns.filter((c) => !declared.has(c.name)),
    removed: (entity.fields ?? []).map((f) => f.name).filter((n) => !columns.has(n)),
  }
}
