import type { Datasource } from '@/stores/workspace'
import type { RoleInput, UserInput } from '@/gql/schema'

// System permissions (sql:execute@<datasource>): the console offers one per
// datasource admitting the escape hatch, and shows who may use it where.

export const SQL_EXECUTE_PREFIX = 'sql:execute@'

export const sqlExecute = (datasource: string) => `${SQL_EXECUTE_PREFIX}${datasource}`

/** The datasources execute_sql can run on at all (their sqlEscape is declared). */
export function escapeDatasources(datasources: Datasource[]): string[] {
  return datasources.filter((d) => d.sqlEscape).map((d) => d.name)
}

/** A user's system permissions: its own and its roles'. */
export function userPermissions(user: UserInput, roles: RoleInput[]): string[] {
  const own = user.permissions ?? []
  const fromRoles = (user.roles ?? []).flatMap((name) => roles.find((r) => r.name === name)?.permissions ?? [])
  return [...new Set([...own, ...fromRoles])]
}

/** The datasources execute_sql is usable on with the permissions held: admitting ones named by a permission. */
export function executableDatasources(permissions: string[], datasources: Datasource[]): string[] {
  const admitted = new Set(escapeDatasources(datasources))
  return permissions
    .filter((p) => p.startsWith(SQL_EXECUTE_PREFIX))
    .map((p) => p.slice(SQL_EXECUTE_PREFIX.length))
    .filter((ds) => admitted.has(ds))
}
