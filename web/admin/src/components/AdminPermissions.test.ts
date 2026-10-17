import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { defineComponent, h, nextTick, ref } from 'vue'
import zh from '@/i18n/zh-CN'
import AdminPermissions from './AdminPermissions.vue'

function render(initial: string[]) {
  const value = ref(initial)
  const Host = defineComponent(() => () => h(AdminPermissions, { modelValue: value.value,
    'onUpdate:modelValue': (v: string[]) => { value.value = v } }))
  const i18n = createI18n({ legacy: false, locale: 'zh-CN', messages: { 'zh-CN': zh } })
  const wrapper = mount(Host, { global: { plugins: [i18n] } })
  const boxes = () => wrapper.findAll('.n-checkbox')
  const state = () => boxes().map((b) => ({
    checked: b.classes().includes('n-checkbox--checked'), disabled: b.classes().includes('n-checkbox--disabled') }))
  return { value, boxes, state }
}

describe('AdminPermissions', () => {
  // admin:* grants every other permission: chosen, it is kept alone and the
  // others show granted and locked; unchosen, the others come back.
  it('locks the others while everything is chosen', async () => {
    const { value, boxes, state } = render(['admin:read'])
    await boxes()[4].trigger('click')
    await nextTick()
    expect(value.value).toEqual(['admin:*'])
    expect(state().slice(0, 4)).toEqual(Array(4).fill({ checked: true, disabled: true }))
    await boxes()[1].trigger('click')
    expect(value.value).toEqual(['admin:*'])
    await boxes()[4].trigger('click')
    await nextTick()
    expect(value.value).toEqual(['admin:read'])
    expect(state()[1]).toEqual({ checked: false, disabled: false })
  })
})
