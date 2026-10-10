import { describe, expect, it } from 'vitest'
import fixture from '../../../../x/admin/graph/testdata/workspace_sections.json'
import { revisionSections } from './workspace'

type Revision = Parameters<typeof revisionSections>[0]

// The server writes a configuration into a workspace (an agent applying a
// plan) as the console builds sections from a revision: the same fixture
// as x/admin/graph's TestWorkspaceSectionsAsTheConsoleBuildsThem.
describe('workspace sections', () => {
  it('are what the server writes for the same configuration', () => {
    const sections = revisionSections({ config: withTypenames(fixture.configuration as unknown as Config) } as unknown as Revision)
    expect(JSON.parse(JSON.stringify(sections))).toEqual(fixture.sections)
  })
})

type Read = Record<string, unknown> & { __typename?: string }
type Config = { entities: Read[]; roles: Read[]; users: Read[]; settings: unknown }

/**
 * The configuration as the GraphQL client reads it: each object of a GraphQL
 * type with a __typename (JSON values, like settings or a join, have none),
 * which the sections never hold.
 */
function withTypenames(c: Config): Config {
  const typed = (o: Read, name: string): Read => ({ ...o, __typename: name })
  const grants = (gs: unknown) => (gs as Read[]).map((g) => typed(g, 'Grant'))
  return {
    entities: c.entities.map((e) => typed({
      ...e,
      mcp: typed(e.mcp as Read, 'EntityMcp'),
      fields: (e.fields as Read[]).map((f) => typed(f, 'Field')),
      relationships: (e.relationships as Read[]).map((r) => typed(r, 'Relationship')),
    }, 'Entity')),
    roles: c.roles.map((r) => typed({ ...r, grants: grants(r.grants) }, 'Role')),
    users: c.users.map((u) => typed({ ...u, grants: grants(u.grants) }, 'User')),
    settings: c.settings,
  }
}
