import { defineStore } from 'pinia'
import { run } from '@/api/client'
import { StatusQuery } from '@/api/ops'
import { useWorkspace } from '@/stores/workspace'
import type { StatusQuery as StatusResult } from '@/gql/graphql'

// What the server reports: the published revision and the revision the
// serving process applies. The layout polls it; publishing refreshes it.
export const useStatus = defineStore('status', {
  state: () => ({
    server: null as StatusResult['serverStatus'] | null,
    published: null as StatusResult['published'] | null,
    loaded: false,
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
     * Re-applies the unsaved changes onto the published revision; conflicts
     * are left in the workspace to resolve.
     */
    async rebaseWorkspace(): Promise<number> {
      const ws = useWorkspace()
      const id = this.publishedId
      if (!id) return 0
      const count = ws.changes.length
      await ws.rebase(id)
      return count
    },
  },
})
