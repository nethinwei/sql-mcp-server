import { afterEach, describe, expect, it, vi } from 'vitest'
import { resyncSession, session } from './session'

function signedInAs(username: string, csrfToken = 'old') {
  Object.assign(session, { ready: true, username, permissions: ['admin:*'], csrfToken })
}

function serverSession(body: object | null) {
  vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify(body ?? { error: 'signed out' }), {
    status: body ? 200 : 401,
  })))
}

describe('resyncSession', () => {
  afterEach(() => vi.unstubAllGlobals())

  it('takes the new CSRF token when the browser is still the same administrator', async () => {
    signedInAs('root')
    serverSession({ username: 'root', permissions: ['admin:*'], csrfToken: 'new' })
    expect(await resyncSession()).toBe(true)
    expect(session.csrfToken).toBe('new')
  })

  it('signs this page out rather than acting as another administrator', async () => {
    signedInAs('root')
    serverSession({ username: 'wei.pm', permissions: ['admin:read'], csrfToken: 'theirs' })
    expect(await resyncSession()).toBe(false)
    expect(session.username).toBe('')
    expect(session.csrfToken).toBe('')
  })

  it('reads the session once for concurrent refusals', async () => {
    signedInAs('root')
    serverSession({ username: 'root', permissions: ['admin:*'], csrfToken: 'new' })
    expect(await Promise.all([resyncSession(), resyncSession()])).toEqual([true, true])
    expect(fetch).toHaveBeenCalledTimes(1)
  })
})
