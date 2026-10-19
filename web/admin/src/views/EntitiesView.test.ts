import { describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createI18n } from 'vue-i18n'
import { createMemoryHistory, createRouter } from 'vue-router'
import { defineComponent, h } from 'vue'
import { NDialogProvider, NMessageProvider } from 'naive-ui'
import zh from '@/i18n/zh-CN'
import { session } from '@/api/session'
import type { EntityInput } from '@/gql/schema'
import { useWorkspace } from '@/stores/workspace'

vi.mock('@/api/client', () => ({ run: () => Promise.resolve({}) }))

const { default: EntitiesView } = await import('./EntitiesView.vue')

const entity = (name: string) => ({ name, datasource: 'shop', fields: [] }) as unknown as EntityInput

async function render(permissions: string[], query: Record<string, string> = {}) {
  session.permissions = permissions
  const pinia = createPinia()
  setActivePinia(pinia)
  useWorkspace().entities = [entity('users'), entity('orders')]
  const blank = defineComponent({ render: () => null })
  const router = createRouter({ history: createMemoryHistory(), routes: [
    { path: '/entities', name: 'entities', component: blank },
    { path: '/datasources', name: 'datasources', component: blank },
    { path: '/entities/:id', name: 'entity', component: blank },
  ] })
  await router.push({ name: 'entities', query })
  const i18n = createI18n({ legacy: false, locale: 'zh-CN', messages: { 'zh-CN': zh } })
  const Host = defineComponent(() => () => h(NMessageProvider, () => h(NDialogProvider, () => h(EntitiesView))))
  const wrapper = mount(Host, { global: { plugins: [pinia, i18n, router] } })
  await flushPromises()
  return wrapper.findAll('button').map((b) => b.text())
}

describe('EntitiesView entity sync', () => {
  it('is offered to administrators who may write', async () => {
    expect(await render(['admin:write'])).toContain('同步实体')
    expect(await render(['admin:*'])).toContain('同步实体')
  })
  it('is not offered to readers', async () => {
    expect((await render(['admin:read'])).some((b) => b.startsWith('同步'))).toBe(false)
  })
  it('syncs what the search matches', async () => {
    expect(await render(['admin:write'], { q: 'ord' })).toContain('同步匹配的 1 个')
  })
})
