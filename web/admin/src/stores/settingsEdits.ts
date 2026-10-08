import { defineStore } from 'pinia'
import { useWorkspace } from '@/stores/workspace'

// Text buffers of the settings page, one per top-level section. Settings are
// edited like everything else: valid JSON goes into the workspace as it is
// typed (and so into the change list, review and rebase merging); only text
// that is not valid JSON stays here, pending, until it is fixed or reverted.
// The buffers live in a store so that leaving the page keeps such text.

const pretty = (v: unknown) => JSON.stringify(v, null, 2)
const same = (a: unknown, b: unknown) => JSON.stringify(a) === JSON.stringify(b)

export const useSettingsEdits = defineStore('settingsEdits', {
  state: () => ({
    texts: {} as Record<string, string>,
    errors: {} as Record<string, string>,
  }),

  getters: {
    /** Sections whose text is not valid JSON, so not in the workspace. */
    pending(state): string[] {
      return Object.keys(state.errors)
    },
  },

  actions: {
    /**
     * Follows the workspace: each valid buffer shows its section (keeping the
     * user's formatting when the value is the same); invalid text is kept.
     */
    sync() {
      const ws = useWorkspace()
      for (const key of Object.keys(this.texts)) {
        if (!(key in ws.settings) && !(key in this.errors)) delete this.texts[key]
      }
      for (const [key, value] of Object.entries(ws.settings)) {
        if (key in this.errors) continue
        if (key in this.texts) {
          try {
            if (same(JSON.parse(this.texts[key]), value)) continue
          } catch {
            // Not valid any more: replaced below.
          }
        }
        this.texts[key] = pretty(value)
      }
    },

    /** Takes typed text; valid JSON updates the workspace section. */
    edit(key: string, text: string) {
      this.texts[key] = text
      let value: unknown
      try {
        value = JSON.parse(text)
      } catch (e) {
        this.errors[key] = e instanceof Error ? e.message : String(e)
        return
      }
      delete this.errors[key]
      const ws = useWorkspace()
      if (!same(ws.settings[key], value)) ws.setSettings({ ...ws.settings, [key]: value })
    },

    /** Puts a section back to the revision the workspace is based on. */
    revert(key: string) {
      const ws = useWorkspace()
      const base = (JSON.parse(ws.baseline || '{}') as { settings?: Record<string, unknown> }).settings ?? {}
      const settings = { ...ws.settings }
      if (key in base) settings[key] = base[key]
      else delete settings[key]
      delete this.errors[key]
      ws.setSettings(settings)
      this.texts[key] = pretty(settings[key])
    },

    format(key: string) {
      if (key in this.errors) return
      try {
        this.texts[key] = pretty(JSON.parse(this.texts[key]))
      } catch {
        // Validity is tracked by edit().
      }
    },
  },
})
