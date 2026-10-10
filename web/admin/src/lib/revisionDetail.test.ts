import { describe, expect, it, vi } from 'vitest'
import { nextTick, ref } from 'vue'

// Requests answer when the test releases them, in any order.
const pending: { vars: Record<string, string>; resolve: (v: unknown) => void }[] = []
vi.mock('@/api/client', () => ({
  run: (_doc: unknown, vars: Record<string, string>) => new Promise((resolve) => pending.push({ vars, resolve })),
}))

const { useRevisionDetail } = await import('./revisionDetail')

function answer(match: (vars: Record<string, string>) => boolean, value: unknown) {
  const i = pending.findIndex((p) => match(p.vars))
  if (i < 0) throw new Error(`no request ${match}`)
  pending.splice(i, 1)[0]!.resolve(value)
}
const isDiff = (from: string) => (v: Record<string, string>) => v.from === from
const settle = () => new Promise((r) => setTimeout(r))

describe('useRevisionDetail', () => {
  it('diffs again from a base that arrives late, and shows that diff whatever answers first', async () => {
    pending.length = 0
    const id = ref<string | null>('5')
    const from = ref<string | null>('4') // the published revision, before the draft list says 5 builds on 3
    const detail = useRevisionDetail(id, from)
    await nextTick()
    from.value = '3'
    await nextTick()
    // The diff from the base answers first, the one from the published revision late.
    answer(isDiff('3'), { diff: 'from 3' })
    answer(isDiff('4'), { diff: 'from 4' })
    answer((v) => v.id === '5', { revision: { yaml: 'yaml 5' } })
    await settle()
    expect(detail.diff.value).toBe('from 3')
    expect(detail.yaml.value).toBe('yaml 5')
    expect(detail.loading.value).toBe(false)
  })

  it('shows the revision selected last when an earlier one answers late', async () => {
    pending.length = 0
    const id = ref<string | null>('5')
    const detail = useRevisionDetail(id, ref('1'))
    await nextTick()
    id.value = '6'
    await nextTick()
    answer((v) => v.to === '6', { diff: 'diff 6' })
    answer((v) => v.to === '5', { diff: 'diff 5' })
    answer((v) => v.id === '6', { revision: { yaml: 'yaml 6' } })
    answer((v) => v.id === '5', { revision: { yaml: 'yaml 5' } })
    await settle()
    expect([detail.diff.value, detail.yaml.value]).toEqual(['diff 6', 'yaml 6'])
  })
})
