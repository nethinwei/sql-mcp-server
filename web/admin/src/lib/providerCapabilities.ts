import type { CapabilityStrength, ProviderCapabilitiesQuery } from '@/gql/graphql'

// What the serving connections guarantee on the database side, as the
// running service judged them from how each connection was opened.
export type ProviderCapabilities = ProviderCapabilitiesQuery['providerCapabilities']

export const emptyProviderCapabilities: ProviderCapabilities = { connections: [], issues: [] }

/** The statement timeouts datasource relies on that its connections do not enforce. */
export function issuesOf(caps: ProviderCapabilities, datasource: string) {
  return caps.issues.filter((i) => i.datasource === datasource)
}

/** The limits one connection guarantees, or null when the service did not report it. */
export function limitsOf(
  caps: ProviderCapabilities, datasource: string, connection: string,
): { read: CapabilityStrength; write: CapabilityStrength; unkeyed: CapabilityStrength } | null {
  const c = caps.connections.find((x) => x.datasource === datasource && x.connection === connection)
  if (!c) return null
  const strength = (key: string) => c.claims.find((x) => x.key === key)?.strength ?? 'UNSUPPORTED'
  return {
    read: strength('limit.timeout.read'),
    write: strength('limit.timeout.write'),
    unkeyed: strength('limit.unkeyedWrite'),
  }
}
