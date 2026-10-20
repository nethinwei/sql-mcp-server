import { reactive } from 'vue'
import { t } from '@/i18n'
import { adminBase, adminURL } from './base'

// The signed-in administrator. The session itself is an HttpOnly cookie; the
// CSRF token comes from /admin/login or /admin/session and is sent as a header.
export const session = reactive({
  ready: false,
  username: '',
  permissions: [] as string[],
  csrfToken: '',
  /** The session could not be read (the service unreachable or failing), as opposed to signed out. */
  unavailable: false,
})

export type Permission = 'admin:read' | 'admin:write' | 'admin:publish' | 'admin:accounts'

export function can(permission: Permission): boolean {
  return session.permissions.includes('admin:*') || session.permissions.includes(permission)
}

interface SessionBody {
  username: string
  permissions: string[]
  csrfToken: string
  error?: string
}

function apply(body: SessionBody | null) {
  session.username = body?.username ?? ''
  session.permissions = body?.permissions ?? []
  session.csrfToken = body?.csrfToken ?? ''
  session.ready = true
}

/**
 * Restores the session after a page load; false when signed out, or when it
 * could not be read (unavailable: the sign-in page says so).
 */
export async function restoreSession(): Promise<boolean> {
  let res: Response
  try {
    res = await fetch(adminURL('session'), { credentials: 'same-origin' })
  } catch {
    apply(null)
    session.unavailable = true
    return false
  }
  session.unavailable = !res.ok && res.status !== 401
  apply(res.ok ? await res.json() : null)
  return res.ok
}

/**
 * Re-reads the session after the server refused this page's CSRF token: a
 * sign-in in another tab of this browser replaces the session cookie. True
 * when the browser is still signed in as the same administrator, whose new
 * token this page then uses; otherwise this page is signed out, so it never
 * acts as another administrator.
 */
let resyncing: Promise<boolean> | null = null

export function resyncSession(): Promise<boolean> {
  resyncing ??= (async () => {
    const was = session.username
    const res = await fetch(adminURL('session'), { credentials: 'same-origin' })
    // Not read now (the service failing): still signed in, as far as known.
    if (!res.ok && res.status !== 401) throw new Error(t('network.unreachable'))
    const body = res.ok ? ((await res.json()) as SessionBody) : null
    const same = Boolean(body && was && body.username === was)
    apply(same ? body : null)
    return same
  })().finally(() => { resyncing = null })
  return resyncing
}

export async function login(username: string, password: string): Promise<string | null> {
  const res = await fetch(adminURL('login'), {
    method: 'POST',
    credentials: 'same-origin',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ username, password }),
  })
  const body = (await res.json().catch(() => ({ error: t('login.failed') }))) as SessionBody
  if (!res.ok) return body.error ?? t('login.failed')
  apply(body)
  return null
}

export async function logout() {
  await fetch(adminURL('logout'), {
    method: 'POST',
    credentials: 'same-origin',
    headers: { 'X-CSRF-Token': session.csrfToken },
  })
  apply(null)
}

/**
 * The console's page for path (a route path such as /roles/x), loaded
 * anew. Only paths of the console: anything else is its home page.
 */
export function consolePage(path: string): string {
  return adminBase + (path.startsWith('/') ? path.replace(/^\/+/, '') : '')
}

/**
 * Ends this page's session: the browser loads the sign-in page anew (next:
 * where to go after signing in). Nothing begun for the administrator signed
 * in until now (a save waiting for another, a plan being applied) survives
 * the page, so none of it can ever be sent for whoever signs in next.
 */
let leaving = false

/** Whether the page is being left for the sign-in page (see leave). */
export const isLeaving = () => leaving

export function leave(next?: string) {
  leaving = true
  apply(null)
  const query = next && next !== '/' ? `?next=${encodeURIComponent(next)}` : ''
  window.location.replace(consolePage('/login') + query)
}
