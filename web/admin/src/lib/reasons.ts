import { i18n } from '@/i18n'

// An explanation the server gives (see the Reason type of the admin API):
// translated by its code, its parts first, the server's English text when
// the console has no message for the code.
export interface ReasonLike {
  code: string
  params?: unknown
  parts?: { name: string; code: string; params?: unknown; text: string }[] | null
  text: string
}

function translate(code: string, params: Record<string, unknown>, fallback: string): string {
  const key = `reasons.${code}`
  return code && i18n.global.te(key) ? i18n.global.t(key, params) : fallback
}

function paramsOf(params: unknown): Record<string, unknown> {
  return params && typeof params === 'object' ? { ...(params as Record<string, unknown>) } : {}
}

/** The explanation in the console's language; fallback when there is none. */
export function reasonText(r: ReasonLike | null | undefined, fallback = ''): string {
  if (!r) return fallback
  const params = paramsOf(r.params)
  for (const p of r.parts ?? []) params[p.name] = translate(p.code, paramsOf(p.params), p.text)
  return translate(r.code, params, r.text)
}
