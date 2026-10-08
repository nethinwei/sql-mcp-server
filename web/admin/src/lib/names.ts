import { t } from '@/i18n'

// Role and user names: lower-case letters, digits, '-' and '_' (core/config).
const accessNameRe = /^[a-z0-9][a-z0-9_-]*$/

export function nameError(name: string, taken: string[]): string | null {
  if (!name) return t('common.required')
  if (!accessNameRe.test(name)) return t('names.invalid')
  if (taken.includes(name)) return t('names.taken')
  return null
}
