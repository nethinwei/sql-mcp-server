import { beforeEach, describe, expect, it } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { useWorkspace } from './workspace'
import type { WorkspacePartsFragment, WorkspaceQuery } from '@/gql/graphql'
import type { RoleInput } from '@/gql/schema'

/** Revision id with the given roles. */
function revision(id: string, roles: RoleInput[]): WorkspaceQuery {
  return {
    published: { id },
    revision: {
      id, parent: null, state: 'PUBLISHED', contentHash: 'sha256:' + id, author: 'root', comment: '', createdAt: '',
      publishedAt: '',
      config: { datasources: [] },
      sections: {
        entities: [], users: [], settings: {},
        roles: roles.map((r) => ({ name: r.name, ...(r.description ? { description: r.description } : {}), grants: [] })),
      },
    },
  } as unknown as WorkspaceQuery
}

type Item = WorkspacePartsFragment['items']['items'][number]

/** The workspace as the server answers it, every item. */
function answer(version: number, roles: RoleInput[], extra: Partial<WorkspacePartsFragment> = {}): WorkspacePartsFragment {
  const items: Item[] = [...roles.map((r) => ({ kind: 'role', key: r.name, value: r })),
    { kind: 'settings', key: 'settings', value: {} }]
  return { base: '1', version, items: { complete: true, items }, updatedBy: 'root', taken: [], checkpoint: null,
    conflicts: [], notes: [], ...extra }
}

/** The workspace as the server answers it, the roles changed since (null: removed). */
function changed(version: number, roles: Record<string, RoleInput | null>): WorkspacePartsFragment {
  const items = Object.entries(roles).map(([key, value]) => ({ kind: 'role', key, value }))
  return { ...answer(version, []), items: { complete: false, items } }
}

/** An answer of every role, or of those changed since the copy. */
const answers = [
  { name: 'every item', of: (version: number, roles: RoleInput[]) => answer(version, roles) },
  { name: 'items changed', of: (version: number, roles: RoleInput[]) =>
    changed(version, Object.fromEntries(roles.map((r) => [r.name, r]))) },
]

const analyst = { name: 'analyst', grants: [] }

// The workspace is the server's: edits here are what differs from the copy
// last read, written item by item with the value each was edited from (the
// server merges what was written there meanwhile); every other item is
// taken in as the server answers it.
describe('the workspace kept on the server', () => {
  beforeEach(() => setActivePinia(createPinia()))

  function read() {
    const ws = useWorkspace()
    ws.take(answer(1, [analyst]), revision('1', [analyst]))
    return ws
  }

  it('is read whole the first time, with nothing to write', () => {
    const ws = read()
    expect(ws.baseId).toBe('1')
    expect(ws.version).toBe(1)
    expect(ws.roles.map((r) => r.name)).toEqual(['analyst'])
    expect(ws.unsavedItems()).toEqual([])
  })

  // A rename made while the write renaming the item is in flight follows
  // that write: once answered, the server knows the item by the key it gave
  // it; failed, by the first key still.
  it('renames again while a rename is being written', () => {
    const ws = read()
    ws.upsertRole({ ...analyst, name: 'lead' }, 'analyst')
    const sent = ws.unsavedItems()
    ws.upsertRole({ ...analyst, name: 'head' }, 'lead')
    ws.wrote(sent)
    expect(ws.unsavedItems()).toEqual([
      { kind: 'role', key: 'head', value: { ...analyst, name: 'head' }, previous: null, renamedFrom: 'lead' },
      { kind: 'role', key: 'lead', value: null, previous: { ...analyst, name: 'lead' } },
    ])
  })

  it('renames back while a rename is being written', () => {
    const ws = read()
    ws.upsertRole({ ...analyst, name: 'lead' }, 'analyst')
    const sent = ws.unsavedItems()
    ws.upsertRole({ ...analyst }, 'lead')
    expect(ws.unsavedItems()).toEqual([])
    ws.wrote(sent)
    expect(ws.unsavedItems()).toEqual([
      { kind: 'role', key: 'analyst', value: analyst, previous: null, renamedFrom: 'lead' },
      { kind: 'role', key: 'lead', value: null, previous: { ...analyst, name: 'lead' } },
    ])
  })

  it('renames from the first key when the write renaming the item failed', () => {
    const ws = read()
    ws.upsertRole({ ...analyst, name: 'lead' }, 'analyst')
    ws.unsavedItems()
    ws.upsertRole({ ...analyst, name: 'head' }, 'lead')
    expect(ws.unsavedItems()).toEqual([
      { kind: 'role', key: 'head', value: { ...analyst, name: 'head' }, previous: null, renamedFrom: 'analyst' },
      { kind: 'role', key: 'analyst', value: null, previous: analyst },
    ])
  })

  // The server moves the references to a renamed item (x/admin/configapp
  // reconcile); the console writes the rename as such.
  it('writes a rename with the key the server knows the item by', () => {
    const ws = read()
    ws.upsertRole({ ...analyst, name: 'lead' }, 'analyst')
    ws.upsertRole({ ...analyst, name: 'head' }, 'lead')
    ws.upsertRole({ name: 'support' })
    ws.upsertRole({ name: 'helper' }, 'support')
    const items = ws.unsavedItems()
    expect(items).toEqual([
      { kind: 'role', key: 'head', value: { ...analyst, name: 'head' }, previous: null, renamedFrom: 'analyst' },
      { kind: 'role', key: 'helper', value: { name: 'helper' }, previous: null },
      { kind: 'role', key: 'analyst', value: null, previous: analyst },
    ])
    ws.wrote(items)
    expect(ws.renamed).toEqual({})
  })

  it('writes the items edited here, with the values they were edited from', () => {
    const ws = read()
    ws.role('analyst')!.description = 'mine'
    ws.upsertRole({ name: 'support' })
    expect(ws.unsavedItems()).toEqual([
      { kind: 'role', key: 'analyst', value: { ...analyst, description: 'mine' }, previous: analyst },
      { kind: 'role', key: 'support', value: { name: 'support' }, previous: null },
    ])
  })

  it.each(answers)('takes in what changed there, keeping what is unsent here ($name)', (a) => {
    const ws = read()
    ws.upsertRole({ name: 'mine' })
    ws.take(a.of(2, [{ ...analyst, description: 'agent' }]))
    expect(ws.role('analyst')!.description).toBe('agent')
    expect(ws.role('mine')).toBeDefined()
    expect(ws.unsavedItems().map((p) => p.key)).toEqual(['mine'])
  })

  it.each(answers)('keeps an unsent edit of an item changed there, measured from what it was edited from ($name)',
    (a) => {
      const ws = read()
      ws.role('analyst')!.description = 'mine'
      ws.take(a.of(2, [{ ...analyst, description: 'agent' }]))
      expect(ws.role('analyst')!.description).toBe('mine')
      expect(ws.unsavedItems()).toEqual([
        { kind: 'role', key: 'analyst', value: { ...analyst, description: 'mine' }, previous: analyst },
      ])
    })

  it.each(answers)('keeps an edit made while its item was being written ($name)', (a) => {
    const ws = read()
    ws.role('analyst')!.description = 'first'
    const items = ws.unsavedItems()
    ws.role('analyst')!.description = 'second' // while the write is in flight
    ws.wrote(items)
    ws.take(a.of(2, [{ ...analyst, description: 'first' }]))
    expect(ws.role('analyst')!.description).toBe('second')
    expect(ws.unsavedItems()).toEqual([{ kind: 'role', key: 'analyst', value: { ...analyst, description: 'second' },
      previous: { ...analyst, description: 'first' } }])
  })

  it('takes in items added and removed there, leaving the others as they are', () => {
    const ws = read()
    ws.take(changed(2, { support: { name: 'support' }, helper: { name: 'helper' } }))
    const kept = ws.role('analyst')
    ws.take(changed(3, { support: null, helper: { name: 'helper', description: 'h' } }))
    expect(ws.roles.map((r) => r.name)).toEqual(['analyst', 'helper'])
    expect(ws.role('helper')!.description).toBe('h')
    expect(ws.role('analyst')).toBe(kept)
    expect(ws.version).toBe(3)
    expect(ws.unsavedItems()).toEqual([])
  })

  it('refuses the items changed since a version for a workspace not held at one', () => {
    expect(() => useWorkspace().take(changed(2, { support: { name: 'support' } }))).toThrow()
  })

  it('shows the conflicts, notes and drafts the server keeps', () => {
    const ws = useWorkspace()
    const conflict = { id: 'c1', kind: 'role', key: 'analyst', path: 'description', mine: 'mine', mineGone: false,
      theirs: 'theirs', theirsGone: false, source: 'draft', fromId: '7', fromBy: 'alice' }
    ws.take(answer(1, [analyst], { conflicts: [conflict], taken: ['7'], checkpoint: '9',
      notes: [{ source: 'agent', by: 'laptop', text: '导入' }] }), revision('1', [analyst]))
    expect(ws.openConflicts).toEqual([conflict])
    expect(ws.taken).toEqual(['7'])
    expect(ws.notes.map((n) => n.text)).toEqual(['导入'])
    // A saved draft is offered only while the workspace has something to publish.
    expect(ws.savedDraftId).toBeNull()
    ws.role('analyst')!.description = 'mine'
    expect(ws.savedDraftId).toBe('9')
  })
})
