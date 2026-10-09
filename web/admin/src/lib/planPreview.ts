import { reactive } from 'vue'
import { cleanPlan, type Plan } from './plan'

// Evaluating a plan is asynchronous and plans change while it runs
// (excluding a table, renaming an entity, editing the workspace). Only the
// latest evaluation is shown, and a preview is taken only while it is the
// preview of the plan and workspace on screen.

export interface PreviewState<P> {
  preview: P | null
  error: string
  loading: boolean
  /** What the shown preview was evaluated for. */
  evaluated: { plan: string; content: string } | null
}

export function planKey(p: Plan): string {
  return JSON.stringify(cleanPlan(p))
}

/**
 * A previewer: evaluate(plan) runs fetch for the plan and the workspace
 * snapshot, keeping only the latest answer; current(plan, content) tells
 * whether the shown preview is that plan's on that workspace content.
 */
export function createPreviewer<P>(
  fetch: (plan: Plan) => Promise<P>, content: () => string,
) {
  const state = reactive({ preview: null, error: '', loading: false, evaluated: null }) as PreviewState<P>
  let seq = 0
  async function evaluate(plan: Plan) {
    const mine = ++seq
    const evaluated = { plan: planKey(plan), content: content() }
    state.loading = true
    state.error = ''
    try {
      const preview = await fetch(cleanPlan(plan))
      if (mine !== seq) return
      Object.assign(state, { preview, evaluated })
    } catch (e) {
      if (mine !== seq) return
      Object.assign(state, { preview: null, evaluated: null, error: e instanceof Error ? e.message : String(e) })
    } finally {
      if (mine === seq) state.loading = false
    }
  }
  const current = (plan: Plan, now: string) =>
    !state.loading && state.evaluated?.plan === planKey(plan) && state.evaluated.content === now
  /** Drops what was shown, and the answers still running. */
  function reset() {
    seq++
    Object.assign(state, { preview: null, error: '', loading: false, evaluated: null })
  }
  return { state, evaluate, current, reset }
}
