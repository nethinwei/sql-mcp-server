import { defineStore } from 'pinia'
import { run } from '@/api/client'
import { StatusQuery } from '@/api/ops'
import { workspace } from '@/lib/workspaceSync'
import { useWorkspace } from '@/stores/workspace'
import type { StatusQuery as StatusResult } from '@/gql/graphql'

// What the server reports: the published revision and the revision the
// serving process applies. The layout polls it; publishing refreshes it.
export const useStatus = defineStore('status', {
  state: () => ({
    server: null as StatusResult['serverStatus'] | null,
    published: null as StatusResult['published'] | null,
    loaded: false,
    settling: false,
  }),
  getters: {
    publishedId: (s) => s.published?.id ?? null,
  },
  actions: {
    async refresh() {
      try {
        const data = await run(StatusQuery, {})
        this.server = data.serverStatus
        this.published = data.published ?? null
        this.loaded = true
      } catch {
        // A transient failure keeps the last status; the next poll retries.
      }
    },

    /**
     * Refreshes, once a second for up to 15 seconds, until the serving process
     * applies the published revision (it reloads a moment after a publish) or
     * reports why it does not; a status read right at the publish would show
     * the revision it served before.
     */
    async settle() {
      if (this.settling) return
      this.settling = true
      try {
        for (let i = 0; i < 15; i++) {
          const s = this.server
          if (!s?.watching || !this.publishedId || s.appliedRevision === this.publishedId || s.unapplied) return
          await new Promise((r) => setTimeout(r, 1000))
          await this.refresh()
        }
      } finally {
        this.settling = false
      }
    },

    /**
     * Re-applies the unsaved changes onto the published revision; conflicts
     * are left in the workspace to resolve.
     */
    async rebaseWorkspace(): Promise<number> {
      const ws = useWorkspace()
      const id = this.publishedId
      if (!id) return 0
      const count = ws.changes.length
      await workspace.update(id)
      return count
    },
  },
})
