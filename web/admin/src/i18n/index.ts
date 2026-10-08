import { computed, watch } from 'vue'
import { createI18n } from 'vue-i18n'
import { dateEnUS, dateZhCN, enUS, zhCN } from 'naive-ui'
import zh from './zh-CN'
import en from './en'

export type Locale = 'zh-CN' | 'en'

const storageKey = 'smcp.console.locale'

export const locales: { value: Locale; label: string }[] = [
  { value: 'zh-CN', label: '简体中文' },
  { value: 'en', label: 'English' },
]

/** Saved choice, else the browser language (Chinese → zh-CN, otherwise English). */
function initialLocale(): Locale {
  try {
    const saved = localStorage.getItem(storageKey)
    if (saved === 'zh-CN' || saved === 'en') return saved
  } catch {
    // Storage may be unavailable; fall back to the browser language.
  }
  return navigator.language.toLowerCase().startsWith('zh') ? 'zh-CN' : 'en'
}

export const i18n = createI18n({
  legacy: false,
  locale: initialLocale(),
  fallbackLocale: 'zh-CN',
  messages: { 'zh-CN': zh, en },
})

export const locale = i18n.global.locale

watch(locale, (l) => {
  try {
    localStorage.setItem(storageKey, l)
  } catch {
    // Without storage the choice lasts for this page only.
  }
  document.documentElement.lang = l
  document.title = i18n.global.t('app.title') + ' · sql-mcp-server'
}, { immediate: true })

/** Naive UI's own strings and date formats follow the console locale. */
export const naiveLocale = computed(() => (locale.value === 'en' ? enUS : zhCN))
export const naiveDateLocale = computed(() => (locale.value === 'en' ? dateEnUS : dateZhCN))

/** Translates outside components (stores, helpers). */
export const t = (key: string, params?: Record<string, unknown>) => i18n.global.t(key, params ?? {})
