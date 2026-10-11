// Live updates from the server: server-sent events push publishes, drafts and
// workspace writes at once; polling is the fallback for a stream a proxy cuts
// off. While the stream is connected, polling slows down to what only polling
// reports (the execution status); when the stream drops it speeds up again.
// Nothing is polled while the page is hidden; showing it polls at once.

/** The part of EventSource used here (a fake one in tests). */
export interface EventSourceLike {
  addEventListener(type: string, listener: () => void): void
  close(): void
}

export interface LiveUpdatesOptions {
  url: string
  /** Called on every tick and when the page is shown again. */
  poll: () => Promise<void>
  /** Server-sent event handlers by event type; 'open' runs on each (re)connection. */
  events: Record<string, () => void>
  /** Poll interval without the stream, and with it. */
  interval?: number
  connectedInterval?: number
  createSource?: (url: string) => EventSourceLike
}

export interface LiveUpdates {
  start(): void
  stop(): void
  /** Whether the stream is connected (polling slowed down). */
  connected(): boolean
}

export function liveUpdates(opts: LiveUpdatesOptions): LiveUpdates {
  const interval = opts.interval ?? 10_000
  const connectedInterval = opts.connectedInterval ?? 60_000
  const createSource = opts.createSource ?? ((url: string) => new EventSource(url))
  let source: EventSourceLike | undefined
  let timer: ReturnType<typeof setTimeout> | undefined
  let isConnected = false
  let running = false

  const visible = () => typeof document === 'undefined' || document.visibilityState === 'visible'
  const poll = () => void opts.poll().catch(() => undefined)

  function schedule() {
    clearTimeout(timer)
    if (!running) return
    timer = setTimeout(() => {
      if (visible()) poll()
      schedule()
    }, isConnected ? connectedInterval : interval)
  }
  function setConnected(value: boolean) {
    if (isConnected === value) return
    isConnected = value
    schedule()
  }
  function onVisibility() {
    if (visible()) poll()
  }

  return {
    start() {
      if (running) return
      running = true
      source = createSource(opts.url)
      for (const [type, handler] of Object.entries(opts.events)) {
        if (type !== 'open') source.addEventListener(type, handler)
      }
      source.addEventListener('open', () => {
        setConnected(true)
        opts.events.open?.()
      })
      // The browser reconnects by itself; until it does, polling covers.
      source.addEventListener('error', () => setConnected(false))
      if (typeof document !== 'undefined') document.addEventListener('visibilitychange', onVisibility)
      schedule()
    },
    stop() {
      running = false
      clearTimeout(timer)
      source?.close()
      source = undefined
      isConnected = false
      if (typeof document !== 'undefined') document.removeEventListener('visibilitychange', onVisibility)
    },
    connected: () => isConnected,
  }
}
