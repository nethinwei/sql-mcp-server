import { describe, expect, it } from 'vitest'
import { escapeDatasources, executableDatasources, sqlExecute, userPermissions } from './systemPermissions'
import type { Datasource } from '@/stores/workspace'

const ds = (name: string, escape: boolean): Datasource => ({
  name, driver: 'postgres', dsn: 'x', connections: [], routing: { read: 'default', write: 'default', execute: 'default' },
  sqlEscape: escape ? { connection: 'default' } : null,
})

describe('system permissions', () => {
  it('offers execute_sql only on datasources admitting it', () => {
    expect(escapeDatasources([ds('crm', true), ds('shop', false)])).toEqual(['crm'])
    expect(sqlExecute('crm')).toBe('sql:execute@crm')
  })
  it('unites a user\'s permissions with its roles\' and keeps the admitting datasources', () => {
    const roles = [{ name: 'dba', permissions: ['sql:execute@crm'] }, { name: 'reader' }]
    const user = { name: 'alice', roles: ['dba', 'reader', 'ghost'], permissions: ['sql:execute@shop', 'sql:execute@crm'] }
    expect(userPermissions(user, roles)).toEqual(['sql:execute@shop', 'sql:execute@crm'])
    expect(executableDatasources(userPermissions(user, roles), [ds('crm', true), ds('shop', false)])).toEqual(['crm'])
  })
})
