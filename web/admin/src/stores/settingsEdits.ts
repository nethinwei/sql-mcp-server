import { defineStore } from 'pinia'
import { useWorkspace } from '@/stores/workspace'

// Edit buffers of the settings page, one per top-level section. They live in
// a store so that leaving the page keeps unapplied text, and a section only
// follows workspace changes while it is not being edited. A buffer is pending
// when the user changed it, not when the workspace moved on (loading another
// revision leaves unedited buffers clean; they take the new value on sync).

const pretty = (v: unknown) => JSON.stringify(v, null, 2)

export const useSettingsEdits = defineStore('settingsEdits', {
  state: () => ({
    texts: {} as Record<string, string>,
    /** The workspace value each buffer started from. */
    based: {} as Record<string, string>,
    errors: {} as Record<string, string>,
  }),

  getters: {
    /** Sections the user edited and has not applied. */
    pending(state): string[] {
      const ws = useWorkspace()
      return Object.keys(state.texts).filter((k) => k in ws.settings && state.texts[k] !== state.based[k])
    },
  },

  actions: {
    /** Follows the workspace: unedited sections take its value, edited ones are kept. */
    sync() {
      const ws = useWorkspace()
      for (const key of Object.keys(this.texts)) {
        if (!(key in ws.settings)) {
          delete this.texts[key]
          delete this.based[key]
          delete this.errors[key]
        }
      }
      for (const [key, value] of Object.entries(ws.settings)) {
        const now = pretty(value)
        if (!(key in this.texts) || this.texts[key] === this.based[key]) {
          this.texts[key] = now
          this.based[key] = now
          delete this.errors[key]
        }
      }
    },

    restore(key: string) {
      const ws = useWorkspace()
      this.texts[key] = this.based[key] = pretty(ws.settings[key])
      delete this.errors[key]
    },

    parse(key: string): { ok: true; value: unknown } | { ok: false } {
      try {
        const value: unknown = JSON.parse(this.texts[key])
        delete this.errors[key]
        return { ok: true, value }
      } catch (e) {
        this.errors[key] = e instanceof Error ? e.message : String(e)
        return { ok: false }
      }
    },

    /**
     * Applies the named sections to the workspace at once; nothing is applied
     * when any of them is not valid JSON. Returns whether it applied.
     */
    apply(keys: string[]): boolean {
      const parsed: Record<string, unknown> = {}
      let ok = true
      for (const key of keys) {
        const r = this.parse(key)
        if (r.ok) parsed[key] = r.value
        else ok = false
      }
      if (!ok) return false
      const ws = useWorkspace()
      for (const key of keys) this.based[key] = this.texts[key] = pretty(parsed[key])
      ws.setSettings({ ...ws.settings, ...parsed })
      return true
    },
  },
})
