import { describe, expect, it } from 'vitest'
import { isEmpty, summarize, type Config } from './changeSummary'

const base: Config = {
  entities: [
    { name: 'orders', fields: [{ name: 'id' }, { name: 'amount' }, { name: 'phone', mask: 'phone' }] },
    { name: 'refunds', fields: [{ name: 'id' }] },
  ],
  roles: [{ name: 'analyst', grants: [{ entity: 'orders', actions: ['READ'] }] }],
  users: [
    { name: 'bi', roles: ['analyst'] },
    { name: 'ops', roles: [], grants: [{ entity: 'refunds', actions: ['READ'] }] },
  ],
}

const clone = (c: Config): Config => JSON.parse(JSON.stringify(c)) as Config

describe('summarize', () => {
  it('is empty for an unchanged configuration', () => {
    expect(isEmpty(summarize(base, clone(base)))).toBe(true)
  })

  it('reports access gained through a role and lost by disabling a user', () => {
    const next = clone(base)
    next.roles[0].grants!.push({ entity: 'refunds', actions: ['AGGREGATE'] })
    next.users[1].disabled = true
    const s = summarize(base, next)
    expect(s.users).toEqual([
      { user: 'bi', gained: [{ entity: 'refunds', action: 'AGGREGATE' }], lost: [], rescoped: [], subjectChanged: false },
      { user: 'ops', gained: [], lost: [{ entity: 'refunds', action: 'READ' }], rescoped: [], subjectChanged: false },
    ])
  })

  it('ignores a rule that duplicates or is covered by an unrestricted one', () => {
    const next = clone(base)
    next.users[0].grants = [{ entity: 'orders', actions: ['READ'] }]
    next.roles[0].grants!.push({ entity: 'orders', actions: ['READ'], fieldsRestricted: true, readFields: ['id'] })
    expect(isEmpty(summarize(base, next))).toBe(true)
  })

  it('reports a narrowed field or row scope as rescoped', () => {
    const next = clone(base)
    next.roles[0].grants![0] = { entity: 'orders', actions: ['READ'], fieldsRestricted: true, readFields: ['id'] }
    expect(summarize(base, next).users).toEqual([
      { user: 'bi', gained: [], lost: [], rescoped: [{ entity: 'orders', action: 'READ' }], subjectChanged: false },
    ])
  })

  it('reports a removed tenant policy as a boundary change with the users reaching the entity', () => {
    const before = clone(base)
    before.entities[0].tenantPolicy = { op: 'eq', field: 'tenant_id', value: '${subject.tenant_id}' }
    const s = summarize(before, clone(base))
    expect(isEmpty(s)).toBe(false)
    expect(s.boundaries).toEqual([{ entity: 'orders', kind: 'tenantPolicy', users: ['bi'] }])
  })

  it('reports a changed subject and legacy access', () => {
    const before = clone(base)
    before.users[0].subject = { tenant_id: 1 }
    const next = clone(before)
    next.users[0].subject = { tenant_id: 2 }
    next.entities[1].legacyAccess = { roles: { read: ['analyst'] } }
    const s = summarize(before, next)
    expect(s.users).toEqual([
      { user: 'bi', gained: [{ entity: 'refunds', action: 'READ' }], lost: [], rescoped: [], subjectChanged: true },
    ])
    expect(s.boundaries).toEqual([])
  })

  it('flags a user reaching an entity only through legacy access when disabled', () => {
    const before = clone(base)
    before.entities[1].legacyAccess = { roles: { read: ['clerk'] } }
    before.users.push({ name: 'legacy', roles: ['clerk'] })
    const next = clone(before)
    next.users[2].disabled = true
    const s = summarize(before, next)
    expect(isEmpty(s)).toBe(false)
    expect(s.users).toEqual([
      { user: 'legacy', gained: [], lost: [{ entity: 'refunds', action: 'READ' }], rescoped: [], subjectChanged: false },
    ])
  })

  it('flags a role membership change that moves legacy access', () => {
    const before = clone(base)
    before.entities[1].legacyAccess = { roles: { read: ['clerk'] } }
    before.roles.push({ name: 'clerk', grants: [] })
    const next = clone(before)
    next.users[0].roles = ['analyst', 'clerk']
    expect(summarize(before, next).users).toEqual([
      { user: 'bi', gained: [{ entity: 'refunds', action: 'READ' }], lost: [], rescoped: [], subjectChanged: false },
    ])
  })

  it('reports switching between legacy roles of the same entity', () => {
    const before = clone(base)
    before.entities[1].legacyAccess = { roles: { read: ['reader'], delete: ['eraser'] } }
    before.users.push({ name: 'clerk', roles: ['reader'] })
    const next = clone(before)
    next.users[2].roles = ['eraser']
    expect(summarize(before, next).users).toEqual([{
      user: 'clerk', gained: [{ entity: 'refunds', action: 'DELETE' }], lost: [{ entity: 'refunds', action: 'READ' }],
      rescoped: [], subjectChanged: false,
    }])
  })

  it('reports a legacy field or row scope change as rescoped', () => {
    const before = clone(base)
    before.entities[1].legacyAccess = { roles: { read: ['reader'] }, fieldACL: { reader: { read: ['id'] } } }
    before.users.push({ name: 'clerk', roles: ['reader'] })
    const next = clone(before)
    next.entities[1].legacyAccess = { roles: { read: ['reader'] }, rowPolicies: { reader: { op: 'eq', field: 'id', value: 1 } } }
    expect(summarize(before, next).users).toEqual([
      { user: 'clerk', gained: [], lost: [], rescoped: [{ entity: 'refunds', action: 'READ' }], subjectChanged: false },
    ])
  })

  it('reports entities and field visibility', () => {
    const next = clone(base)
    next.entities = next.entities.filter((e) => e.name !== 'refunds')
    next.entities.push({ name: 'invoices' })
    next.entities[0].fields = [{ name: 'id' }, { name: 'amount', exclude: true }, { name: 'phone' }, { name: 'email', mask: 'email' }]
    const s = summarize(base, next)
    expect(s.entitiesAdded).toEqual(['invoices'])
    expect(s.entitiesRemoved).toEqual(['refunds'])
    expect(s.fields).toEqual([{ entity: 'orders', shown: ['email'], hidden: ['amount'], masked: ['email'], unmasked: ['phone'] }])
  })
})
