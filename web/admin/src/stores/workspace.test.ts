import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { canonical, upgradeLegacySections, useWorkspace } from './workspace'
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
      config: { datasources: [] },
      sections: {
        entities: cfg.entities.map((name) => ({
          name, source: name, datasource: 'default', kind: 'table', primaryKey: [], uniqueKeys: [], params: [], affects: [],
          mcp: { dmlTools: true, customTool: false, trustedProcedure: false },
          fields: [{ name: 'id', exclude: false }], relationships: [],
        })),
        roles: cfg.roles.map((r) => ({
          name: r.name,
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

  it('treats settings written in another key order as unchanged', () => {
    const ws = useWorkspace()
    const data = loaded('7', { entities: [], roles: [], users: [] })
    data.revision!.sections.settings = { cost: { maxRows: 20, maxBytes: 10 } }
    ws.apply(data, '7')
    ws.setSettings({ cost: { maxBytes: 10, maxRows: 20 } })
    expect(ws.changes).toEqual([])
    expect(ws.dirty).toBe(false)
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

  // What a removal or rename means elsewhere (grants, relationships, members)
  // is the server's to work out (x/admin/configapp reconcile): an edit here
  // changes its own item only.
  it('changes only the item edited, leaving references to the server', () => {
    const ws = seeded()
    ws.removeEntity('default.orders')
    ws.removeRole('analyst')
    expect(ws.entity('default.orders')).toBeUndefined()
    expect(ws.user('alice')!.roles).toEqual(['analyst'])
    expect(ws.user('alice')!.grants!.map((g) => g.entity)).toEqual(['orders'])
  })

  it('removes entities in bulk, each an item of its own', () => {
    const ws = seeded()
    const ids = ws.entities.map((e) => `${e.datasource ?? 'default'}.${e.name}`)
    ws.removeEntities(ids)
    expect(ws.entities).toEqual([])
    expect(ws.changes.filter((c) => c.kind === 'entity' && c.type === 'removed').length).toBe(ids.length)
  })

  it('manages role membership on the users', () => {
    const ws = seeded()
    ws.setRoleMembers('analyst', [])
    expect(ws.user('alice')!.roles).toEqual([])
    ws.setRoleMembers('analyst', ['alice'])
    expect(ws.user('alice')!.roles).toEqual(['analyst'])
  })
})

describe('fields', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('adds and removes fields', () => {
    const ws = seeded()
    ws.upsertEntity({ name: 'orders', fields: [{ name: 'id' }, { name: 'gone' }] })
    ws.syncFields('default.orders', [{ name: 'region', exclude: true }], ['gone'])
    expect(ws.entity('default.orders')!.fields).toEqual([{ name: 'id' }, { name: 'region', exclude: true }])
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

  // The existing reference the import makes ambiguous is the server's to
  // qualify; the grant the import writes names the new entity unambiguously.
  it('names an imported entity unambiguously', () => {
    const ws = twoDatasources()
    ws.importEntities([{ name: 'tenants', datasource: 'warehouse', schema: 'logistics' }], ['analyst'], ['READ'])
    expect(ws.role('analyst')!.grants!.map((g) => g.entity)).toEqual(['tenants', 'logistics.tenants'])
    expect(ws.accessTo('warehouse.logistics.tenants').roles).toEqual(['analyst'])
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
    // Its ID, archive.orders, is the reference "old" uses for
    // main.archive.orders, which the server keeps naming it.
    ws.importEntities([{ name: 'orders', datasource: 'archive' }], ['new'], ['READ'])
    expect(ws.entityIndex.idOf(ws.role('new')!.grants![0].entity)).toBe('archive.orders')
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
  })
})

describe('contextKey', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('changes with the base and with every edit', () => {
    const ws = useWorkspace()
    ws.apply(loaded('7', { entities: ['orders'], roles: [], users: [] }), '7')
    const key = ws.contextKey
    ws.upsertEntity({ name: 'refunds' })
    expect(ws.contextKey).not.toBe(key)
    ws.apply(loaded('8', { entities: ['orders'], roles: [], users: [] }), '8')
    expect(ws.contextKey).not.toBe(key)
  })
})

describe('canonical', () => {
  // The server's merge rules (x/admin/configapp/testdata/merge_schema.json).
  it('reads a zero where a rule says so as unset, and nowhere else', () => {
    expect(canonical({ name: 'id', fields: [{ name: 'a', exclude: false, mask: '' }] }))
      .toBe(canonical({ name: 'id', fields: [{ name: 'a' }] }))
    expect(canonical({ name: 'o', mcp: { dmlTools: false } })).not.toBe(canonical({ name: 'o', mcp: {} }))
    expect(canonical({ cost: { description: '' } })).not.toBe(canonical({ cost: {} }))
  })
})

// A role renamed here and not written yet stays a rename when the
// workspace moves onto another base revision meanwhile: it is written with
// renamedFrom, so what named the role follows it.
describe('a rename across a change of base', () => {
  beforeEach(() => setActivePinia(createPinia()))

  const items = (base: string) => ({
    base, version: 1, updatedBy: 'root', taken: [], checkpoint: null, conflicts: [], notes: [],
    items: { complete: true, items: [
      { kind: 'role', key: 'analyst', value: { name: 'analyst', grants: [] } },
      { kind: 'settings', key: 'settings', value: {} },
    ] },
  })

  it('is still written as a rename', () => {
    const ws = useWorkspace()
    ws.apply(loaded('1', { entities: [], roles: [{ name: 'analyst', grants: [] }], users: [] }), '1')
    ws.take(items('1'))
    ws.role('analyst')!.name = 'lead'
    ws.rename('role', 'analyst', 'lead')
    ws.take(items('2'), loaded('2', { entities: [], roles: [{ name: 'analyst', grants: [] }], users: [] }))
    expect(ws.unsavedItems()).toEqual(expect.arrayContaining([
      expect.objectContaining({ kind: 'role', key: 'lead', renamedFrom: 'analyst' }),
    ]))
  })

  // Reverting the item renamed brings it back under its key: the renamed
  // copy is then written as an addition, not as a rename of it.
  it('is dropped when the item renamed is reverted', () => {
    const ws = useWorkspace()
    ws.apply(loaded('1', { entities: [], roles: [{ name: 'analyst', grants: [] }], users: [] }), '1')
    ws.take(items('1'))
    ws.role('analyst')!.name = 'lead'
    ws.rename('role', 'analyst', 'lead')
    expect(ws.unsavedItems()).toEqual(expect.arrayContaining([expect.objectContaining({ renamedFrom: 'analyst' })]))
    ws.revert({ kind: 'role', name: 'analyst' })
    expect(ws.roles.map((r) => r.name).sort()).toEqual(['analyst', 'lead'])
    const unsaved = ws.unsavedItems()
    expect(unsaved.map((it) => it.key)).toEqual(['lead'])
    expect(unsaved[0].renamedFrom).toBeUndefined()
  })
})
