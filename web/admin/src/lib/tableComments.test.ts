import { afterEach, describe, expect, it, vi } from 'vitest'

const run = vi.hoisted(() => vi.fn())
vi.mock('@/api/client', () => ({ run }))

const { loadTableComments, tableComments } = await import('./tableComments')

const tables = (vars: unknown) => (vars as { tables: { table: string }[] }).tables.map((t) => t.table)

describe('loadTableComments', () => {
  afterEach(() => { vi.useRealTimers(); run.mockReset() })

  it('asks for the tables not loaded yet, and for failed ones only after a while', async () => {
    vi.useFakeTimers()
    run.mockRejectedValueOnce(new Error('timeout'))
    await loadTableComments([{ name: 'orders' }, { name: 'refunds' }])
    await loadTableComments([{ name: 'orders' }, { name: 'refunds' }])
    expect(run).toHaveBeenCalledTimes(1)

    run.mockImplementation((_doc: unknown, vars: unknown) => Promise.resolve({
      tableComments: tables(vars).map((t) => ({ description: `the ${t}`, columns: [] })),
    }))
    await loadTableComments([{ name: 'orders' }, { name: 'customers' }])
    expect(tables(run.mock.calls[1][1])).toEqual(['customers'])
    expect(tableComments({ name: 'orders' })).toBeNull()

    vi.advanceTimersByTime(60_000)
    await loadTableComments([{ name: 'orders' }, { name: 'customers' }])
    expect(tables(run.mock.calls[2][1])).toEqual(['orders'])
    expect(tableComments({ name: 'orders' })?.description).toBe('the orders')
  })
})
