import { describe, expect, it } from 'vitest'
import cases from '../../../../core/config/testdata/rules.json'
import schemaJSON from '../../../../core/config/schema.json'
import { validate, type Path, type Schema } from './jsonSchema'

// The same cases as the server's x/configyaml/rules_test.go. The console must
// not reject what the server accepts, and must flag single-field problems
// where the server does; cases with path null are conditional or cross-field
// rules only the server checks.
const root = schemaJSON as unknown as Schema
type RuleCase = { name: string; config: unknown; error?: string; path?: Path | null }

describe('shared validation cases', () => {
  for (const tc of cases as RuleCase[]) {
    it(tc.name, () => {
      const problems = validate(root, root, tc.config).filter((p) => p.severity === 'error')
      if (!tc.error || tc.path === null) {
        if (!tc.error) expect(problems).toEqual([])
        return
      }
      expect(problems.map((p) => p.path)).toContainEqual(tc.path)
    })
  }
})
