import { describe, expect, it } from 'vitest'
import { describeFilter, filterFields } from './filter'
import { locale } from '@/i18n'

locale.value = 'zh-CN'

describe('describeFilter', () => {
  it('renders conditions, groups and subject placeholders', () => {
    expect(describeFilter(undefined)).toBe('全部行')
    expect(describeFilter({ op: 'eq', field: 'region', value: 'CN' })).toBe('region = "CN"')
    expect(describeFilter({ op: 'is_null', field: 'deleted_at' })).toBe('deleted_at 为空')
    expect(describeFilter({ op: 'eq', field: 'tenant_id', value: '${subject.tenant_id}' }))
      .toBe('tenant_id = 「当前用户.tenant_id」')
    expect(describeFilter({
      and: [{ op: 'gt', field: 'amount', value: 100 }, { or: [{ op: 'eq', field: 'a', value: 1 }, { op: 'eq', field: 'b', value: 2 }] }],
    })).toBe('amount > 100 且 (a = 1 或 b = 2)')
    expect(describeFilter({ op: 'in', field: 'region', value: ['CN', 'SG'] })).toBe('region ∈ ["CN", "SG"]')
  })

  it('lists referenced fields', () => {
    expect(filterFields({ and: [{ op: 'eq', field: 'a', value: 1 }, { or: [{ op: 'eq', field: 'b', value: 2 }] }] }))
      .toEqual(['a', 'b'])
  })
})
