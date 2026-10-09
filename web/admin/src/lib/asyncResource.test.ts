import { describe, expect, it } from 'vitest'
import { createResource } from './asyncResource'
import { fieldSync } from './fieldSync'

describe('resource', () => {
  it('tells not found from read and failed, and keeps only the latest load', async () => {
    const answers: Record<string, (v: unknown) => void> = {}
    const fail: Record<string, (e: unknown) => void> = {}
    const r = createResource((key: string) => new Promise<string | null>((resolve, reject) => {
      answers[key] = resolve as (v: unknown) => void
      fail[key] = reject
    }))
    const a = r.load('a')
    const b = r.load('b')
    answers.b(null)
    await b
    expect(r.state.status).toBe('notFound')
    answers.a('late')
    await a
    expect(r.state).toMatchObject({ status: 'notFound', data: null })
    const c = r.load('c')
    expect(r.state.status).toBe('loading')
    fail.c(new Error('down'))
    await c
    expect(r.state).toMatchObject({ status: 'error', error: 'down' })
    const d = r.load('d')
    answers.d('table')
    await d
    expect(r.state).toMatchObject({ status: 'ok', data: 'table' })
  })
})

describe('field sync', () => {
  const entity = { name: 'orders', fields: [{ name: 'id' }, { name: 'note' }, { name: 'gone' }] }
  it('proposes nothing without a table that was read', () => {
    expect(fieldSync(entity, null)).toBeNull()
    expect(fieldSync(undefined, { columns: [] } as never)).toBeNull()
  })
  it('compares the fields with the columns read', () => {
    const table = { columns: [{ name: 'id' }, { name: 'note' }, { name: 'phone' }] } as never
    const sync = fieldSync(entity, table)!
    expect(sync.added.map((c) => c.name)).toEqual(['phone'])
    expect(sync.removed).toEqual(['gone'])
  })
})
