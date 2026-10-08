import { computed, ref, watch } from 'vue'
import { darkTheme, useOsTheme } from 'naive-ui'

// Theme preference: follow the system (default), or force light or dark.
export type ThemeMode = 'auto' | 'light' | 'dark'

const storageKey = 'smcp.console.theme'

function load(): ThemeMode {
  try {
    const v = localStorage.getItem(storageKey)
    return v === 'light' || v === 'dark' ? v : 'auto'
  } catch {
    return 'auto'
  }
}

export const themeMode = ref<ThemeMode>(load())
watch(themeMode, (m) => {
  try {
    localStorage.setItem(storageKey, m)
  } catch {
    // Without storage the choice lasts for this page only.
  }
})

/** Modes with their message keys. */
export const themeModes: { value: ThemeMode; labelKey: string }[] = [
  { value: 'auto', labelKey: 'prefs.themeAuto' },
  { value: 'light', labelKey: 'prefs.themeLight' },
  { value: 'dark', labelKey: 'prefs.themeDark' },
]

/** The effective theme; call inside a component setup. */
export function useEffectiveTheme() {
  const os = useOsTheme()
  const dark = computed(() => (themeMode.value === 'auto' ? os.value === 'dark' : themeMode.value === 'dark'))
  return { dark, theme: computed(() => (dark.value ? darkTheme : null)) }
}
