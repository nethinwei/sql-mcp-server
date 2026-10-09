import { describe, expect, it } from 'vitest'
import { byNamespace, entityNamespace, sourceIfRenamed } from './names'

describe('entity namespaces', () => {
  it('names the datasource and schema, or the datasource alone', () => {
    expect(entityNamespace({ name: 'orders', schema: 'sales', datasource: 'shop' })).toBe('shop · sales')
    expect(entityNamespace({ name: 'orders' })).toBe('default')
  })
  it('shows the table only when the entity is named differently', () => {
    expect(sourceIfRenamed({ name: 'open_orders', source: 'v_open_orders' })).toBe('v_open_orders')
    expect(sourceIfRenamed({ name: 'orders', source: 'orders' })).toBeNull()
    expect(sourceIfRenamed({ name: 'orders' })).toBeNull()
  })
  it('groups by namespace, without a heading when there is one namespace', () => {
    const a = { name: 'a', datasource: 'shop', schema: 'crm' }
    const b = { name: 'b', datasource: 'shop', schema: 'sales' }
    const c = { name: 'c', datasource: 'shop', schema: 'crm' }
    expect(byNamespace([a, b, c])).toEqual([
      { namespace: 'shop · crm', items: [a, c] }, { namespace: 'shop · sales', items: [b] },
    ])
    expect(byNamespace([a, c])).toEqual([{ namespace: '', items: [a, c] }])
  })
})
