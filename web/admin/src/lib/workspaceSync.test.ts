import { beforeEach, describe, expect, it, vi } from 'vitest'
import { effectScope } from 'vue'
import { createPinia, setActivePinia } from 'pinia'
import { session } from '@/api/session'
import { useWorkspace, WorkspaceSwitched } from '@/stores/workspace'
import type { WorkspacePartsFragment } from '@/gql/graphql'

vi.mock('vue-i18n', async (actual) => ({ ...(await actual<object>()), useI18n: () => ({ t: (k: string) => k }) }))
vi.mock('vue-router', () => ({ useRouter: () => ({ push: vi.fn() }) }))
vi.mock('naive-ui', async (actual) => ({ ...(await actual<object>()), useNotification: () => ({ info: vi.fn() }) }))

// Every request the console makes, by operation name, and a way to hold one.
const sent: string[] = []
let hold: ((name: string) => Promise<void>) | null = null
vi.mock('@/api/client', () => ({
  run: async (doc: { definitions: { name?: { value: string } }[] }) => {
    const name = doc.definitions[0]?.name?.value ?? ''
    sent.push(`${session.username}:${name}`)
    await hold?.(name)
    return { [name.charAt(0).toLowerCase() + name.slice(1)]: answer() }
  },
}))

// The base revision the server answers the workspace on.
let answerBase = '1'

function answer(): WorkspacePartsFragment {
  return { base: answerBase, version: 1, items: { complete: true, items: [
    { kind: 'role', key: 'analyst', value: { name: 'analyst' } }, { kind: 'settings', key: 'settings', value: {} },
  ] }, updatedBy: 'root', taken: [], checkpoint: null, conflicts: [], notes: [] }
}

const { useWorkspaceSync, workspace } = await import('./workspaceSync')

describe('workspace operations across a change of administrator', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    sent.length = 0
    hold = null
    answerBase = '1'
    session.username = 'root'
  })

  it('are not sent for the administrator signed in meanwhile', async () => {
    const ws = useWorkspace()
    ws.bindTo('root')
    const scope = effectScope()
    scope.run(() => useWorkspaceSync())
    ws.$patch({ baseId: '1', basePublished: '1' })
    ws.take(answer())
    ws.upsertRole({ name: 'mine' })
    // root's edit is being written when another administrator signs in.
    let release = () => {}
    hold = (name) => (name === 'EditWorkspace' ? new Promise((r) => (release = r)) : Promise.resolve())
    const reset = workspace.reset('1')
    await vi.waitFor(() => expect(sent).toContain('root:EditWorkspace'))
    scope.stop()
    session.username = 'other'
    ws.bindTo('other')
    release()
    await expect(reset).rejects.toThrow()
    expect(sent.filter((s) => s.startsWith('other:'))).toEqual([])
  })
})

describe('an operation answered across a change of administrator', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    sent.length = 0
    hold = null
    answerBase = '1'
    session.username = 'root'
    session.permissions = ['admin:*']
  })

  it('fails when another signed in while its base revision was read', async () => {
    const ws = useWorkspace()
    ws.bindTo('root')
    const scope = effectScope()
    scope.run(() => useWorkspaceSync())
    ws.$patch({ baseId: '1', basePublished: '1' })
    ws.take(answer())
    // The workspace moves onto revision 2: its base is read after the answer.
    answerBase = '2'
    let release = () => {}
    hold = (name) => (name === 'Workspace' ? new Promise((r) => (release = r)) : Promise.resolve())
    const reset = workspace.reset('2')
    await vi.waitFor(() => expect(sent).toContain('root:Workspace'))
    scope.stop()
    session.username = 'other'
    ws.bindTo('other')
    release()
    await expect(reset).rejects.toThrow(WorkspaceSwitched)
  })
})

describe('following a new publish', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    sent.length = 0
    hold = null
    answerBase = '1'
    session.username = 'root'
  })

  it('reads the workspace again for an administrator who cannot write', async () => {
    session.permissions = ['admin:read']
    const ws = useWorkspace()
    ws.bindTo('root')
    const scope = effectScope()
    scope.run(() => useWorkspaceSync())
    ws.$patch({ baseId: '1', basePublished: '1' })
    ws.take(answer())
    await workspace.follow('1')
    expect(sent).toEqual(['root:WorkspaceState'])
    session.permissions = ['admin:*']
    await workspace.follow('1')
    expect(sent).toContain('root:UpdateWorkspace')
    scope.stop()
  })
})
