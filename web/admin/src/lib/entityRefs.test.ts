import { describe, expect, it } from 'vitest'
import cases from '../../../../core/config/testdata/entity_refs.json'
import { EntityIndex, entityId } from './entityRefs'

describe('entity references (shared with the server)', () => {
  const entities = cases.entities.map((e) => ({ name: e.name, datasource: e.datasource, schema: e.schema }))
  const index = new EntityIndex(entities)

  it.each(cases.cases)('$ref', (c: { ref: string; want?: string; candidates?: string[] }) => {
    const want = c.want ? { id: c.want } : c.candidates ? { candidates: c.candidates } : null
    expect(index.resolve(c.ref)).toEqual(want)
  })

  it('names each entity by its shortest unambiguous reference', () => {
    for (const e of entities) expect(index.shortName(entityId(e))).toBe((cases.shortNames as Record<string, string>)[entityId(e)])
  })
})
