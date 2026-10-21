import { describe, expect, it } from 'vitest'
import { router } from '@/router'
import type { Change } from '@/stores/workspace'
import { routeFor } from './changeRoute'

describe('routeFor', () => {
  const kinds: Change['kind'][] = ['entity', 'role', 'user', 'settings']
  it.each(kinds)('resolves a changed %s to its page', (kind) => {
    const target = routeFor({ kind, name: 'pg.public.orders', type: 'modified' })!
    const resolved = router.resolve(target)
    expect(resolved.name).toBe(kind === 'settings' ? 'settings' : kind)
    if (kind !== 'settings') expect(resolved.fullPath).toContain('pg.public.orders')
  })
  it('leaves a removed item without a page', () => {
    expect(routeFor({ kind: 'entity', name: 'x', type: 'removed' })).toBeNull()
  })
})
