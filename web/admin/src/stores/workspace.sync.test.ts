import { beforeEach, describe, expect, it } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { useWorkspace, type Sections } from './workspace'
import type { RoleInput, WorkspacePartsFragment, WorkspaceQuery } from '@/gql/graphql'

/** Revision id with the given roles. */
function revision(id: string, roles: RoleInput[]): WorkspaceQuery {
  return {
    published: { id },
    revision: {
      id, parent: null, state: 'PUBLISHED', contentHash: 'sha256:' + id, author: 'root', comment: '', createdAt: '',
      publishedAt: '',
      config: {
        datasources: [], entities: [], users: [], settings: {},
        roles: roles.map((r) => ({ name: r.name, description: r.description ?? null, members: [], grants: [] })),
      },
    },
  } as unknown as WorkspaceQuery
}

/** The workspace as the server answers it. */
function answer(version: number, roles: RoleInput[], extra: Partial<WorkspacePartsFragment> = {}): WorkspacePartsFragment {
  const sections: Sections = { entities: [], roles, users: [], settings: {} }
  return { base: '1', version, sections, updatedBy: 'root', taken: [], checkpoint: null, conflicts: [], notes: [], ...extra }
}

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
    expect(ws.pendingItems()).toEqual([])
  })

  it('writes the items edited here, with the values they were edited from', () => {
    const ws = read()
    ws.role('analyst')!.description = 'mine'
    ws.upsertRole({ name: 'support' })
    expect(ws.pendingItems()).toEqual([
      { kind: 'role', key: 'analyst', value: { ...analyst, description: 'mine' }, previous: analyst },
      { kind: 'role', key: 'support', value: { name: 'support' }, previous: null },
    ])
  })

  it('takes in what changed there, keeping what is unsent here', () => {
    const ws = read()
    ws.upsertRole({ name: 'mine' })
    ws.take(answer(2, [{ ...analyst, description: 'agent' }]))
    expect(ws.role('analyst')!.description).toBe('agent')
    expect(ws.role('mine')).toBeDefined()
    expect(ws.pendingItems().map((p) => p.key)).toEqual(['mine'])
  })

  it('keeps an unsent edit of an item changed there, still measured from what it was edited from', () => {
    const ws = read()
    ws.role('analyst')!.description = 'mine'
    ws.take(answer(2, [{ ...analyst, description: 'agent' }]))
    expect(ws.role('analyst')!.description).toBe('mine')
    expect(ws.pendingItems()).toEqual([
      { kind: 'role', key: 'analyst', value: { ...analyst, description: 'mine' }, previous: analyst },
    ])
  })

  it('keeps an edit made while its item was being written', () => {
    const ws = read()
    ws.role('analyst')!.description = 'first'
    const items = ws.pendingItems()
    ws.role('analyst')!.description = 'second' // while the write is in flight
    ws.wrote(items)
    ws.take(answer(2, [{ ...analyst, description: 'first' }]))
    expect(ws.role('analyst')!.description).toBe('second')
    expect(ws.pendingItems()).toEqual([{ kind: 'role', key: 'analyst', value: { ...analyst, description: 'second' },
      previous: { ...analyst, description: 'first' } }])
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
