import { describe, expect, it } from 'vitest'
import { createPreviewer } from './planPreview'
import type { Plan } from './plan'

function deferred<T>() {
  let resolve!: (v: T) => void
  let reject!: (e: unknown) => void
  const promise = new Promise<T>((res, rej) => { resolve = res; reject = rej })
  return { promise, resolve, reject }
}

describe('previewer', () => {
  it('shows only the latest evaluation when answers come out of order', async () => {
    const answers: ReturnType<typeof deferred<string>>[] = []
    let content = 'w1'
    const p = createPreviewer<string>(() => { const d = deferred<string>(); answers.push(d); return d.promise }, () => content)
    const all: Plan = { scope: {}, import: true }
    const withoutA: Plan = { scope: {}, import: true, exclude: ['a'] }
    const first = p.evaluate(all)
    const second = p.evaluate(withoutA)
    answers[1].resolve('without a')
    await second
    expect(p.state.loading).toBe(false)
    answers[0].resolve('with a')
    await first
    expect(p.state.preview).toBe('without a')
    expect(p.current(withoutA, 'w1')).toBe(true)
    expect(p.current(all, 'w1')).toBe(false)
    content = 'w2'
    expect(p.current(withoutA, 'w2')).toBe(false)
  })

  it('keeps loading until the latest answer and drops older failures', async () => {
    const answers: ReturnType<typeof deferred<string>>[] = []
    const p = createPreviewer<string>(() => { const d = deferred<string>(); answers.push(d); return d.promise }, () => 'w')
    const plan: Plan = { scope: {}, import: true }
    const first = p.evaluate(plan)
    const second = p.evaluate(plan)
    answers[0].reject(new Error('old'))
    await first
    expect(p.state.loading).toBe(true)
    expect(p.state.error).toBe('')
    expect(p.current(plan, 'w')).toBe(false)
    answers[1].resolve('new')
    await second
    expect(p.current(plan, 'w')).toBe(true)
    p.reset()
    expect(p.state.preview).toBeNull()
  })
})
