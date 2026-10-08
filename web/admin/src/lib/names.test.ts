import { describe, expect, it } from 'vitest'
import { physicalLocation } from './names'

describe('physicalLocation', () => {
  it('names the datasource, schema and table', () => {
    expect(physicalLocation({ name: 'orders_b', source: 'orders', schema: 'sales', datasource: 'crm' }))
      .toBe('crm · sales.orders')
  })
  it('defaults the datasource and the table, and omits an unset schema', () => {
    expect(physicalLocation({ name: 'orders' })).toBe('default · orders')
  })
})
