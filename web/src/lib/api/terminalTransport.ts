// Terminal input queues and reconnecting output streams.
import type { Engine } from './engine.ts'
import type { TermEvent } from './terminal.ts'
import type { createTransport } from './transport.ts'
import { isRecord, isString } from './guards.ts'

// TermOutput is a terminal event as termAttach hands it on. error is set when
// the attach ended for good because the terminal is gone or the call was refused.
export interface TermOutput extends TermEvent {
  error?: string
}

export function createTerminalTransport(
  app: Pick<Engine, 'TermWrite' | 'TermAttach'>,
  authenticatedFetch: ReturnType<typeof createTransport>['authenticatedFetch'],
) {
  // termWrite sends terminal input in order: one write per terminal at a
  // time, and keys typed while a write is in flight join the next write. On
  // the web each write is its own request, and two in flight can arrive at
  // the host swapped. It resolves when the bytes reached the host.
  interface TermWrite {
    base64: string
    resolve: () => void
    reject: (reason: unknown) => void
  }
  interface TermQueue {
    pending: TermWrite[]
    busy: boolean
  }
  const termQueues = new Map<string, TermQueue>()
  function termWrite(id: string, base64: string): Promise<void> {
    let q = termQueues.get(id)
    if (!q) {
      q = { pending: [], busy: false }
      termQueues.set(id, q)
    }
    const queue = q
    const done = new Promise<void>((resolve, reject) => queue.pending.push({ base64, resolve, reject }))
    if (!queue.busy) drainTerm(id, queue)
    return done
  }

  async function drainTerm(id: string, q: TermQueue) {
    q.busy = true
    while (q.pending.length) {
      const batch = q.pending.splice(0)
      try {
        await app.TermWrite(id, btoa(batch.map((b) => atob(b.base64)).join('')))
        for (const b of batch) b.resolve()
      } catch (e) {
        for (const b of batch) b.reject(e)
      }
    }
    q.busy = false
    termQueues.delete(id)
  }

  // termAttach sends {id,data,start,offset,reset?,exited,exitCode} to output.
  // data is base64 raw PTY bytes. Return the detach function on view unmount;
  // detaching never closes the shell. Byte offsets prevent duplicate output.
  async function termAttach(id: string, output: (ev: TermOutput) => void, after = 0): Promise<() => void> {
    let stopped = false
    let cursor = after
    let controller: AbortController | null = null
    let retryTimer: ReturnType<typeof setTimeout> | undefined
    let wakeRetry = () => {}
    const sleep = (ms: number) => new Promise<void>((resolve) => { wakeRetry = resolve; retryTimer = setTimeout(resolve, ms) })
    const accept = (ev: TermOutput) => {
      if (stopped || ev.id !== id) return
      // An out-of-order event may overlap the initial scrollback.
      if (!ev.reset && ev.offset < cursor) return
      if (!ev.reset && ev.start < cursor) {
        const raw = atob(ev.data || '')
        ev = { ...ev, data: btoa(raw.slice(cursor - ev.start)), start: cursor }
      }
      if (ev.data || ev.exited || ev.reset) output(ev)
      cursor = ev.offset
      if (ev.exited) stopped = true
    }
    const detach = () => {
      stopped = true
      controller?.abort()
      clearTimeout(retryTimer)
      wakeRetry()
    }

    // A stream held by a proxy falls back to bounded snapshot calls. The
    // terminal keeps running while either transport reconnects.
    let polling = false
    const run = async () => {
      let wait = 500
      while (!stopped) {
        let helloTimer: ReturnType<typeof setTimeout> | undefined
        try {
          if (polling) {
            accept(await app.TermAttach(id, cursor))
            if (!stopped) await sleep(250)
            continue
          }
          const attempt = new AbortController()
          controller = attempt
          helloTimer = setTimeout(() => { polling = true; attempt.abort() }, 6000)
          const response = await authenticatedFetch('/api/terminal/attach', {
            method: 'POST', credentials: 'same-origin', signal: attempt.signal,
            headers: { 'Content-Type': 'application/json', 'X-Dyna': '1' },
            body: JSON.stringify({ id, after: cursor }),
          })
          if (!response.ok) {
            const error: unknown = await response.json().catch(() => null)
            const reason = isRecord(error) && isString(error.error) ? error.error : undefined
            // Missing terminals and auth refusal are final, rather than a
            // network interruption that should retry forever.
            if (response.status >= 400 && response.status < 500) {
              output({ id, data: '', start: cursor, offset: cursor, exited: true, exitCode: -1, error: reason })
              stopped = true
              return
            }
            throw new Error(reason || `HTTP ${response.status}`)
          }
          const reader = response.body?.getReader()
          if (!reader) throw new Error('the terminal stream has no body')
          const decoder = new TextDecoder()
          let buffer = ''
          try {
            while (!stopped) {
              const { value, done } = await reader.read()
              if (done) break
              buffer += decoder.decode(value, { stream: true })
              let end: number
              while ((end = buffer.indexOf('\n\n')) !== -1) {
                const frame = buffer.slice(0, end)
                buffer = buffer.slice(end + 2)
                if (frame.includes('event: hello')) { clearTimeout(helloTimer); wait = 500 }
                else if (!frame.includes('event: ping')) {
                  const line = frame.split('\n').find((s) => s.startsWith('data: '))
                  if (line) accept(JSON.parse(line.slice(6)))
                }
              }
            }
          } finally { await reader.cancel().catch(() => {}) }
        } catch (error) {
          // Snapshot calls also need backoff when a polling connection fails.
          if (String(error).includes('no such terminal')) {
            output({ id, data: '', start: cursor, offset: cursor, exited: true, exitCode: -1, error: String(error) })
            stopped = true
          } else if (!stopped && polling) await sleep(1000)
        } finally { clearTimeout(helloTimer) }
        if (!stopped && !polling) { await sleep(wait); wait = Math.min(wait * 2, 5000) }
      }
    }
    run()
    return detach
  }

  return { termWrite, termAttach }
}
