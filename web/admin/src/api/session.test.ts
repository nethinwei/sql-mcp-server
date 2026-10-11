import { afterEach, describe, expect, it, vi } from 'vitest'
import { adminBase } from './base'
import { consolePage, isLeaving, leave, resyncSession, session } from './session'

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

// Ending the session loads the sign-in page anew: nothing begun for the
// administrator signed in until then can go on for whoever signs in next.
describe('leave', () => {
  afterEach(() => vi.unstubAllGlobals())

  it('signs out and loads the sign-in page, coming back to where it was', () => {
    signedInAs('root')
    const replace = vi.fn()
    vi.stubGlobal('location', { ...window.location, replace })
    leave('/roles/analyst?tab=grants')
    expect(session.username).toBe('')
    expect(isLeaving()).toBe(true) // the page's leave guard lets it go
    expect(replace).toHaveBeenCalledWith(`${adminBase}login?next=${encodeURIComponent('/roles/analyst?tab=grants')}`)
  })

  it('goes only to pages of the console after signing in', () => {
    expect(consolePage('/roles/analyst')).toBe(`${adminBase}roles/analyst`)
    expect(consolePage('//evil.example/x')).toBe(`${adminBase}evil.example/x`)
    expect(consolePage('https://evil.example')).toBe(adminBase)
  })
})

// Signing out within the page (a route to the sign-in page) would keep what
// was begun for the administrator alive: every way out goes through leave.
describe('the ways out of a session', () => {
  it('all load the page anew', async () => {
    const { readdirSync, readFileSync } = await import('node:fs')
    const { join } = await import('node:path')
    const root = join(__dirname, '..')
    const files = (readdirSync(root, { recursive: true }) as string[])
      .filter((f) => /\.(ts|vue)$/.test(f) && !f.includes('.test.') && !f.startsWith('gql'))
    const offending = files.filter((f) => f !== 'router.ts' &&
      /(push|replace)\(\{\s*name:\s*'login'/.test(readFileSync(join(root, f), 'utf8')))
    expect(offending).toEqual([])
  })
})
