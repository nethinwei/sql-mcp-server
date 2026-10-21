import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { createPinia } from 'pinia'
import { createI18n } from 'vue-i18n'
import { defineComponent, h, nextTick, ref } from 'vue'
import zh from '@/i18n/zh-CN'
import GrantMatrix from './GrantMatrix.vue'
import type { GrantRow } from '@/lib/grants'
import type { GrantInput } from '@/gql/schema'

vi.mock('@/api/client', () => ({ run: () => Promise.reject(new Error('offline')) }))

interface Matrix {
  pageRows: GrantRow[]
  toggle: (row: GrantRow, action: string, on: boolean) => void
  openRows: (row: GrantRow) => void
  saveEditor: () => void
  draftRows: unknown
  editorGone: boolean
}

function render(count: number, granted: number) {
  const entities = Array.from({ length: count }, (_, i) => ({ name: `t${i}`, datasource: 'shop', schema: `s${i % 3}` }))
  const grants = ref<GrantInput[]>(Array.from({ length: granted }, (_, g) => ({ entity: `s${g % 3}.t${g}`,
    actions: ['READ'] })) as GrantInput[])
  const Host = defineComponent(() => () => h(GrantMatrix, { modelValue: grants.value, entities,
    'onUpdate:modelValue': (v: GrantInput[]) => { grants.value = v } }))
  const i18n = createI18n({ legacy: false, locale: 'zh-CN', messages: { 'zh-CN': zh } })
  const wrapper = mount(Host, { global: { plugins: [createPinia(), i18n], stubs: { RouterLink: true } } })
  const matrix = (wrapper.findComponent(GrantMatrix).vm as unknown as { $: { setupState: Matrix } }).$.setupState
  return { grants, matrix }
}

describe('GrantMatrix', () => {
  // Toggling one cell replaces only its grant and its row: the other rows
  // stay the same objects, so they are not rendered again.
  it('changes only the row toggled', async () => {
    const { grants, matrix } = render(30, 10)
    const before = [...grants.value]
    const rows = [...matrix.pageRows]
    matrix.toggle(rows[2], 'AGGREGATE', true)
    await nextTick()
    const changed = rows[2].index
    expect(grants.value[changed].actions).toEqual(['READ', 'AGGREGATE'])
    expect(before[changed].actions).toEqual(['READ'])
    grants.value.forEach((g, i) => { if (i !== changed) expect(g).toBe(before[i]) })
    matrix.pageRows.forEach((r, i) => (i === 2 ? expect(r).not.toBe(rows[i]) : expect(r).toBe(rows[i])))
  })

  it('drops a grant left without actions', async () => {
    const { grants, matrix } = render(5, 2)
    matrix.toggle(matrix.pageRows[0], 'READ', false)
    await nextTick()
    expect(grants.value).toHaveLength(1)
  })

  // The row scope editor writes the grant it opened on, even when another
  // write reordered the grants meanwhile, and nothing once it is gone.
  it('saves the scope onto the grant edited, wherever it moved', async () => {
    const { grants, matrix } = render(5, 3)
    const row = matrix.pageRows.find((r) => r.grant?.entity === 's1.t1')!
    matrix.openRows(row)
    matrix.draftRows = { field: 'id', op: 'eq', value: 1 }
    const [g0, g1, g2] = grants.value
    grants.value = JSON.parse(JSON.stringify([g2, g0, g1])) as GrantInput[]
    await nextTick()
    matrix.saveEditor()
    await nextTick()
    expect(grants.value.map((g) => [g.entity, g.rows])).toEqual([
      ['s2.t2', undefined], ['s0.t0', undefined], ['s1.t1', { field: 'id', op: 'eq', value: 1 }]])
    expect(matrix.editorGone).toBe(false)
  })

  it('writes nothing when the grant edited was removed', async () => {
    const { grants, matrix } = render(5, 3)
    const row = matrix.pageRows.find((r) => r.grant?.entity === 's2.t2')!
    matrix.openRows(row)
    matrix.draftRows = { field: 'id', op: 'eq', value: 1 }
    const left = grants.value.slice(0, 2)
    grants.value = left
    await nextTick()
    matrix.saveEditor()
    await nextTick()
    expect(grants.value).toEqual(left)
    expect(grants.value.every((g) => g.rows === undefined)).toBe(true)
    expect(matrix.editorGone).toBe(true)
  })
})
