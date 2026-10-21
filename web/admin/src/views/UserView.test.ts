import { describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createI18n } from 'vue-i18n'
import { createMemoryHistory, createRouter } from 'vue-router'
import { defineComponent, h } from 'vue'
import { NDialogProvider, NMessageProvider } from 'naive-ui'
import zh from '@/i18n/zh-CN'
import { session } from '@/api/session'
import { GenerateUserTokenMutation } from '@/api/ops'
import { useWorkspace } from '@/stores/workspace'

const run = vi.hoisted(() => vi.fn())
vi.mock('@/api/client', () => ({ run }))

const { default: UserView } = await import('./UserView.vue')

// /users/:name?token=1 mints the user's first token; on a cold load the
// workspace arrives after the page, and the token waits for the user.
describe('UserView first token', () => {
  it('is minted once the user is loaded, into the user', async () => {
    run.mockImplementation((doc: unknown) => Promise.resolve(doc === GenerateUserTokenMutation
      ? { generateUserToken: { token: 'secret', tokenHash: 'sha256:x' } }
      : {}))
    session.permissions = ['admin:write']
    const pinia = createPinia()
    setActivePinia(pinia)
    const blank = defineComponent({ render: () => null })
    const router = createRouter({ history: createMemoryHistory(), routes: [
      { path: '/users/:name', name: 'user', component: blank },
      { path: '/users', name: 'users', component: blank },
    ] })
    await router.push({ name: 'user', params: { name: 'alice' }, query: { token: '1' } })
    const i18n = createI18n({ legacy: false, locale: 'zh-CN', messages: { 'zh-CN': zh } })
    const Host = defineComponent(() => () =>
      h(NMessageProvider, () => h(NDialogProvider, () => h(UserView, { name: 'alice' }))))
    mount(Host, { global: { plugins: [pinia, i18n, router], stubs: { GrantMatrix: true, VisibilityTable: true,
      PrincipalLimits: true, McpConnect: true } } })
    await flushPromises()
    const minted = () => run.mock.calls.filter(([doc]) => doc === GenerateUserTokenMutation).length
    expect(minted()).toBe(0)

    const ws = useWorkspace()
    ws.users = [{ name: 'alice', roles: [] }]
    await flushPromises()
    expect(minted()).toBe(1)
    expect(ws.user('alice')!.tokenHash).toBe('sha256:x')

    ws.users = [{ name: 'alice', roles: [], tokenHash: 'sha256:x' }]
    await flushPromises()
    expect(minted()).toBe(1)
  })
})
