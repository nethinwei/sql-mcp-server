import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createI18n } from 'vue-i18n'
import { defineComponent, h, nextTick, ref } from 'vue'
import zh from '@/i18n/zh-CN'
import { ScanBatchesQuery, SchemaListQuery, StartScanBatchMutation } from '@/api/ops'
import type { EntityInput } from '@/gql/schema'
import type { Plan } from '@/lib/plan'
import { useStatus } from '@/stores/status'

// The server is a map from document to answer.
const calls: unknown[] = []
let answer = new Map<unknown, (vars: Record<string, unknown>) => unknown>()
vi.mock('@/api/client', () => ({
  run: (doc: unknown, vars: Record<string, unknown>) => {
    calls.push(doc)
    return Promise.resolve(answer.get(doc)?.(vars) ?? {})
  },
}))

const { default: SyncEntitiesDialog } = await import('./SyncEntitiesDialog.vue')

const hour = 60 * 60 * 1000
const entity = (name: string, extra: Partial<EntityInput> = {}) =>
  ({ name, datasource: 'shop', fields: [], ...extra }) as unknown as EntityInput

const batch = (state: string) => ({
  id: 'b1', datasource: 'shop', state, total: 1, done: state === 'done' ? 1 : 0, failed: 0, canceled: 0,
  pending: state === 'done' ? 0 : 1, startedBy: 'root', startedAt: '', finishedAt: null,
  items: [{ schema: 'sales', state: state === 'done' ? 'done' : 'running', error: null }],
})

function render(entities: EntityInput[]) {
  const pinia = createPinia()
  setActivePinia(pinia)
  useStatus().server = { unreachableDatasources: [{ datasource: 'down', error: 'refused' }] } as never
  const show = ref(false)
  const plans: Plan[] = []
  const Host = defineComponent(() => () => h(SyncEntitiesDialog, {
    show: show.value, entities, 'onUpdate:show': (v: boolean) => { show.value = v }, onPlan: (p: Plan) => plans.push(p),
  }))
  const i18n = createI18n({ legacy: false, locale: 'zh-CN', messages: { 'zh-CN': zh } })
  mount(Host, { global: { plugins: [pinia, i18n] }, attachTo: document.body })
  return { show, plans }
}

const button = (text: string) =>
  [...document.querySelectorAll('button')].find((b) => b.textContent?.includes(text)) as HTMLButtonElement

beforeEach(() => {
  calls.length = 0
  document.body.innerHTML = ''
  answer = new Map()
  answer.set(SchemaListQuery, () => ({ schemaList: { source: 'a', defaultSchema: 'public', listedAt: '', schemas: [
    { name: 'public', scannedAt: new Date(Date.now() - hour / 2).toISOString(), tables: 1 },
    { name: 'sales', scannedAt: new Date(Date.now() - 3 * hour).toISOString(), tables: 1 },
  ] } }))
})

describe('SyncEntitiesDialog', () => {
  const entities = [entity('users'), entity('orders', { schema: 'sales' }), entity('logs', { datasource: 'down' }),
    entity('refresh', { kind: 'procedure' })]

  it('lists the schemas, checks the stale ones and leaves out what it cannot sync', async () => {
    const { show } = render(entities)
    show.value = true
    await flushPromises()
    const text = document.body.textContent ?? ''
    expect(text).toContain('同步 2 个实体')
    expect(text).toContain('down（1）')
    expect(text).toContain('1 个存储过程不参与同步')
    const boxes = [...document.querySelectorAll('.n-checkbox')]
    expect(boxes.map((b) => b.classList.contains('n-checkbox--checked'))).toEqual([false, true])
  })

  it('previews right away when the scan is skipped', async () => {
    const { show, plans } = render(entities)
    show.value = true
    await flushPromises()
    button('跳过扫描').click()
    await nextTick()
    expect(plans).toEqual([{ scope: { entities: ['shop.users', 'shop.sales.orders'] },
      sync: { add: true, remove: true, hideNew: true, removeTables: true, keys: true } }])
    expect(calls).not.toContain(StartScanBatchMutation)
    expect(show.value).toBe(false)
  })

  it('scans the stale schemas, then previews', async () => {
    vi.useFakeTimers()
    const states = ['running', 'running', 'done']
    answer.set(StartScanBatchMutation, () => ({ startScanBatch: batch('running') }))
    answer.set(ScanBatchesQuery, () => ({ scanBatches: [batch(states.shift() ?? 'done')] }))
    const { show, plans } = render(entities)
    show.value = true
    await vi.runAllTimersAsync()
    button('扫描 1 个库后预览').click()
    await vi.runAllTimersAsync()
    expect(calls).toContain(StartScanBatchMutation)
    expect(plans).toHaveLength(1)
    vi.useRealTimers()
  })
})
