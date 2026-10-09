import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { useWorkspace } from './workspace'
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

describe('conflicts', () => {
  beforeEach(() => setActivePinia(createPinia()))

  function editedRole(theirs: string) {
    const ws = useWorkspace()
    ws.apply(loaded('3', { entities: ['orders'], roles: [{ name: 'analyst', grants: [] }], users: [] }), '3')
    ws.role('analyst')!.description = 'mine'
    const next = loaded('4', { entities: ['orders'], roles: [{ name: 'analyst', grants: [] }], users: [] })
    next.revision!.config.roles[0].description = theirs
    return { ws, found: ws.rebaseOnto(next, '4') }
  }

  it('records a value both sides changed, keeping mine until resolved', () => {
    const { ws, found } = editedRole('theirs')
    expect(found).toEqual([{ kind: 'role', name: 'analyst', path: ['description'], mine: 'mine', theirs: 'theirs', revision: '4' }])
    expect(ws.role('analyst')!.description).toBe('mine')
    expect(ws.conflicts).toHaveLength(1)
    ws.resolveConflict(ws.conflicts[0], 'theirs')
    expect(ws.role('analyst')!.description).toBe('theirs')
    expect(ws.conflicts).toEqual([])
  })

  it('keeps mine when chosen, and needs no choice when both made the same change', () => {
    const { ws } = editedRole('theirs')
    ws.resolveConflict(ws.conflicts[0], 'mine')
    expect(ws.role('analyst')!.description).toBe('mine')
    expect(editedRole('mine').found).toEqual([])
  })

  it('merges different fields of one entity, and conflicts on the same one', () => {
    const ws = useWorkspace()
    ws.apply(loaded('3', { entities: ['orders'], roles: [], users: [] }), '3')
    ws.entity('default.orders')!.fields = [{ name: 'id', exclude: false, mask: 'secret' }]
    const next = loaded('4', { entities: ['orders'], roles: [], users: [] })
    const theirs = next.revision!.config.entities[0] as unknown as { fields: object[] }
    theirs.fields = [{ name: 'id', exclude: true, mask: 'phone' }, { name: 'email', exclude: false }]
    const found = ws.rebaseOnto(next, '4')
    // Both masked id differently; only theirs hid it and added email.
    expect(found.map((c) => [c.path, c.mine, c.theirs])).toEqual([[['fields', { element: 'id' }, 'mask'], 'secret', 'phone']])
    expect(ws.entity('default.orders')!.fields).toEqual([
      { name: 'id', exclude: true, mask: 'secret' }, { name: 'email', exclude: false },
    ])
  })

  it('keeps unresolved conflicts across rebases, against the newest revision', () => {
    const { ws } = editedRole('theirs')
    const newer = loaded('5', { entities: ['orders'], roles: [{ name: 'analyst', grants: [] }], users: [] })
    newer.revision!.config.roles[0].description = 'newer'
    ws.rebaseOnto(newer, '5')
    expect(ws.conflicts.map((c) => [c.theirs, c.revision])).toEqual([['newer', '5']])
  })
})

describe('rebase sequences (random)', () => {
  beforeEach(() => setActivePinia(createPinia()))

  // A seeded generator, so a failure names the seed that reproduces it.
  const rng = (seed: number) => () => {
    seed = (seed * 1664525 + 1013904223) >>> 0
    return seed / 2 ** 32
  }

  it('ends every sequence of edits, publishes and choices with the chosen values', () => {
    const texts = ['a', 'b', 'c']
    for (let seed = 1; seed <= 200; seed++) {
      setActivePinia(createPinia())
      const r = rng(seed)
      const pick = <T>(xs: T[]) => xs[Math.floor(r() * xs.length)]
      const ws = useWorkspace()
      const revision = (id: number, description: string) => {
        const data = loaded(String(id), { entities: ['orders'], roles: [{ name: 'analyst', grants: [] }], users: [] })
        data.revision!.config.roles[0].description = description
        return data
      }
      let published = pick(texts)
      ws.apply(revision(1, published), '1')
      for (let id = 2; id <= 6; id++) {
        if (r() < 0.7) ws.role('analyst')!.description = pick(texts)
        const mine = ws.role('analyst')!.description
        published = r() < 0.7 ? pick(texts) : published
        ws.rebaseOnto(revision(id, published), String(id))
        for (const c of [...ws.conflicts]) {
          const choice = r() < 0.5 ? 'mine' : 'theirs'
          ws.resolveConflict(c, choice)
          expect(ws.role('analyst')!.description, `seed ${seed}`).toBe(choice === 'mine' ? mine : published)
        }
        expect(ws.conflicts, `seed ${seed}`).toEqual([])
        expect(ws.baseId).toBe(String(id))
      }
    }
  })
})

describe('conflicts across rebases', () => {
  beforeEach(() => setActivePinia(createPinia()))

  function described(id: string, description: string | null) {
    const roles = description === null ? [] : [{ name: 'analyst', grants: [] }]
    const data = loaded(id, { entities: ['orders'], roles, users: [] })
    if (description !== null) data.revision!.config.roles[0].description = description
    return data
  }

  it('lets a removal of the whole item replace the conflicts on its values', () => {
    const ws = useWorkspace()
    ws.apply(described('3', 'base'), '3')
    ws.role('analyst')!.description = 'mine'
    ws.rebaseOnto(described('4', 'theirs'), '4')
    expect(ws.openConflicts.map((c) => c.path)).toEqual([['description']])
    // The newer revision removes the role: one conflict, on the whole role.
    ws.rebaseOnto(described('5', null), '5')
    expect(ws.openConflicts.map((c) => c.path)).toEqual([[]])
    ws.resolveConflict(ws.openConflicts[0], 'theirs')
    expect(ws.roles).toEqual([])
    expect(ws.conflicts).toEqual([])
  })

  it('clears the conflicts under the one resolved', () => {
    const ws = useWorkspace()
    ws.apply(described('3', 'base'), '3')
    ws.role('analyst')!.description = 'mine'
    ws.rebaseOnto(described('4', 'theirs'), '4')
    // A whole-item conflict and one on its value, as an older workspace may hold.
    ws.conflicts.push({ kind: 'role', name: 'analyst', path: [], mine: ws.role('analyst'), theirs: undefined, revision: '4' })
    ws.resolveConflict(ws.conflicts[1], 'theirs')
    expect(ws.roles).toEqual([])
    expect(ws.conflicts).toEqual([])
  })

  it('needs no choice once the value matches the newer revision, and stops blocking', () => {
    const ws = useWorkspace()
    ws.apply(described('3', 'base'), '3')
    ws.role('analyst')!.description = 'mine'
    ws.rebaseOnto(described('4', 'theirs'), '4')
    expect(ws.openConflicts).toHaveLength(1)
    ws.role('analyst')!.description = 'theirs'
    expect(ws.openConflicts).toEqual([])
    expect(ws.dirty).toBe(false)
  })
})

describe('a whole-item conflict across rebases', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('is not dropped by a later conflict on one of its values, and taking theirs takes all of it', () => {
    const ws = useWorkspace()
    const revision = (id: string, roles: { description?: string, actions?: string[] }[]) => {
      const data = loaded(id, {
        entities: ['orders'],
        roles: roles.map((r) => ({ name: 'analyst', grants: [{ entity: 'orders', actions: r.actions ?? ['READ'] }] })),
        users: [],
      })
      roles.forEach((r, i) => { data.revision!.config.roles[i].description = r.description ?? '' })
      return data
    }
    ws.apply(revision('3', []), '3')
    // Both add the role, with different descriptions: one conflict on the whole role.
    ws.upsertRole({ name: 'analyst', description: 'mine', grants: [{ entity: 'orders', actions: ['READ'] }] })
    ws.rebaseOnto(revision('4', [{ description: 'theirs' }]), '4')
    expect(ws.openConflicts.map((c) => c.path)).toEqual([[]])
    // Both then change the grant's actions: still the one whole-role conflict.
    ws.role('analyst')!.grants![0].actions = ['READ', 'CREATE']
    ws.rebaseOnto(revision('5', [{ description: 'theirs', actions: ['READ', 'DELETE'] }]), '5')
    expect(ws.openConflicts.map((c) => c.path)).toEqual([[]])
    ws.resolveConflict(ws.openConflicts[0], 'theirs')
    expect(ws.role('analyst')!.description).toBe('theirs')
    expect(ws.role('analyst')!.grants![0].actions).toEqual(['READ', 'DELETE'])
    expect(ws.openConflicts).toEqual([])
  })
})

describe('the local side of a conflict', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('is the current value, after edits and across rebases, and is what keeping mine keeps', () => {
    const ws = useWorkspace()
    const revision = (id: string, description: string) => {
      const data = loaded(id, { entities: ['orders'], roles: [{ name: 'analyst', grants: [] }], users: [] })
      data.revision!.config.roles[0].description = description
      return data
    }
    ws.apply(revision('3', 'base'), '3')
    ws.role('analyst')!.description = 'mine'
    ws.rebaseOnto(revision('4', 'theirs'), '4')
    ws.role('analyst')!.description = 'edited'
    expect(ws.openConflicts[0].mine).toBe('edited')
    ws.role('analyst')!.description = 'edited again'
    ws.rebaseOnto(revision('5', 'newer'), '5')
    expect(ws.openConflicts.map((c) => [c.mine, c.theirs])).toEqual([['edited again', 'newer']])
    ws.resolveConflict(ws.openConflicts[0], 'mine')
    expect(ws.role('analyst')!.description).toBe('edited again')
    expect(ws.conflicts).toEqual([])
  })
})

describe('applyProposal', () => {
  beforeEach(() => setActivePinia(createPinia()))

  const entity = (name: string, schema: string, fields: { name: string; mask?: string }[] = [{ name: 'id' }]) => ({
    name, source: name, datasource: 'shop', schema, kind: null, description: null, primaryKey: ['id'], uniqueKeys: [],
    params: [], affects: [], allowCascade: false, tenantPolicy: null, legacyAccess: null,
    mcp: { dmlTools: true, customTool: false, trustedProcedure: false },
    fields: fields.map((f) => ({ name: f.name, alias: null, description: null, mask: f.mask ?? null, exclude: false })),
    relationships: [],
  })
  const grant = (e: string) => ({ entity: e, actions: ['READ' as const], fieldsRestricted: false, readFields: [], writeFields: [], rows: null })

  it('imports, changes fields and grants by ID, qualifying what becomes ambiguous', () => {
    const ws = useWorkspace()
    ws.$patch((s) => {
      s.entities = [
        { name: 'orders', datasource: 'shop', schema: 'sales', fields: [{ name: 'id' }] },
        { name: 'customers', datasource: 'shop', schema: 'sales', fields: [{ name: 'id' }, { name: 'phone' }] },
      ]
      s.roles = [{ name: 'analyst', grants: [{ entity: 'orders', actions: ['READ'] }] }]
      s.users = [{ name: 'alice', grants: [] }]
    })
    ws.applyProposal({
      added: ['shop.public.orders'],
      entities: [entity('orders', 'public'), entity('customers', 'sales', [{ name: 'id' }, { name: 'phone', mask: 'phone' }])],
      roles: [
        { name: 'analyst', description: null, grants: [grant('shop.sales.orders'), grant('shop.public.orders')] },
        { name: 'support', description: 'new', grants: [grant('shop.public.orders')] },
      ],
      users: [{ name: 'alice', grants: [grant('shop.sales.customers')] }],
    } as never)
    expect(ws.entities.map((e) => `${e.schema}.${e.name}`)).toEqual(['sales.orders', 'sales.customers', 'public.orders'])
    expect(ws.entity('shop.sales.customers')!.fields![1].mask).toBe('phone')
    expect(ws.role('analyst')!.grants!.map((g) => g.entity)).toEqual(['sales.orders', 'public.orders'])
    expect(ws.role('support')).toMatchObject({ description: 'new', grants: [{ entity: 'public.orders' }] })
    expect(ws.user('alice')!.grants!.map((g) => g.entity)).toEqual(['customers'])
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
