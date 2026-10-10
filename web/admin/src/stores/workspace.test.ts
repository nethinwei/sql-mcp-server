import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { upgradeLegacySections, useWorkspace } from './workspace'
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
        users: cfg.users.map((name) => ({ name, roles: [], disabled: false, tokenHash: `sha256:${name}`, grants: [] })),
        settings: {},
      },
    },
  } as unknown as WorkspaceQuery
}

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
  })

  it('treats cleared optional text as unchanged', () => {
    const ws = seeded()
    ws.entity('default.orders')!.fields![0].alias = 'order_id'
    ws.entity('default.orders')!.fields![0].alias = ''
    ws.role('analyst')!.description = ''
    // alice has no token: revoking it changes nothing.
    ws.user('alice')!.tokenHash = ''
    expect(ws.dirty).toBe(false)
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

  it('produces a draft of the base revision holding only the changed sections', () => {
    const ws = seeded()
    expect(ws.draft).toEqual({ base: '2' })
    ws.role('analyst')!.description = 'changed'
    expect(Object.keys(ws.draft).sort()).toEqual(['base', 'roles'])
    ws.setSettings({ budget: { maxRows: 5 } })
    // A removed settings section is sent as null, a new one as it is.
    expect(ws.draft.settings).toEqual({ cost: null, budget: { maxRows: 5 } })
    ws.upsertEntity({ name: 'refunds' })
    expect(ws.draft.entities).toHaveLength(3)
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
    // A row filter must be fixed before the field goes; a field list follows.
    expect(ws.fieldReferences('default.orders', 'customer_id').map((r) => `${r.kind}:${r.name}:${r.via ?? ''}`))
      .toEqual(['role:analyst:rows', 'relationship:default.orders:'])
    expect(ws.fieldReferences('default.orders', 'id').map((r) => `${r.kind}:${r.name}:${r.via ?? ''}`))
      .toEqual(['role:analyst:fields', 'primaryKey:default.orders:'])
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

describe('upgradeLegacySections', () => {
  it('keeps the base token of a user the older console left without a hash', () => {
    const old = { entities: [], roles: [], settings: {}, users: [
      { name: 'kept', roles: ['analyst'] }, { name: 'revoked', tokenHash: '' },
      { name: 'rotated', tokenHash: 'sha256:new' }, { name: 'none' },
    ] }
    const base = { kept: 'sha256:a', revoked: 'sha256:b', rotated: 'sha256:c', none: '' }
    expect(upgradeLegacySections(old, base).users.map((u) => u.tokenHash))
      .toEqual(['sha256:a', '', 'sha256:new', undefined])
  })
})

describe('a workspace from before the upgrade', () => {
  const items = new Map<string, string>()
  beforeEach(() => {
    setActivePinia(createPinia())
    items.clear()
    vi.stubGlobal('localStorage', {
      getItem: (k: string) => items.get(k) ?? null,
      setItem: (k: string, v: string) => void items.set(k, v),
      removeItem: (k: string) => void items.delete(k),
    })
  })
  afterEach(() => vi.unstubAllGlobals())

  it('is kept for its owner to claim, never taken as anyone\'s', () => {
    const old = seeded()
    old.upsertRole({ name: 'unclaimed', grants: [] })
    const { datasources: _d, owner: _o, ...saved } = old.$state
    items.set('smcp.console.workspace.v1', JSON.stringify(saved))
    setActivePinia(createPinia())
    const ws = useWorkspace()
    ws.bindTo('root')
    expect(ws.roles.map((r) => r.name)).not.toContain('unclaimed')
    expect(ws.legacy()).toMatchObject({ changes: 1 })
    ws.dropLegacy()
    expect(ws.legacy()).toBeNull()
  })

  it('is dropped when it holds no changes', () => {
    const { datasources: _d, owner: _o, ...saved } = seeded().$state
    items.set('smcp.console.workspace.v1', JSON.stringify(saved))
    expect(useWorkspace().legacy()).toBeNull()
    expect(items.has('smcp.console.workspace.v1')).toBe(false)
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

describe('tokens', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('are part of a user, so revoking one is a change sent as no token', () => {
    const ws = useWorkspace()
    ws.apply(loaded('7', { entities: [], roles: [], users: ['alice', 'bob'] }), '7')
    expect(ws.dirty).toBe(false)
    expect(ws.hasToken(ws.user('alice')!)).toBe(true)
    ws.user('alice')!.tokenHash = ''
    delete ws.user('bob')!.tokenHash
    expect(ws.changes.map((c) => c.name)).toEqual(['alice', 'bob'])
    expect(ws.hasToken(ws.user('alice')!)).toBe(false)
    // A missing hash would keep the base user's token: the draft says none.
    expect(ws.draft.users?.map((u) => u.tokenHash)).toEqual(['', ''])
  })
})

describe('computeDraft', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('is the workspace base, a loaded draft included, never the published revision by default', () => {
    const ws = useWorkspace()
    expect(ws.computeDraft).toBeUndefined()
    ws.apply(loaded('7', { entities: ['orders'], roles: [], users: [] }), '7')
    // A loaded draft is the base: nothing unsaved, yet not the published one.
    expect(ws.dirty).toBe(false)
    expect(ws.computeDraft).toEqual({ base: '7' })
    const key = ws.contextKey
    ws.upsertEntity({ name: 'refunds' })
    expect(ws.computeDraft?.entities?.map((e) => e.name)).toEqual(['orders', 'refunds'])
    expect(ws.contextKey).not.toBe(key)
  })
})
