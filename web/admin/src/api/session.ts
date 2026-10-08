import { reactive } from 'vue'
import { t } from '@/i18n'

// The signed-in administrator. The session itself is an HttpOnly cookie; the
// CSRF token comes from /admin/login or /admin/session and is sent as a header.
export const session = reactive({
  ready: false,
  username: '',
  permissions: [] as string[],
  csrfToken: '',
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

/** Restores the session after a page load; false when signed out. */
export async function restoreSession(): Promise<boolean> {
  const res = await fetch('/admin/session', { credentials: 'same-origin' })
  apply(res.ok ? await res.json() : null)
  return res.ok
}

export async function login(username: string, password: string): Promise<string | null> {
  const res = await fetch('/admin/login', {
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
  await fetch('/admin/logout', {
    method: 'POST',
    credentials: 'same-origin',
    headers: { 'X-CSRF-Token': session.csrfToken },
  })
  apply(null)
}

export function signedOut() {
  apply(null)
}
