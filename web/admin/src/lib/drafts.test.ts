import { describe, expect, it } from 'vitest'
import { pendingDrafts, type DraftRevision } from './drafts'

const draft = (id: string, basedOn: string | null) =>
  ({ basedOn, revision: { id, parent: basedOn, state: 'DRAFT', author: 'root', comment: '', createdAt: '' } }) as DraftRevision

describe('pending drafts', () => {
  const all = [draft('23', '20'), draft('21', '19'), draft('18', null)]

  it('are the drafts but those taken, telling those not built on the published revision', () => {
    expect(pendingDrafts(all, '20', ['18']).map((d) => [d.revision.id, d.stale])).toEqual([['23', false], ['21', true]])
  })
})
