import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { liveUpdates, type EventSourceLike } from './liveUpdates'

class FakeSource implements EventSourceLike {
  listeners: Record<string, (() => void)[]> = {}
  closed = false
  addEventListener(type: string, listener: () => void) {
    ;(this.listeners[type] ??= []).push(listener)
  }
  close() {
    this.closed = true
  }
  emit(type: string) {
    for (const l of this.listeners[type] ?? []) l()
  }
}

describe('liveUpdates', () => {
  beforeEach(() => vi.useFakeTimers())
  afterEach(() => vi.useRealTimers())

  function setup() {
    const source = new FakeSource()
    const poll = vi.fn(async () => {})
    const published = vi.fn()
    const opened = vi.fn()
    const live = liveUpdates({
      url: '/events', poll, events: { published, open: opened },
      interval: 1000, connectedInterval: 5000, createSource: () => source,
    })
    live.start()
    return { source, poll, published, opened, live }
  }

  it('polls often without the stream and slowly with it', () => {
    const { source, poll, live } = setup()
    vi.advanceTimersByTime(3000)
    expect(poll).toHaveBeenCalledTimes(3)
    source.emit('open')
    expect(live.connected()).toBe(true)
    vi.advanceTimersByTime(4999)
    expect(poll).toHaveBeenCalledTimes(3)
    vi.advanceTimersByTime(1)
    expect(poll).toHaveBeenCalledTimes(4)
    live.stop()
  })

  it('speeds up again when the stream drops', () => {
    const { source, poll, live } = setup()
    source.emit('open')
    source.emit('error')
    expect(live.connected()).toBe(false)
    vi.advanceTimersByTime(2000)
    expect(poll).toHaveBeenCalledTimes(2)
    live.stop()
  })

  it('dispatches events, runs open on each connection, and stops', () => {
    const { source, poll, published, opened, live } = setup()
    source.emit('published')
    source.emit('open')
    source.emit('open')
    expect(published).toHaveBeenCalledTimes(1)
    expect(opened).toHaveBeenCalledTimes(2)
    live.stop()
    expect(source.closed).toBe(true)
    vi.advanceTimersByTime(10_000)
    expect(poll).not.toHaveBeenCalled()
  })

  it('does not poll a hidden page, and polls when it is shown', () => {
    const { poll, live } = setup()
    const state = vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('hidden')
    vi.advanceTimersByTime(3000)
    expect(poll).not.toHaveBeenCalled()
    state.mockReturnValue('visible')
    document.dispatchEvent(new Event('visibilitychange'))
    expect(poll).toHaveBeenCalledTimes(1)
    live.stop()
  })
})
