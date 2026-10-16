// The workspace at the scale of a large deployment, timed:
// `PERF=1 npx vitest run src/stores/workspace.perf.test.ts`.
import { beforeAll, describe, it } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { useWorkspace, type Sections } from './workspace'
import { entityId } from '@/lib/entityRefs'
import type { WorkspacePartsFragment, WorkspaceQuery } from '@/gql/graphql'

/** 3000 entities of 20 fields, 50 roles granting 60 entities each, 200 users. */
function large(): Sections {
  const entities = Array.from({ length: 3000 }, (_, i) => ({
    name: `t${i}`, datasource: 'shop', schema: `s${i % 10}`, primaryKey: ['c0'], description: `table ${i}`,
    fields: Array.from({ length: 20 }, (_, f) => ({ name: `c${f}`, description: `column ${f}` })),
    relationships: [],
  }))
  const roles = Array.from({ length: 50 }, (_, r) => ({
    name: `r${r}`,
    grants: Array.from({ length: 60 }, (_, g) => {
      const e = (r * 60 + g) % 3000
      return { entity: `s${e % 10}.t${e}`, actions: ['READ'] }
    }),
  }))
  const users = Array.from({ length: 200 }, (_, u) => ({ name: `u${u}`, roles: [`r${u % 50}`] }))
  return { entities, roles, users, settings: { cost: { maxRows: 100 } } } as unknown as Sections
}

/** The server's answer: every item of sections, or (changed) those given. */
function answer(version: number, sections: Sections | null, changed?: { kind: string; key: string; value: unknown }[]) {
  const all = () => sections && [
    ...sections.entities.map((e) => ({ kind: 'entity', key: entityId(e), value: e })),
    ...sections.roles.map((r) => ({ kind: 'role', key: r.name, value: r })),
    ...sections.users.map((u) => ({ kind: 'user', key: u.name, value: u })),
    { kind: 'settings', key: 'settings', value: sections.settings },
  ]
  return { base: '1', version, items: { complete: !changed, items: changed ?? all() }, conflicts: [], notes: [],
    taken: [], checkpoint: null, updatedBy: 'root' } as unknown as WorkspacePartsFragment
}

function loaded(): ReturnType<typeof useWorkspace> {
  setActivePinia(createPinia())
  const ws = useWorkspace()
  const sections = large()
  const base = { published: { id: '1' }, revision: { id: '1', contentHash: 'h', config: { datasources: [] }, sections } }
  ws.take(answer(1, sections), base as unknown as WorkspaceQuery)
  return ws
}

/** Runs fn n times and prints the mean time it took. */
function time(name: string, fn: () => void, n = 20) {
  it(name, () => {
    fn()
    const start = performance.now()
    for (let k = 0; k < n; k++) fn()
    console.log(`${name}: ${((performance.now() - start) / n).toFixed(2)} ms`)
  }, 120_000)
}

describe.skipIf(!process.env.PERF)('workspace of 3000 entities', () => {
  let ws: ReturnType<typeof useWorkspace>
  beforeAll(() => { ws = loaded() })
  let i = 0
  time('an edit, then what the console reads after it', () => {
    const e = ws.entities[i++ % 3000]
    ws.upsertEntity({ ...e, description: `edited ${i}` })
    void ws.changes.length
    void ws.unsavedItems().length
    void ws.contextKey
  })

  time('who can access each entity (overview, entity list)', () => {
    let n = 0
    for (const e of ws.entities) n += ws.accessTo(entityId(e)).roles.length
    if (!n) throw new Error('no access')
  })

  time('taking in every item the server answered', () => {
    ws.take(answer(ws.version + 1, large()))
  }, 5)

  time('taking in the items changed since (an edit written)', () => {
    const e = ws.entities[i++ % 3000]
    ws.upsertEntity({ ...e, description: `written ${i}` })
    const items = ws.unsavedItems()
    ws.wrote(items)
    ws.take(answer(ws.version + 1, null, items.map(({ kind, key, value }) => ({ kind, key, value }))))
    void ws.changes.length
  })
})
