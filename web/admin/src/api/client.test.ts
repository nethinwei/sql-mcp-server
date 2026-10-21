import { afterEach, describe, expect, it, vi } from 'vitest'
import { parse } from 'graphql'
import { ApiError, run } from './client'
import { session } from './session'

// A request refused for its CSRF token while the service cannot read the
// session (it answers 503) keeps the page signed in, and fails as a network
// error the page reports.
describe('run, when the session cannot be read', () => {
  afterEach(() => vi.unstubAllGlobals())

  it('fails as a network error and stays signed in', async () => {
    Object.assign(session, { ready: true, username: 'root', permissions: ['admin:*'], csrfToken: 'old' })
    vi.stubGlobal('fetch', vi.fn(async (url: string | URL | Request) =>
      String(url).endsWith('graphql')
        ? new Response('{"error":"missing or invalid X-CSRF-Token"}', { status: 403 })
        : new Response('{"error":"the account cannot be read now"}', { status: 503 })))
    const failed = await run(parse('query Me { me { username } }'), {}).catch((e: unknown) => e)
    expect(failed).toBeInstanceOf(ApiError)
    expect((failed as ApiError).code).toBe('NETWORK')
    expect(session.username).toBe('root')
  })
})
