import { describe, expect, it } from 'vitest'
import { parseSimulationQuery, simulationRoute } from './simulation'

describe('simulation links', () => {
  it('round-trips a picked call', () => {
    const route = simulationRoute({ who: 'crm-agent', source: 'workspace', entity: 'customers', action: 'READ', fields: ['id', 'name'] })
    const query = (route as { query: Record<string, string> }).query
    expect(parseSimulationQuery(query)).toEqual({
      who: 'crm-agent', source: 'workspace', entity: 'customers', action: 'READ', fields: ['id', 'name'],
    })
  })

  it('drops unknown parts', () => {
    expect(parseSimulationQuery({ user: 'x', action: 'DROP', source: 'elsewhere' }))
      .toEqual({ who: 'x', source: undefined, entity: undefined, action: undefined, fields: [] })
  })
})
