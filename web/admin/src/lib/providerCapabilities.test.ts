import { describe, expect, it } from 'vitest'
import { issuesOf, limitsOf, type ProviderCapabilities } from './providerCapabilities'

const caps: ProviderCapabilities = {
  connections: [{
    datasource: 'shop', connection: 'ro',
    claims: [
      { key: 'limit.timeout.read', strength: 'BEST_EFFORT', evidence: 'DECLARED', reason: 'the DSN sets it' },
      { key: 'limit.unkeyedWrite', strength: 'ENFORCED', evidence: 'DB_VERIFIED', reason: null },
    ],
  }],
  issues: [
    { datasource: 'shop', connection: 'ro', key: 'limit.timeout.read', use: 'reads', strength: 'BEST_EFFORT', reason: 'x' },
    { datasource: 'ledger', connection: 'rw', key: 'limit.timeout.write', use: 'writes', strength: 'UNSUPPORTED', reason: 'y' },
  ],
}

describe('provider capabilities', () => {
  it('keeps the issues of one datasource', () => {
    expect(issuesOf(caps, 'shop').map((i) => i.connection)).toEqual(['ro'])
    expect(issuesOf(caps, 'none')).toEqual([])
  })
  it('reads a missing claim as unsupported, and an unreported connection as unknown', () => {
    expect(limitsOf(caps, 'shop', 'ro')).toEqual({ read: 'BEST_EFFORT', write: 'UNSUPPORTED', unkeyed: 'ENFORCED' })
    expect(limitsOf(caps, 'shop', 'rw')).toBeNull()
  })
})
