import type { DraftsQuery } from '@/gql/graphql'

// Drafts waiting for review: every revision still a draft (saved by another
// administrator, or by this one from the review page), but those this
// workspace took in. The server says what each builds on (its chain of draft
// parents walked over the whole history); one that does not build on the
// published revision cannot be published as it is (the server only
// fast-forwards) and is reviewed in the workspace, onto it, first.

export type DraftRevision = DraftsQuery['drafts'][number]

export interface PendingDraft {
  revision: DraftRevision['revision']
  basedOn: string | null
  /** It does not build on the published revision. */
  stale: boolean
}

/** The drafts, newest first, but those taken into this workspace. */
export function pendingDrafts(drafts: DraftRevision[], published: string | null, taken: string[] = []): PendingDraft[] {
  return drafts
    .filter((d) => !taken.includes(d.revision.id))
    .map((d) => ({ revision: d.revision, basedOn: d.basedOn ?? null, stale: published !== null && d.basedOn !== published }))
}

/** The first line of a comment. */
export const firstLine = (comment: string) => comment.split('\n')[0] ?? ''
