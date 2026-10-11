import { reactive } from 'vue'

// One asynchronous read and where it stands: loading, read (ok), not found
// (the server answered null), or failed. Only the latest load counts: an
// older answer arriving late is dropped. Views compute and act only on ok.

export type ResourceStatus = 'idle' | 'loading' | 'ok' | 'notFound' | 'error'

export interface ResourceState<T> {
  status: ResourceStatus
  data: T | null
  error: string
}

export function createResource<T, A extends unknown[]>(fetch: (...args: A) => Promise<T | null | undefined>) {
  const state = reactive({ status: 'idle', data: null, error: '' }) as ResourceState<T>
  let seq = 0
  async function load(...args: A) {
    const mine = ++seq
    Object.assign(state, { status: 'loading', data: null, error: '' })
    try {
      const data = await fetch(...args)
      if (mine !== seq) return
      Object.assign(state, data == null ? { status: 'notFound' } : { status: 'ok', data })
    } catch (e) {
      if (mine === seq) Object.assign(state, { status: 'error', error: e instanceof Error ? e.message : String(e) })
    }
  }
  function reset() {
    seq++
    Object.assign(state, { status: 'idle', data: null, error: '' })
  }
  return { state, load, reset }
}
