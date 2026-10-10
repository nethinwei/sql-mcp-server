import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createI18n } from 'vue-i18n'
import RuntimeBadge from './RuntimeBadge.vue'
import zh from '@/i18n/zh-CN'
import { useStatus } from '@/stores/status'

type Server = NonNullable<ReturnType<typeof useStatus>['server']>

function render(server: Partial<Server>, published: string) {
  const pinia = createPinia()
  setActivePinia(pinia)
  const status = useStatus()
  status.server = { appliedRevision: '3', watching: true, unapplied: null, execution: null, ...server } as Server
  status.published = { id: published } as NonNullable<typeof status.published>
  const i18n = createI18n({ legacy: false, locale: 'zh-CN', messages: { 'zh-CN': zh } })
  return mount(RuntimeBadge, { global: { plugins: [pinia, i18n] } }).text()
}

describe('RuntimeBadge', () => {
  it('shows the revision served', () => {
    expect(render({}, '3')).toContain('运行中 #3')
  })
  it('shows a published revision on its way', () => {
    expect(render({}, '4')).toContain('正在加载 #4')
  })
  it('shows a revision that failed to apply', () => {
    expect(render({ unapplied: { id: '4', restartRequired: false, error: 'x' } }, '4')).toContain('#4 应用失败')
  })
  it('shows a revision waiting for a restart', () => {
    expect(render({ unapplied: { id: '4', restartRequired: true, error: '' } }, '4')).toContain('#4 待重启生效')
  })
})
