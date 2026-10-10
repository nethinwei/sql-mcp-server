import { describe, expect, it } from 'vitest'
import zh from './zh-CN'

// Every literal message key the console uses exists. en.ts has the same keys
// as zh-CN.ts (its type says so), so checking zh-CN covers both. Keys built at
// run time (t(`plan.${kind}`)) are not literals and are not checked here.
const sources = import.meta.glob(['../**/*.{ts,vue}', '!../**/*.test.ts', '!../gql/**'], {
  query: '?raw', import: 'default', eager: true,
}) as Record<string, string>

const call = /(?:\$t|\bt|\bte)\(\s*'([A-Za-z][\w-]*(?:\.[\w-]+)+)'/g

function has(messages: unknown, key: string): boolean {
  let node = messages
  for (const part of key.split('.')) {
    if (typeof node !== 'object' || node === null || !(part in node)) return false
    node = (node as Record<string, unknown>)[part]
  }
  return typeof node === 'string' || typeof node === 'function'
}

describe('message keys', () => {
  it('every literal key exists', () => {
    const missing: string[] = []
    let seen = 0
    for (const [file, source] of Object.entries(sources)) {
      for (const [, key] of source.matchAll(call)) {
        seen++
        if (!has(zh, key)) missing.push(`${file}: ${key}`)
      }
    }
    expect(seen).toBeGreaterThan(100)
    expect(missing).toEqual([])
  })
})
