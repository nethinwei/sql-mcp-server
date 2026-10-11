import { describe, expect, it } from 'vitest'

// base.ts reads the page's <base> once; each case loads it under another.
async function under(href: string) {
  document.head.innerHTML = `<base href="${href}">`
  const { vi } = await import('vitest')
  vi.resetModules()
  return import('./base')
}

describe('console base', () => {
  it('derives the API, router and service paths from the page base', async () => {
    const b = await under('/admin/')
    expect([b.adminBase, b.adminURL('graphql'), b.serviceBase]).toEqual(['/admin/', '/admin/graphql', '/'])
  })

  it('follows a gateway path prefix', async () => {
    const b = await under('/sql-mcp-server/admin/')
    expect([b.adminBase, b.adminURL('events'), b.serviceBase])
      .toEqual(['/sql-mcp-server/admin/', '/sql-mcp-server/admin/events', '/sql-mcp-server/'])
  })
})
