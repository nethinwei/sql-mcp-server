import { describe, expect, it } from 'vitest'
import { at, contextAt, localize, restartsBelow, validate, type Schema } from './jsonSchema'
import schemaJSON from '../../../../core/config/schema.json'

const root: Schema = {
  type: 'object',
  properties: {
    cost: {
      type: 'object',
      additionalProperties: false,
      properties: {
        maxRows: { type: 'integer', minimum: 1 },
        aqe: { type: 'object', properties: { sampleRate: { type: 'number', minimum: 0, maximum: 1 } } },
      },
    },
    budget: {
      type: 'object',
      properties: { roles: { type: 'object', additionalProperties: { $ref: '#/$defs/limits' } } },
    },
    server: { type: 'object', properties: { transport: { type: 'string', enum: ['stdio', 'http'] } } },
  },
  $defs: { limits: { type: 'object', properties: { maxConcurrent: { type: 'integer', minimum: 0 } } } },
}

describe('schema lookup', () => {
  it('follows properties, maps and refs', () => {
    expect(at(root, root, ['budget', 'roles', 'analyst', 'maxConcurrent'])?.type).toBe('integer')
    expect(at(root, root, ['cost', 'nope'])).toBeUndefined()
  })

  it('keeps keywords written next to a $ref', () => {
    const s: Schema = { $defs: { d: { type: 'string', description: 'def' } } }
    expect(at(s, { type: 'object', properties: { p: { $ref: '#/$defs/d', description: 'prop' } } }, ['p']))
      .toMatchObject({ type: 'string', description: 'prop' })
  })
})

describe('generated configuration schema', () => {
  const generated = schemaJSON as unknown as Schema

  it('localizes help text and keeps defaults and restart markers', () => {
    const zh = localize(generated, 'zh-CN')
    const maxRows = at(zh, zh, ['cost', 'maxRows'])
    expect(maxRows?.description).toMatch(/LIMIT/)
    expect(maxRows?.default).toBe(10000)
    expect(at(zh, zh, ['cost'])?.title).toBe('成本闸门与输入上限')
    expect(at(generated, generated, ['cost'])?.title).toBe('Cost gate and input limits')
    expect(at(zh, zh, ['budget', 'roles', 'analyst', 'maxConcurrent'])?.description).toBeTruthy()
  })

  it('marks sections that need a restart', () => {
    expect(restartsBelow(generated, at(generated, generated, ['tools']))).toBe(true)
    expect(restartsBelow(generated, at(generated, generated, ['transactions']))).toBe(true)
    expect(restartsBelow(generated, at(generated, generated, ['cache']))).toBe(false)
  })
})

describe('validate', () => {
  it('reports types, ranges, enums and unknown keys', () => {
    const problems = validate(root, root, {
      cost: { maxRows: 0, aqe: { sampleRate: 'x' }, maxRow: 5 },
      server: { transport: 'tcp' },
      budget: { roles: { a: { maxConcurrent: 1.5 } } },
    })
    expect(problems.map((p) => `${p.path.join('.')}:${p.code}`).sort()).toEqual([
      'budget.roles.a.maxConcurrent:type',
      'cost.aqe.sampleRate:type',
      'cost.maxRow:unknown',
      'cost.maxRows:minimum',
      'server.transport:enum',
    ])
    expect(problems.find((p) => p.code === 'unknown')?.severity).toBe('error')
  })
})

describe('contextAt', () => {
  const ctx = (src: string) => contextAt(src.replace('|', ''), src.indexOf('|'))

  it('finds key positions, including inside an unfinished string', () => {
    expect(ctx('{"aqe": {"sa|')).toMatchObject({ container: ['aqe'], expect: 'key', prefix: 'sa', from: 9 })
    expect(ctx('{"maxRows": 1, |')).toMatchObject({ container: [], expect: 'key', prefix: '' })
  })

  it('finds value positions in objects and arrays', () => {
    expect(ctx('{"server": {"transport": "ht|')).toMatchObject({ container: ['server'], expect: 'value', key: 'transport', prefix: 'ht' })
    expect(ctx('{"a": [1, tr|')).toMatchObject({ container: ['a'], expect: 'value', key: 1, prefix: 'tr' })
  })

  it('skips escaped quotes and closed containers', () => {
    expect(ctx('{"x": "a\\"b", "y": {}, |')).toMatchObject({ container: [], expect: 'key' })
  })
})
