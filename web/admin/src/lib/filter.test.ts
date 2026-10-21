import { describe, expect, it } from 'vitest'
import { describeFilter, filterFields, withOp } from './filter'
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
    expect(describeFilter({
      and: [{ op: 'eq', field: 'a', value: 1 }, { not: { or: [{ op: 'eq', field: 'b', value: 2 }, { op: 'is_null', field: 'c' }] } }],
    })).toBe('a = 1 且 非(b = 2 或 c 为空)')
  })

  it('lists referenced fields', () => {
    expect(filterFields({ and: [{ op: 'eq', field: 'a', value: 1 }, { or: [{ op: 'eq', field: 'b', value: 2 }] }] }))
      .toEqual(['a', 'b'])
    expect(filterFields({ not: { op: 'eq', field: 'c', value: 1 } })).toEqual(['c'])
  })
})

describe('withOp', () => {
  it('fits the value to the operator', () => {
    expect(withOp({ op: 'in', field: 'region', value: ['CN', 'US'] }, 'eq')).toEqual({ op: 'eq', field: 'region', value: 'CN' })
    expect(withOp({ op: 'not_in', field: 'region', value: [] }, 'like')).toEqual({ op: 'like', field: 'region', value: '' })
    expect(withOp({ op: 'eq', field: 'region', value: 'CN' }, 'in')).toEqual({ op: 'in', field: 'region', value: ['CN'] })
    expect(withOp({ op: 'in', field: 'region', value: ['CN'] }, 'not_in')).toEqual({ op: 'not_in', field: 'region', value: ['CN'] })
    expect(withOp({ op: 'in', field: 'region', value: ['CN'] }, 'is_null')).toEqual({ op: 'is_null', field: 'region' })
    expect(withOp({ op: 'eq', field: 'n', value: 3 }, 'gt')).toEqual({ op: 'gt', field: 'n', value: 3 })
  })
})
