import type { CapabilityStrength, ProviderCapabilitiesQuery } from '@/gql/graphql'
import { i18n, t } from '@/i18n'
import { reasonText } from './reasons'

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

type Issue = ProviderCapabilities['issues'][number]

/** A capability key in the console language; the key itself when it has no label. */
export function capabilityKeyText(key: string): string {
  const label = `datasources.capKey.${key.replaceAll('.', '_')}`
  return i18n.global.te(label) ? t(label) : key
}

/** One issue as the console reads it: the limit, the work, how firmly, and why. */
export function issueText(i: Issue): string {
  return t('datasources.limitIssue', {
    connection: i.connection, key: capabilityKeyText(i.key), use: reasonText(i.useDetail, i.use),
    strength: t(`datasources.strength.${i.strength}`), reason: reasonText(i.reasonDetail, i.reason ?? ''),
  })
}
