import { describe, expect, it } from 'vitest'
import { issueText, issuesOf, limitsOf, type ProviderCapabilities } from './providerCapabilities'
import { locale } from '@/i18n'

const caps: ProviderCapabilities = {
  connections: [{
    datasource: 'shop', connection: 'ro',
    claims: [
      { key: 'limit.timeout.read', strength: 'BEST_EFFORT', evidence: 'DECLARED', reason: 'the DSN sets it',
        reasonDetail: null },
      { key: 'limit.unkeyedWrite', strength: 'ENFORCED', evidence: 'DB_VERIFIED', reason: null, reasonDetail: null },
    ],
  }],
  issues: [
    { datasource: 'shop', connection: 'ro', key: 'limit.timeout.read', use: 'reads', strength: 'BEST_EFFORT', reason: 'x',
      useDetail: { code: 'limitUse.reads', params: {}, parts: [], text: 'reads' }, reasonDetail: null },
    { datasource: 'ledger', connection: 'rw', key: 'limit.timeout.write', use: 'writes', strength: 'UNSUPPORTED',
      reason: 'max_execution_time stops SELECT only, and behind a transaction pooler',
      useDetail: { code: 'limitUse.writes', params: {}, parts: [], text: 'writes' },
      reasonDetail: { code: 'timeout.mysqlWritePooled', params: {}, parts: [],
        text: 'max_execution_time stops SELECT only, and behind a transaction pooler' } },
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

  it('reads an issue in the console language', () => {
    locale.value = 'zh-CN'
    expect(issueText(caps.issues[1])).toBe('rw：写超时（写入）为 无：' +
      'max_execution_time 只限制 SELECT；经事务型连接池时无法停止超时的语句（KILL QUERY 可能误停其他客户端的会话）：' +
      '写语句须由数据库侧限制（DBA）')
    expect(issueText({ ...caps.issues[0], reasonDetail: null })).toBe('ro：读超时（读取）为 尽力：x')
  })
})
