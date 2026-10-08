import { describe, expect, it } from 'vitest'
import { capabilityOf, denied, indexCapabilities } from './capabilities'

describe('capabilities', () => {
  const index = indexCapabilities([
    { entity: 'orders', action: 'READ', privilege: 'GRANTED', connection: 'ro', columns: null, reason: null },
    { entity: 'orders', action: 'UPDATE', privilege: 'DENIED', connection: 'ro', columns: null, reason: 'x' },
  ])

  it('looks up by entity and action', () => {
    expect(denied(capabilityOf(index, 'orders', 'UPDATE'))).toBe(true)
    expect(denied(capabilityOf(index, 'orders', 'READ'))).toBe(false)
  })

  it('treats unassessed entities and actions as not denied', () => {
    expect(capabilityOf(index, 'refunds', 'READ')).toBeUndefined()
    expect(denied(capabilityOf(index, 'orders', 'DELETE'))).toBe(false)
  })
})
