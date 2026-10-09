import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { merge3, mergeDeep, useWorkspace } from './workspace'
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
          name, source: name, datasource: 'default', kind: 'table', primaryKey: [], uniqueKeys: [], params: [], affects: [], allowCascade: false,
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
    expect(ws.changes.map((c) => `${c.kind}:${c.name}`).sort()).toEqual(['entity:default.refunds', 'role:analyst'])
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
      { kind: 'entity', name: 'default.refunds', type: 'added' },
      { kind: 'role', name: 'analyst', type: 'modified' },
      { kind: 'user', name: 'alice', type: 'removed' },
      { kind: 'settings', name: 'settings', type: 'modified' },
    ]))
    ws.discard()
    expect(ws.dirty).toBe(false)
  })

  it('treats cleared optional text as unchanged but not a revoked token', () => {
    const ws = seeded()
    ws.entity('default.orders')!.fields![0].alias = 'order_id'
    ws.entity('default.orders')!.fields![0].alias = ''
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
    expect(ws.changes.map((c) => c.name).sort()).toEqual(['alice', 'default.refunds'])
    ws.revert({ kind: 'user', name: 'alice' })
    ws.revert({ kind: 'entity', name: 'default.refunds' })
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
    expect(ws.entity('default.refunds')!.relationships!.map((r) => r.target)).toEqual(['orders'])
    expect(ws.role('analyst')!.grants!.at(-1)).toEqual({ entity: 'refunds', actions: ['READ'] })
    expect(ws.role('support')!.grants).toEqual([{ entity: 'refunds', actions: ['READ'] }])
    expect(ws.changes.map((c) => `${c.kind}:${c.name}:${c.type}`).sort())
      .toEqual(['entity:default.refunds:added', 'role:analyst:modified', 'role:support:added'])
  })

  it('imports without grants when no actions are chosen', () => {
    const ws = seeded()
    ws.importEntities([{ name: 'refunds' }], ['analyst'], [])
    expect(ws.role('analyst')!.grants!.some((g) => g.entity === 'refunds')).toBe(false)
  })

  it('removes grants with their entity and roles from users', () => {
    const ws = seeded()
    ws.removeEntity('default.orders')
    expect(ws.role('analyst')!.grants!.map((g) => g.entity)).toEqual(['customers'])
    expect(ws.user('alice')!.grants).toEqual([])
    ws.removeRole('analyst')
    expect(ws.user('alice')!.roles).toEqual([])
  })

  it('moves references when an entity is renamed or removed', () => {
    const ws = seeded()
    ws.upsertEntity({ name: 'customers', fields: [{ name: 'id' }] })
    ws.entity('default.orders')!.relationships = [{ name: 'customer', target: 'customers', cardinality: 'belongs-to', joinOn: { customer_id: 'id' } }]
    ws.upsertEntity({ ...ws.entity('default.customers')!, name: 'clients' }, 'default.customers')
    expect(ws.role('analyst')!.grants!.map((g) => g.entity)).toEqual(['orders', 'clients'])
    expect(ws.entity('default.orders')!.relationships![0].target).toBe('clients')
    expect(ws.entity('default.clients')!.source).toBe('customers')
    ws.removeEntity('default.clients')
    expect(ws.entity('default.orders')!.relationships).toEqual([])
    expect(ws.role('analyst')!.grants!.map((g) => g.entity)).toEqual(['orders'])
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

describe('mergeDeep', () => {
  it('merges settings field by field and reports fields changed on both sides', () => {
    const base = { cost: { maxRows: 10, softScore: 5 }, cache: { enabled: false } }
    const local = { cost: { maxRows: 20, softScore: 5 }, cache: { enabled: true } }
    const next = { cost: { maxRows: 10, softScore: 8 }, cache: { enabled: false, ttl: '1m' }, audit: { enabled: true } }
    expect(mergeDeep(base, local, next)).toEqual({
      value: { cost: { maxRows: 20, softScore: 8 }, cache: { enabled: true, ttl: '1m' }, audit: { enabled: true } },
      conflicts: [],
    })
    expect(mergeDeep(base, local, { ...next, cost: { maxRows: 30, softScore: 5 } }).conflicts).toEqual(['cost.maxRows'])
  })
})

describe('saved drafts', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('reports the draft only while the workspace still holds its content', () => {
    const ws = seeded()
    ws.role('analyst')!.description = 'changed'
    expect(ws.savedDraftId).toBeNull()
    ws.markDraftSaved('7', ws.snapshot().content)
    expect(ws.savedDraftId).toBe('7')
    ws.role('analyst')!.description = 'changed again'
    expect(ws.savedDraftId).toBeNull()
    ws.role('analyst')!.description = 'changed'
    expect(ws.savedDraftId).toBe('7')
  })
})

describe('saving while editing', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('binds the draft to the submitted content, not to edits made meanwhile', () => {
    const ws = seeded()
    ws.role('analyst')!.description = 'A'
    const snap = ws.snapshot()
    ws.role('analyst')!.description = 'B'
    expect(snap.draft.roles![0].description).toBe('A')
    ws.markDraftSaved('8', snap.content)
    expect(ws.savedDraftId).toBeNull()
    ws.role('analyst')!.description = 'A'
    expect(ws.savedDraftId).toBe('8')
  })
})

describe('publishing while editing', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('keeps an undo made while the publish ran', () => {
    const ws = useWorkspace()
    const rev = (id: string) => loaded(id, {
      entities: ['orders'], roles: [{ name: 'analyst', grants: [{ entity: 'orders', actions: ['READ'] }] }], users: [],
    })
    ws.apply(rev('3'), '3')
    ws.role('analyst')!.description = 'B'
    const snap = ws.snapshot()
    ws.role('analyst')!.description = undefined
    const published = rev('4')
    ;(published.revision!.config.roles[0] as { description?: string }).description = 'B'

    const conflicts = ws.rebaseOnto(published, '4', snap.sections)
    expect(conflicts).toEqual([])
    expect(ws.baseId).toBe('4')
    expect(ws.role('analyst')!.description).toBeUndefined()
    expect(ws.changes).toEqual([{ kind: 'role', name: 'analyst', type: 'modified' }])
  })

  it('is clean after publishing without edits, and keeps edits made meanwhile', () => {
    const rev = (id: string, description?: string) => {
      const data = loaded(id, {
        entities: ['orders'], roles: [{ name: 'analyst', grants: [{ entity: 'orders', actions: ['READ'] }] }], users: [],
      })
      ;(data.revision!.config.roles[0] as { description?: string }).description = description
      return data
    }
    const ws = useWorkspace()
    ws.apply(rev('3'), '3')
    ws.role('analyst')!.description = 'B'
    let snap = ws.snapshot()
    ws.rebaseOnto(rev('4', 'B'), '4', snap.sections)
    expect(ws.dirty).toBe(false)

    ws.role('analyst')!.description = 'C'
    snap = ws.snapshot()
    ws.upsertEntity({ name: 'refunds' })
    ws.rebaseOnto(rev('5', 'C'), '5', snap.sections)
    expect(ws.role('analyst')!.description).toBe('C')
    expect(ws.changes).toEqual([{ kind: 'entity', name: 'default.refunds', type: 'added' }])
  })

  it('measures from the baseline without a snapshot', () => {
    const ws = seeded()
    ws.role('analyst')!.description = 'mine'
    expect(ws.changes).toEqual([{ kind: 'role', name: 'analyst', type: 'modified' }])
  })
})

describe('fields', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('lists where a field is named', () => {
    const ws = seeded()
    ws.upsertEntity({ name: 'orders', primaryKey: ['id'], fields: [{ name: 'id' }, { name: 'customer_id' }],
      relationships: [{ name: 'customer', target: 'customers', cardinality: 'belongs-to', joinOn: { customer_id: 'id' } }] })
    ws.upsertRole({ name: 'analyst', grants: [{ entity: 'orders', actions: ['READ'], fieldsRestricted: true,
      readFields: ['id'], writeFields: [], rows: { op: 'gt', field: 'customer_id', value: 0 } }] })
    expect(ws.fieldReferences('default.orders', 'customer_id').map((r) => `${r.kind}:${r.name}`))
      .toEqual(['role:analyst', 'relationship:default.orders'])
    expect(ws.fieldReferences('default.orders', 'id').map((r) => `${r.kind}:${r.name}`))
      .toEqual(['role:analyst', 'primaryKey:default.orders'])
    expect(ws.fieldReferences('default.customers', 'id').map((r) => `${r.kind}:${r.name}`)).toEqual(['relationship:default.orders'])
  })

  it('adds and removes fields and prunes grant field lists', () => {
    const ws = seeded()
    ws.upsertRole({ name: 'analyst', grants: [{ entity: 'orders', actions: ['READ'], fieldsRestricted: true,
      readFields: ['id', 'gone'], writeFields: ['gone'] }] })
    ws.upsertEntity({ name: 'orders', fields: [{ name: 'id' }, { name: 'gone' }] })
    ws.syncFields('default.orders', [{ name: 'region', exclude: true }], ['gone'])
    expect(ws.entity('default.orders')!.fields).toEqual([{ name: 'id' }, { name: 'region', exclude: true }])
    expect(ws.role('analyst')!.grants![0]).toMatchObject({ readFields: ['id'], writeFields: [] })
  })
})

describe('persistence', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    const items = new Map<string, string>()
    vi.stubGlobal('localStorage', {
      getItem: (k: string) => items.get(k) ?? null,
      setItem: (k: string, v: string) => void items.set(k, v),
    })
  })
  afterEach(() => vi.unstubAllGlobals())

  it('keeps datasources out of storage and ignores ones saved by older versions', () => {
    const ws = seeded()
    ws.datasources = [{ name: 'shop', driver: 'postgres', dsn: 'x', connections: [], routing: { read: 'default', write: 'default', execute: 'default' } }]
    ws.persist()
    const saved = JSON.parse(localStorage.getItem('smcp.console.workspace.v1')!)
    expect(saved.datasources).toBeUndefined()

    localStorage.setItem('smcp.console.workspace.v1', JSON.stringify({ ...saved, datasources: [{ name: 'old', driver: 'postgres', dsn: 'x' }] }))
    setActivePinia(createPinia())
    const restored = useWorkspace()
    expect(restored.restore()).toBe(true)
    expect(restored.datasources).toEqual([])
    expect(restored.entities.map((e) => e.name)).toEqual(['orders', 'customers'])
  })
})

describe('namespaced entities', () => {
  beforeEach(() => setActivePinia(createPinia()))

  function twoDatasources() {
    const ws = useWorkspace()
    ws.$patch((s) => {
      s.entities = [{ name: 'tenants', datasource: 'shop', schema: 'crm' }, { name: 'orders', datasource: 'shop', schema: 'sales' }]
      s.roles = [{ name: 'analyst', grants: [{ entity: 'tenants', actions: ['READ'] }] }]
      s.users = []
    })
    return ws
  }

  it('qualifies references an import makes ambiguous, and names the new entity unambiguously', () => {
    const ws = twoDatasources()
    ws.importEntities([{ name: 'tenants', datasource: 'warehouse', schema: 'logistics' }], ['analyst'], ['READ'])
    expect(ws.role('analyst')!.grants!.map((g) => g.entity)).toEqual(['crm.tenants', 'logistics.tenants'])
    expect(ws.accessTo('shop.crm.tenants').roles).toEqual(['analyst'])
    expect(ws.accessTo('warehouse.logistics.tenants').roles).toEqual(['analyst'])
  })

  it('keeps references on an entity moved to another schema', () => {
    const ws = twoDatasources()
    ws.entity('shop.sales.orders')!.relationships = [{ name: 'tenant', target: 'tenants', cardinality: 'belongs-to', joinOn: {} }]
    ws.upsertEntity({ ...ws.entity('shop.crm.tenants')!, schema: 'archive' }, 'shop.crm.tenants')
    expect(ws.entity('shop.archive.tenants')).toBeDefined()
    expect(ws.role('analyst')!.grants![0].entity).toBe('tenants')
    // A same-named entity added later qualifies both references.
    ws.upsertEntity({ name: 'tenants', datasource: 'shop', schema: 'crm' })
    expect(ws.role('analyst')!.grants![0].entity).toBe('archive.tenants')
    expect(ws.entity('shop.sales.orders')!.relationships![0].target).toBe('archive.tenants')
  })
})

describe('importing next to same-named entities', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('grants what was imported, even where a reference meant another entity before', () => {
    const ws = useWorkspace()
    ws.$patch((s) => {
      s.entities = [{ name: 'orders', datasource: 'main', schema: 'archive' }]
      s.roles = [{ name: 'old', grants: [{ entity: 'archive.orders', actions: ['READ'] }] }]
      s.users = []
    })
    // Its ID, archive.orders, is the reference "old" uses for main.archive.orders.
    ws.importEntities([{ name: 'orders', datasource: 'archive' }], ['new'], ['READ'])
    const named = (role: string) => ws.entityIndex.idOf(ws.role(role)!.grants![0].entity)
    expect(named('new')).toBe('archive.orders')
    expect(named('old')).toBe('main.archive.orders')
  })
})
