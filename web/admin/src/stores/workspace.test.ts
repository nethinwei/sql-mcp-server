import { beforeEach, describe, expect, it } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { merge3, useWorkspace } from './workspace'
import type { WorkspaceQuery } from '@/gql/graphql'

function seeded() {
  const ws = useWorkspace()
  ws.$patch((s) => {
    s.baseId = '2'
    s.entities = [{ name: 'orders', fields: [{ name: 'id' }] }, { name: 'customers', fields: [{ name: 'id' }] }]
    s.roles = [{ name: 'analyst', grants: [{ entity: 'orders', actions: ['READ'] }, { entity: 'customers', actions: ['READ'] }] }]
    s.users = [{ name: 'alice', roles: ['analyst'], grants: [{ entity: 'orders', actions: ['READ'] }] }]
    s.settings = { cost: { maxRows: 10 } }
  })
  ws.baseline = JSON.stringify({ entities: ws.entities, roles: ws.roles, users: ws.users, settings: ws.settings })
  return ws
}

/** A loaded revision in the shape of WorkspaceQuery, with minimal items. */
function loaded(id: string, cfg: {
  entities: string[]
  roles: { name: string; grants: { entity: string; actions: string[] }[] }[]
  users: string[]
}): WorkspaceQuery {
  return {
    published: { id },
    revision: {
      id, parent: null, state: 'PUBLISHED', contentHash: 'sha256:' + id, author: 'root', comment: '',
      createdAt: '', publishedAt: '',
      config: {
        datasources: [],
        entities: cfg.entities.map((name) => ({
          name, source: name, datasource: 'default', kind: 'table', primaryKey: [], params: [],
          mcp: { dmlTools: true, customTool: false, trustedProcedure: false },
          fields: [{ name: 'id', exclude: false }], relationships: [],
        })),
        roles: cfg.roles.map((r) => ({
          name: r.name, members: [],
          grants: r.grants.map((g) => ({ ...g, fieldsRestricted: false, readFields: [], writeFields: [] })),
        })),
        users: cfg.users.map((name) => ({ name, roles: [], disabled: false, hasToken: true, grants: [] })),
        settings: {},
      },
    },
  } as unknown as WorkspaceQuery
}

describe('rebase', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('re-applies unsaved changes onto a newer revision and reports conflicts', () => {
    const ws = useWorkspace()
    ws.apply(loaded('3', {
      entities: ['orders'], roles: [{ name: 'analyst', grants: [{ entity: 'orders', actions: ['READ'] }] }], users: [],
    }), '3')
    ws.role('analyst')!.description = 'mine'
    ws.upsertEntity({ name: 'refunds' })

    const conflicts = ws.rebaseOnto(loaded('4', {
      entities: ['orders'],
      roles: [{ name: 'analyst', grants: [{ entity: 'orders', actions: ['READ', 'AGGREGATE'] }] }],
      users: ['bob'],
    }), '4')

    expect(ws.baseId).toBe('4')
    expect(ws.basePublished).toBe('4')
    // Different properties of the same role merge without a conflict.
    expect(conflicts).toEqual([])
    expect(ws.user('bob')).toBeDefined()
    expect(ws.role('analyst')!.description).toBe('mine')
    expect(ws.role('analyst')!.grants![0].actions).toEqual(['READ', 'AGGREGATE'])
    expect(ws.changes.map((c) => `${c.kind}:${c.name}`).sort()).toEqual(['entity:refunds', 'role:analyst'])
  })
})

describe('merge3', () => {
  it('reports a property both sides changed differently and keeps the local value', () => {
    const base = { name: 'r', description: 'a', grants: [1] }
    expect(merge3(base, { ...base, description: 'mine' }, { ...base, description: 'theirs', grants: [2] }))
      .toEqual({ value: { name: 'r', description: 'mine', grants: [2] }, conflict: true })
    expect(merge3(base, { ...base, description: 'same' }, { ...base, description: 'same' }).conflict).toBe(false)
  })

  it('applies a local property removal and treats a concurrent removal as a conflict', () => {
    const base = { name: 'r', description: 'a' }
    expect(merge3(base, { name: 'r' }, { ...base, extra: 1 })).toEqual({ value: { name: 'r', extra: 1 }, conflict: false })
    expect(merge3(base, { ...base, description: 'b' }, undefined).conflict).toBe(true)
    expect(merge3(undefined, { name: 'r' }, undefined)).toEqual({ value: { name: 'r' }, conflict: false })
  })
})

describe('workspace', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('tracks added, removed and modified items', () => {
    const ws = seeded()
    expect(ws.dirty).toBe(false)
    ws.upsertEntity({ name: 'refunds' })
    ws.role('analyst')!.description = 'changed'
    ws.removeUser('alice')
    ws.setSettings({ cost: { maxRows: 20 } })
    expect(ws.changes).toEqual(expect.arrayContaining([
      { kind: 'entity', name: 'refunds', type: 'added' },
      { kind: 'role', name: 'analyst', type: 'modified' },
      { kind: 'user', name: 'alice', type: 'removed' },
      { kind: 'settings', name: 'settings', type: 'modified' },
    ]))
    ws.discard()
    expect(ws.dirty).toBe(false)
  })

  it('treats cleared optional text as unchanged but not a revoked token', () => {
    const ws = seeded()
    ws.entity('orders')!.fields![0].alias = 'order_id'
    ws.entity('orders')!.fields![0].alias = ''
    ws.role('analyst')!.description = ''
    expect(ws.dirty).toBe(false)
    ws.user('alice')!.tokenHash = ''
    expect(ws.changes).toEqual([{ kind: 'user', name: 'alice', type: 'modified' }])
  })

  it('reverts single changes', () => {
    const ws = seeded()
    ws.role('analyst')!.description = 'changed'
    ws.removeUser('alice')
    ws.upsertEntity({ name: 'refunds' })
    ws.revert({ kind: 'role', name: 'analyst' })
    expect(ws.changes.map((c) => c.name).sort()).toEqual(['alice', 'refunds'])
    ws.revert({ kind: 'user', name: 'alice' })
    ws.revert({ kind: 'entity', name: 'refunds' })
    expect(ws.dirty).toBe(false)
    expect(ws.user('alice')!.roles).toEqual(['analyst'])
  })

  it('imports entities and grants them to existing and new roles', () => {
    const ws = seeded()
    const dropped = ws.importEntities([
      { name: 'refunds', relationships: [
        { name: 'orders', target: 'orders', cardinality: 'belongs-to', joinOn: { order_id: 'id' } },
        { name: 'ghost', target: 'ghost', cardinality: 'belongs-to', joinOn: { ghost_id: 'id' } },
      ] },
    ], ['analyst', 'support'], ['READ'])
    expect(dropped).toBe(1)
    expect(ws.entity('refunds')!.relationships!.map((r) => r.target)).toEqual(['orders'])
    expect(ws.role('analyst')!.grants!.at(-1)).toEqual({ entity: 'refunds', actions: ['READ'] })
    expect(ws.role('support')!.grants).toEqual([{ entity: 'refunds', actions: ['READ'] }])
    expect(ws.changes.map((c) => `${c.kind}:${c.name}:${c.type}`).sort())
      .toEqual(['entity:refunds:added', 'role:analyst:modified', 'role:support:added'])
  })

  it('imports without grants when no actions are chosen', () => {
    const ws = seeded()
    ws.importEntities([{ name: 'refunds' }], ['analyst'], [])
    expect(ws.role('analyst')!.grants!.some((g) => g.entity === 'refunds')).toBe(false)
  })

  it('removes grants with their entity and roles from users', () => {
    const ws = seeded()
    ws.removeEntity('orders')
    expect(ws.role('analyst')!.grants!.map((g) => g.entity)).toEqual(['customers'])
    expect(ws.user('alice')!.grants).toEqual([])
    ws.removeRole('analyst')
    expect(ws.user('alice')!.roles).toEqual([])
  })

  it('renames roles on their members and manages membership', () => {
    const ws = seeded()
    ws.upsertRole({ ...ws.role('analyst')!, name: 'cn_analyst' }, 'analyst')
    expect(ws.user('alice')!.roles).toEqual(['cn_analyst'])
    ws.setRoleMembers('cn_analyst', [])
    expect(ws.user('alice')!.roles).toEqual([])
  })

  it('produces a draft against the base revision', () => {
    const ws = seeded()
    expect(ws.draft.base).toBe('2')
    expect(ws.draft.entities).toHaveLength(2)
  })
})
