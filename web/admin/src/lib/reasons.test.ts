import { describe, expect, it } from 'vitest'
import codes from '../../../../core/reason/testdata/codes.json'
import { reasonsEn, reasonsZh } from '@/i18n/reasons'
import { locale } from '@/i18n'
import { reasonText } from './reasons'

const placeholders = (s: string) => [...s.matchAll(/\{(\w+)\}/g)].map((m) => m[1]).sort()

function message(messages: Record<string, unknown>, code: string): string | undefined {
  const v = code.split('.').reduce<unknown>((o, k) => (o as Record<string, unknown> | undefined)?.[k], messages)
  return typeof v === 'string' ? v : undefined
}

describe('server reasons', () => {
  // Every code the server gives has a message in both languages, with the
  // template's placeholders.
  for (const [code, template] of Object.entries(codes as Record<string, string>)) {
    it(code, () => {
      for (const messages of [reasonsZh, reasonsEn]) {
        const m = message(messages, code)
        expect(m, code).toBeTypeOf('string')
        expect(placeholders(m!), code).toEqual(placeholders(template))
      }
    })
  }

  it('lists no message the server does not give', () => {
    const walk = (o: Record<string, unknown>, prefix = ''): string[] => Object.entries(o).flatMap(([k, v]) =>
      typeof v === 'string' ? [prefix + k] : walk(v as Record<string, unknown>, `${prefix}${k}.`))
    expect(walk(reasonsZh).sort()).toEqual(Object.keys(codes).sort())
  })

  it('translates parts, and falls back to the text for an unknown code', () => {
    locale.value = 'zh-CN'
    const notice = {
      code: 'validation.capabilityDenied',
      params: { entity: 'orders', action: 'update', connection: 'ro' },
      parts: [{ name: 'cause', code: 'privilege.missing', params: { privilege: 'UPDATE' }, text: 'x' }],
      text: 'English',
    }
    expect(reasonText(notice)).toBe('实体 orders：授予了 update，但连接 ro 无法执行（账号缺少 UPDATE 权限）')
    expect(reasonText({ code: 'new.unknown', text: 'as the server says' })).toBe('as the server says')
    expect(reasonText(null, 'fallback')).toBe('fallback')
    locale.value = 'en'
    expect(reasonText(notice)).toBe('entity orders: update is granted, but connection ro cannot run it ' +
      '(the account lacks the UPDATE privilege)')
    locale.value = 'zh-CN'
  })
})
