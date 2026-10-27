<script lang="ts">
  // Terminals for a room (its repo folder) and its sessions, in a bar along
  // the bottom of the main area. A chip per shell; a click opens the dock
  // above the bar, and the bar becomes its tab strip. Shells live on the
  // host, so hiding the dock, switching rooms or reloading never stops one:
  // the app lists them again and reattaches. On a phone there is no dock; a
  // one-line bar opens a list, and a shell opens as a full-screen sheet with a
  // key row for Esc, Tab, Ctrl and the arrows, which a soft keyboard lacks.
  // Height, maximize and the open shell are view state per device (prefs.ts).
  import { onMount, untrack, tick } from 'svelte'
  import { api } from '../../api.ts'
  import { base } from '../files/repo.ts'
  import TermPane from './TermPane.svelte'
  import Confirm from '../ui/Confirm.svelte'
  import FloatWindow from '../ui/FloatWindow.svelte'
  import PhoneKeys from '../ui/PhoneKeys.svelte'
  import { errorText } from '../errorText.ts'
  import { loadDock, saveDock } from '../ui/prefs.ts'
  import { dialog } from '../ui/dialog.ts'
  import { dockReduce, clampHeight, clampRect, type DockAction, type Win } from './termDock.ts'
  import type { TermInfo } from '../api/terminal.ts'
  import type { Question } from '../api/room.ts'
  import RequestReview from './RequestReview.svelte'
  import RequestResult from './RequestResult.svelte'
  import { autoVerdict, followUpText, requestActions, type SentReport } from './termRequest.ts'

  // session set: only that session's shells show, and + opens one in its
  // folder (a session view). owner names the room's folder in the chips; names
  // maps session ids to names. request counts up when the app wants the dock
  // open (the old terminal link, the palette).
  // requests are the terminal requests that wait for the owner. review carries
  // a one-shot request ID the owner chose to review; later dock opens leave the
  // current tab alone.
  // noRepo: the room has no repository, so a room terminal has no folder.
  let { roomId, session = '', owner = '', narrow = false, names = {}, noRepo = false, request = 0, review = { request: 0, id: '', run: false }, requests = [] }: { roomId: string; session?: string; owner?: string; narrow?: boolean; names?: Record<string, string>; noRepo?: boolean; request?: number; review?: { request: number; id: string; run: boolean }; requests?: Question[] } = $props()
  let ref = $derived(session || roomId)
  let roomless = $derived(noRepo && ref === roomId)

  let dock = $state(untrack(() => loadDock(roomId)))
  let all = $state<TermInfo[] | null>(null)
  let error = $state('')
  let opening = $state(false)
  let exited = $state<Record<string, number>>({}) // id -> exit code
  let closing = $state<TermInfo | null>(null) // the terminal waiting on the close confirm
  let panes = $state<Record<string, { key(name: string): void; send(text: string): void; focus(): void } | undefined>>({})
  let ctrl = $state(false)
  let tabsEl = $state<HTMLElement>()
  let rootEl = $state<HTMLElement>()
  let avail = $state(800) // height of the main area
  let availW = $state(1000)
  let dragging = $state(false)
  let listing = $state(false) // phone: the list of terminals
  let sheet = $state(false) // phone: the open terminal

  const act = (a: DockAction) => (dock = dockReduce(dock, a))

  // A room lists every shell in the room, its sessions' too; a session view
  // lists that session's.
  let list = $derived((all ?? []).filter((t) => (ref === roomId ? t.roomId === roomId || t.ref === roomId : t.sessionId === ref || t.ref === ref)))
  // The saved tab, or the newest shell when it is gone or this ref has none yet.
  // Shells popped out into windows are not in the dock; the bar lists both.
  const winOf = (id: string): Win | undefined => dock.wins.find((w) => w.id === id)
  let docked = $derived(list.filter((t) => !winOf(t.id)))
  let windowed = $derived(list.filter((t) => winOf(t.id)))
  let area = $derived({ w: availW, h: avail })
  let active = $derived(docked.find((t) => t.id === dock.active[ref])?.id ?? docked.at(-1)?.id ?? '')
  let current = $derived(docked.find((t) => t.id === active))
  // The phone has no windows, so its sheet shows the chosen shell wherever it sits.
  let sheetTerm = $derived(list.find((t) => t.id === dock.active[ref]) ?? list.at(-1))
  let height = $derived(clampHeight(dock.height, avail))

  // Not while dragging: one write when the handle is let go.
  $effect(() => { const d = dock; if (!dragging) saveDock(roomId, d) })

  $effect(() => {
    const el = rootEl?.parentElement
    if (!el) return
    const measure = () => { avail = el.clientHeight; availW = el.clientWidth }
    measure()
    const observer = new ResizeObserver(measure)
    observer.observe(el)
    return () => observer.disconnect()
  })

  let seen = untrack(() => request)
  $effect(() => {
    if (request === seen) return
    seen = request
    if (review.request === request && review.id) {
      if (review.run) startRequest(review.id)
      else showReview(review.id)
    }
    else show()
  })

  function revealTab() {
    const tab = tabsEl?.querySelector<HTMLElement>('[aria-selected="true"]')?.closest<HTMLElement>('.tab')
    if (!tabsEl || !tab) return
    const strip = tabsEl.getBoundingClientRect()
    const item = tab.getBoundingClientRect()
    if (item.left < strip.left) tabsEl.scrollLeft += item.left - strip.left
    else if (item.right > strip.right) tabsEl.scrollLeft += item.right - strip.right
  }

  $effect(() => {
    active
    list
    if (!tabsEl) return
    let live = true
    tick().then(() => { if (live) revealTab() })
    const observer = new ResizeObserver(revealTab)
    observer.observe(tabsEl)
    return () => { live = false; observer.disconnect() }
  })

  // load lists the shells. A background refresh (quiet) never shows an
  // error: a lost connection already shows elsewhere and passes.
  async function load(quiet = false) {
    try {
      all = (await api.terms()) ?? []
      error = ''
      // An empty answer (a restarting engine) proves nothing: keep the windows.
      untrack(() => { if (all?.length) act({ type: 'prune', keep: all.map((t) => t.id) }) })
    } catch (e) {
      if (!quiet) error = errorText(e)
    }
  }
  const refresh = () => load(true)

  // open starts a shell in the folder; the size is a guess until the pane
  // measures itself and resizes.
  async function open() {
    // Every path (button, shortcut, restart) stops here for a room without a
    // repository: the host would only refuse.
    if (opening || roomless) return
    opening = true
    try {
      const t = await api.termOpen(ref, narrow ? 48 : 120, narrow ? 24 : 32)
      all = [...(all ?? []), t]
      act({ type: 'tab', ref, id: t.id })
      if (narrow) { listing = false; sheet = true } else act({ type: 'show' })
      load()
    } catch (e) {
      error = errorText(e)
    } finally {
      opening = false
    }
  }

  // The request being reviewed in the dock, by ID. A request the owner ran is
  // a terminal tab with a banner; one not yet run has its own tab.
  let viewing = $state('')
  // reported: requests the owner just ended, hidden at once while the engine's list catches up.
  let reported = $state(new Set<string>())
  let waiting = $derived(requests.filter((q) => q.state === 'open' && !reported.has(q.id)))
  // ran: request ID to the terminal the owner just started, until the engine's list shows it.
  let ran = $state<Record<string, string>>({})
  const termOf = (q: Question): string | undefined => q.terminal?.termId || ran[q.id]
  let unstarted = $derived(waiting.filter((q) => !termOf(q)))
  let reviewQ = $derived(unstarted.find((q) => q.id === viewing))
  const requestOf = (termId: string): Question | undefined => waiting.find((q) => termOf(q) === termId)

  const requestApi = (q: Question) => requestActions({
    run: () => api.termRequestRun(roomId, q.id, null, narrow ? 48 : 120, narrow ? 24 : 32),
    preview: () => api.termRequestPreview(roomId, q.id),
    report: (verdict, note, share) => api.termRequestReport(roomId, q.id, verdict, note, share),
  })

  // run starts the command in a new terminal and shows it.
  async function runRequest(q: Question) {
    const t = await requestApi(q).run()
    ran = { ...ran, [q.id]: t.id }
    all = [...(all ?? []), t]
    viewing = ''
    pick(t)
    load()
  }

  // startRequest runs a request exactly as filed, from the chat card's Run. A
  // failure leaves the review open, where the error shows on its Run button.
  async function startRequest(id: string) {
    const q = waiting.find((r) => r.id === id)
    if (!q || termOf(q)) { showReview(id); return }
    try { await runRequest(q) } catch (e) { error = errorText(e); showReview(id) }
  }

  async function declineRequest(q: Question, reason: string) {
    await requestApi(q).decline(reason)
    reported = new Set([...reported, q.id])
    viewing = ''
    if (narrow) sheet = false
  }

  // sent: terminal ID to the result sent to its filer when the command exited.
  let sent = $state<Record<string, SentReport>>({})
  let sending = new Set<string>()

  // noteExit shows the result the engine sends the moment the command exits:
  // exit code 0 is done, anything else failed. The engine reports, so a closed
  // tab loses nothing; this only keeps the line for the follow-ups.
  function noteExit(q: Question, termId: string, code: number) {
    if (sending.has(q.id)) return
    sending.add(q.id)
    sent = { ...sent, [termId]: { requestId: q.id, askerName: q.askerName, verdict: autoVerdict(code), code } }
    reported = new Set([...reported, q.id])
  }

  $effect(() => {
    for (const q of waiting) {
      const termId = termOf(q)
      const t = termId ? list.find((x) => x.id === termId) : undefined
      const code = termId ? exited[termId] ?? (t?.exited ? t.exitCode : undefined) : undefined
      if (termId && code !== undefined) noteExit(q, termId, code)
    }
  })

  // followUp tells the filer more about a result already sent.
  async function followUp(s: SentReport, note: string, lines: string | null) {
    await api.send(roomId, followUpText(s, note, lines))
  }

  // showReview opens the review of one request, or its terminal once it ran.
  async function showReview(id: string) {
    const q = waiting.find((r) => r.id === id)
    if (!q) { show(); return }
    const termId = termOf(q)
    if (termId) {
      await load()
      const t = list.find((x) => x.id === termId)
      if (t) { pick(t); return }
    }
    viewing = id
    if (narrow) sheet = true
    else act({ type: 'show' })
  }

  // The gold chip opens the first request that waits.
  const openFirstRequest = () => { const q = waiting[0]; if (q) showReview(q.id) }

  // show opens the dock; with no shell to show it starts one.
  async function show() {
    if (narrow) {
      // A phone has no dock: one shell opens its sheet, several open the list.
      if (all === null) await load()
      const only = list.length === 1 ? list[0] : undefined
      if (only) pick(only)
      else listing = true
      return
    }
    act({ type: 'show' })
    if (all === null) await load()
    if (!list.length && !roomless) open()
  }

  const toggle = () => (dock.open ? act({ type: 'hide' }) : show())

  // A chip shows its shell: in the dock, or by raising its window.
  function pick(t: TermInfo) {
    viewing = ''
    if (narrow) { act({ type: 'tab', ref, id: t.id }); listing = false; sheet = true }
    else if (winOf(t.id)) act({ type: 'raise', id: t.id })
    else { act({ type: 'tab', ref, id: t.id }); act({ type: 'show' }) }
  }

  function popOut(t: TermInfo) {
    act({ type: 'pop', id: t.id, area })
    if (!docked.length) act({ type: 'hide' })
  }

  function dockIn(t: TermInfo) {
    act({ type: 'dockIn', id: t.id })
    act({ type: 'tab', ref, id: t.id })
    act({ type: 'show' })
  }

  async function close(t: TermInfo) {
    closing = null
    try {
      await api.termClose(t.id)
    } catch (e) {
      error = errorText(e)
    }
    all = (all ?? []).filter((x) => x.id !== t.id)
    act({ type: 'dockIn', id: t.id })
    if (!docked.length) { act({ type: 'hide' }); sheet = false }
    load()
  }

  function askClose(t: TermInfo) {
    if (t.alive === false || exited[t.id] !== undefined) close(t)
    else closing = t
  }

  function exitText(t: TermInfo) {
    const code = exited[t.id] ?? (t.exited ? t.exitCode : undefined)
    if (code === -1) return 'This shell is gone. The host restarted, or it was closed elsewhere.'
    return `The shell exited${code !== undefined ? ` with code ${code}` : ''}.`
  }

  // The chip: the owner of the folder (the session, or the room's repo), then
  // the shell.
  function label(t: TermInfo) {
    // A request's shell keeps the request's name while the request lasts.
    const asked = requestOf(t.id)
    if (asked) return `request · ${asked.askerName}`
    const who = (t.sessionId && names[t.sessionId]) || owner
    const shell = t.title || t.shell?.split('/').pop() || 'shell'
    const name = who ? `${who} · ${shell}` : shell
    const same = list.filter((x) => x.sessionId === t.sessionId)
    return same.length > 1 ? `${name} ${same.indexOf(t) + 1}` : name
  }

  // Ctrl+` shows or hides the dock from anywhere. Ctrl+Shift+T opens a tab,
  // Ctrl+Shift+W closes it, and Ctrl+PageUp or Ctrl+PageDown switch, as in
  // most terminal apps, while the focus is in the dock.
  function shortcut(e: KeyboardEvent) {
    if (!e.ctrlKey || e.altKey || e.metaKey || narrow) return
    if (e.key === '`' && !e.shiftKey) {
      toggle()
      e.preventDefault()
      e.stopPropagation()
      return
    }
    // From the dock, or from a window (then the shortcut means that window's shell).
    const from = e.target instanceof Element ? e.target.closest<HTMLElement>('.termdock, .win') : null
    const inWin = from?.dataset.term
    if (!from || !(inWin || dock.open)) return
    const subject = inWin ? list.find((t) => t.id === inWin) : current
    const k = e.key.toLowerCase()
    const i = docked.findIndex((t) => t.id === active)
    const go = (n: number) => { const t = docked[n]; if (t) act({ type: 'tab', ref, id: t.id }) }
    if (e.shiftKey && k === 't') open()
    else if (e.shiftKey && k === 'w' && subject) askClose(subject)
    else if (k === 'pagedown' && !inWin && docked.length) go((i + 1) % docked.length)
    else if (k === 'pageup' && !inWin && docked.length) go((i - 1 + docked.length) % docked.length)
    else return
    e.preventDefault()
  }

  function tabKey(e: KeyboardEvent) {
    const i = list.findIndex((t) => t.id === active)
    const next = e.key === 'Home' ? 0 : e.key === 'End' ? list.length - 1
      : e.key === 'ArrowRight' ? (i + 1) % list.length
      : e.key === 'ArrowLeft' ? (i - 1 + list.length) % list.length : -1
    const t = list[next]
    if (t) {
      e.preventDefault()
      act({ type: 'tab', ref, id: t.id })
      tick().then(() => {
        tabsEl?.querySelector<HTMLElement>('[aria-selected="true"]')?.focus({ preventScroll: true })
        revealTab()
      })
    } else if (e.key === 'Delete' && current) askClose(current)
  }

  // track follows one pointer drag on the element that got the press.
  function track(e: PointerEvent, onmove: (m: PointerEvent) => void) {
    const el = e.currentTarget
    if (!(el instanceof HTMLElement)) return
    el.setPointerCapture(e.pointerId)
    dragging = true
    const up = () => { dragging = false; el.removeEventListener('pointermove', onmove); el.removeEventListener('pointerup', up); el.removeEventListener('pointercancel', up) }
    el.addEventListener('pointermove', onmove)
    el.addEventListener('pointerup', up)
    el.addEventListener('pointercancel', up)
  }

  // The top edge resizes the dock; the arrow keys do the same in steps.
  function drag(e: PointerEvent) {
    if (!rootEl) return
    const bottom = rootEl.getBoundingClientRect().bottom
    const strip = rootEl.querySelector<HTMLElement>('.strip')?.offsetHeight ?? 0
    track(e, (m) => act({ type: 'resize', height: bottom - strip - m.clientY, avail }))
  }

  function gripKey(e: KeyboardEvent) {
    const step = e.key === 'ArrowUp' ? 24 : e.key === 'ArrowDown' ? -24 : 0
    if (!step) return
    e.preventDefault()
    act({ type: 'resize', height: height + step, avail })
  }

  // Swiping the sheet's title down returns to the room.
  let swipeFrom = 0
  const swipeStart = (e: TouchEvent) => (swipeFrom = e.touches[0]?.clientY ?? 0)
  const swipeEnd = (e: TouchEvent) => { if ((e.changedTouches[0]?.clientY ?? 0) - swipeFrom > 70) sheet = false }

  onMount(() => {
    // A dock left open with no shell (a reload, the old terminal link) starts one.
    load().then(() => { if (dock.open && !list.length && !roomless && !narrow) open() })
    // Shells can end or vanish elsewhere (another device, a host restart);
    // keep the tabs current.
    const t = setInterval(refresh, 5000)
    window.addEventListener('focus', refresh)
    return () => {
      clearInterval(t)
      window.removeEventListener('focus', refresh)
    }
  })
</script>

<svelte:window onkeydowncapture={shortcut} />

{#snippet reviewPane(q: Question)}
  <RequestReview {q} onrun={() => runRequest(q)} ondecline={(reason) => declineRequest(q, reason)} />
{/snippet}

{#snippet body(t: TermInfo | undefined)}
  {#if error}<p class="err">{error}</p>{/if}
  {#if t}
    {@const req = requestOf(t.id)}
    <!-- A request's shell is reported on, not restarted: its banner replaces this one. -->
    {#if !req && (exited[t.id] !== undefined || t.alive === false)}
      <div class="exit">
        <span>{exitText(t)}</span>
        <button onclick={() => close(t)}>Close tab</button>
        {#if !roomless}<button class="primary" onclick={async () => { await open(); close(t) }}>Start a new shell</button>{/if}
      </div>
    {/if}
    {#if req}
      <RequestResult name={req.askerName} sent={undefined} onpreview={() => api.termRequestPreview(roomId, req.id)} onfollowup={async () => {}} />
    {:else}
      {@const s = sent[t.id]}
      {#if s}
      <RequestResult name={s.askerName} sent={s} onpreview={() => api.termRequestPreview(roomId, s.requestId)} onfollowup={(note, lines) => followUp(s, note, lines)} />
      {/if}
    {/if}
    {#key t.id}
      <TermPane bind:this={panes[t.id]} bind:ctrl id={t.id} {narrow} onexit={(code) => (exited = { ...exited, [t.id]: code })} />
    {/key}
    {#if narrow}
      <PhoneKeys {ctrl} onkey={(k: string) => { if (k === 'ctrl') { ctrl = !ctrl; panes[t.id]?.focus() } else panes[t.id]?.key(k) }} />
    {/if}
  {:else if all === null}
    <p class="quiet">Starting a shell…</p>
  {:else if roomless}
    <p class="quiet">This room has no repository, so a room terminal has no folder. Open one from a session.</p>
  {:else}
    <p class="quiet">No terminal open. <button class="open" onclick={open} disabled={opening}>Open a terminal</button></p>
  {/if}
{/snippet}

{#if narrow}
  <div class="phone" bind:this={rootEl}>
    <button class="pbar" onclick={() => (listing = true)} aria-haspopup="dialog">
      <svg viewBox="0 0 16 16" width="14" height="14" aria-hidden="true"><path d="M2.5 3.5h11a1 1 0 0 1 1 1v7a1 1 0 0 1-1 1h-11a1 1 0 0 1-1-1v-7a1 1 0 0 1 1-1ZM4.5 6.5l2 1.5-2 1.5M8 10h3" fill="none" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round" /></svg>
      <span>{list.length === 1 ? '1 terminal' : list.length ? `${list.length} terminals` : 'Terminal'}</span>
      {#if waiting.length}<span class="reqchip">{waiting.length === 1 ? '1 request' : `${waiting.length} requests`}</span>{/if}
      {#if !list.length}<svg viewBox="0 0 16 16" width="12" height="12" aria-hidden="true"><path d="M8 3v10M3 8h10" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" /></svg>{/if}
    </button>
  </div>
  {#if listing}
    <!-- The backdrop is dismissed by Escape and the dialog action too; the tap is a touch convenience. -->
    <div class="scrim" role="presentation" onclick={(e) => { if (e.target === e.currentTarget) listing = false }}>
    <div class="list" role="dialog" aria-modal="true" aria-label="Terminals" use:dialog={{ close: () => (listing = false) }}>
      <h2>Terminals</h2>
      {#each unstarted as q (q.id)}
        <div class="row req"><button class="rname" onclick={() => { listing = false; showReview(q.id) }}><span class="dot" aria-hidden="true"></span><span class="tl">request · {q.askerName}</span><span class="rcwd">waiting for you</span></button></div>
      {/each}
      {#each list as t (t.id)}
        <div class="row" class:dead={t.alive === false || exited[t.id] !== undefined} class:req={!!requestOf(t.id)}>
          <button class="rname" onclick={() => pick(t)}><span class="dot" aria-hidden="true"></span><span class="tl">{label(t)}</span><span class="rcwd mono">{base(t.cwd)}</span></button>
          <button class="tx" onclick={() => askClose(t)} aria-label="Close {label(t)}"><svg viewBox="0 0 16 16" width="12" height="12" aria-hidden="true"><path d="M4 4l8 8M12 4l-8 8" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" /></svg></button>
        </div>
      {/each}
      {#if error}<p class="err">{error}</p>{/if}
      {#if roomless}<p class="quiet">This room has no repository, so a room terminal has no folder. Open one from a session.</p>
      {:else}<button class="row new" onclick={open} disabled={opening}>+ New terminal</button>{/if}
    </div>
    </div>
  {/if}
  {#if sheet && (reviewQ || sheetTerm)}
    <div class="sheetback">
    <div class="sheet" role="dialog" aria-modal="true" aria-label={reviewQ ? `Request from ${reviewQ.askerName}` : `Terminal ${sheetTerm ? label(sheetTerm) : ''}`} use:dialog={{ close: () => (sheet = false) }}>
      <header role="presentation" ontouchstart={swipeStart} ontouchend={swipeEnd}>
        <span class="grab" aria-hidden="true"></span>
        <span class="tl">{reviewQ ? `request · ${reviewQ.askerName}` : sheetTerm ? label(sheetTerm) : ''}</span>
        <button class="tx" onclick={() => (sheet = false)} aria-label="Back to the room"><svg viewBox="0 0 16 16" width="14" height="14" aria-hidden="true"><path d="M4 4l8 8M12 4l-8 8" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" /></svg></button>
      </header>
      {#if reviewQ}
        {@render reviewPane(reviewQ)}
      {:else}
        {@render body(sheetTerm)}
      {/if}
    </div>
    </div>
  {/if}
{:else}
  <section class="termdock" class:maxed={dock.open && dock.maxed} bind:this={rootEl}>
    {#if dock.open}
      <!-- svelte-ignore a11y_no_noninteractive_tabindex, a11y_no_noninteractive_element_interactions -->
      <div class="grip" role="separator" aria-orientation="horizontal" aria-label="Resize the terminal" aria-valuenow={height} aria-valuemin={120} tabindex="0" onpointerdown={drag} onkeydown={gripKey}></div>
    {/if}
    <div class="strip">
      {#if !list.length && !unstarted.length && !dock.open}
        <button class="bare" onclick={show} disabled={roomless} title={roomless ? 'This room has no repository, so a room terminal has no folder' : 'Open a terminal (Ctrl+`)'}>
          <svg viewBox="0 0 16 16" width="12" height="12" aria-hidden="true"><path d="M8 3v10M3 8h10" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" /></svg>
          Terminal
        </button>
      {:else}
        <!-- svelte-ignore a11y_interactive_supports_focus -->
        <div class="tablist" role="tablist" aria-label="Terminals" bind:this={tabsEl} onkeydown={tabKey}>
          {#each unstarted as q (q.id)}
            <div class="tab req" class:on={dock.open && viewing === q.id}>
              <button role="tab" aria-selected={dock.open && viewing === q.id} tabindex="-1" class="tname" onclick={() => showReview(q.id)} title={q.terminal?.reason}>
                <span class="dot" aria-hidden="true"></span>
                <span class="tl">request · {q.askerName}</span>
              </button>
            </div>
          {/each}
          {#each list as t (t.id)}
            {@const w = winOf(t.id)}
            <div class="tab" class:on={dock.open && !reviewQ && t.id === active} class:dead={t.alive === false || exited[t.id] !== undefined} class:outside={!!w} class:req={!!requestOf(t.id)}>
              <button role="tab" aria-selected={dock.open && !reviewQ && t.id === active} tabindex={t.id === active ? 0 : -1} class="tname" onclick={() => pick(t)} title={w ? `${t.cwd} (in a window)` : t.cwd}>
                <span class="dot" aria-hidden="true"></span>
                <span class="tl">{label(t)}</span>
                {#if w}<svg class="inwin" viewBox="0 0 16 16" width="10" height="10" aria-hidden="true"><path d="M6.5 3.5h6v6M12.5 3.5l-7 7M3.5 6.5v6h6" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" /></svg>{/if}
              </button>
              <button class="tx" onclick={() => askClose(t)} aria-label="Close {label(t)}" title="Close (Ctrl+Shift+W)">
                <svg viewBox="0 0 16 16" width="10" height="10" aria-hidden="true"><path d="M4 4l8 8M12 4l-8 8" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" /></svg>
              </button>
            </div>
          {/each}
        </div>
        {#if !roomless}<button class="add" onclick={open} disabled={opening} aria-label="New terminal" title="New terminal (Ctrl+Shift+T)">
          <svg viewBox="0 0 16 16" width="12" height="12" aria-hidden="true"><path d="M8 3v10M3 8h10" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" /></svg>
        </button>{/if}
      {/if}
      <div class="slot">
        {#if waiting.length}<button class="reqchip" onclick={openFirstRequest} title="A session asks you to run a command">{waiting.length === 1 ? '1 request' : `${waiting.length} requests`}</button>{/if}
        {#if dock.open}
          {#if current?.cwd}<span class="cwd mono" title={current.cwd}>{base(current.cwd)}</span>{/if}
          {#if current}<button class="ib" onclick={() => popOut(current)} aria-label="Pop out as a window" title="Pop out as a window">
            <svg viewBox="0 0 16 16" width="12" height="12" aria-hidden="true"><path d="M6.5 3.5h6v6M12.5 3.5l-7 7M3.5 6.5v6h6" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" /></svg>
          </button>{/if}
          <button class="ib" onclick={() => act({ type: 'maximize' })} aria-label={dock.maxed ? 'Restore the dock' : 'Maximize the dock'} aria-pressed={dock.maxed} title={dock.maxed ? 'Restore' : 'Maximize'}>
            <svg viewBox="0 0 16 16" width="12" height="12" aria-hidden="true">{#if dock.maxed}<path d="M5.5 2.5v3h-3M10.5 2.5v3h3M5.5 13.5v-3h-3M10.5 13.5v-3h3" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" />{:else}<rect x="3" y="3" width="10" height="10" rx="1.5" fill="none" stroke="currentColor" stroke-width="1.5" />{/if}</svg>
          </button>
          <button class="ib" onclick={() => act({ type: 'hide' })} aria-label="Hide the dock" title="Hide (Ctrl+`)">
            <svg viewBox="0 0 16 16" width="12" height="12" aria-hidden="true"><path d="M3.5 6l4.5 4.5L12.5 6" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" /></svg>
          </button>
        {/if}
      </div>
    </div>
    {#if dock.open}
      <div class="dockbody" style:height={dock.maxed ? undefined : `${height}px`}>
        {#if reviewQ}
          {@render reviewPane(reviewQ)}
        {:else}
          {@render body(current)}
        {/if}
      </div>
    {/if}
  </section>
  <div class="wins">
    {#each windowed as t (t.id)}
      {@const w = winOf(t.id)}
      {#if w && !w.min}
        {@const r = clampRect(w, area)}
        <FloatWindow
          rect={r} {area} label="Terminal {label(t)}" z={w.z} term={t.id}
          onmove={(x, y, a) => act({ type: 'move', id: t.id, x, y, area: a })}
          onsize={(nw, nh, a) => act({ type: 'size', id: t.id, w: nw, h: nh, area: a })}
          onraise={() => act({ type: 'raise', id: t.id })}
          ondrag={(on) => (dragging = on)}
        >
          {#snippet title()}
            <span class="dot" aria-hidden="true"></span>
            <span class="tl" title={t.cwd}>{label(t)}</span>
          {/snippet}
          {#snippet buttons()}
            <button class="ib" onclick={() => dockIn(t)} aria-label="Dock {label(t)} again" title="Dock again">
              <svg viewBox="0 0 16 16" width="12" height="12" aria-hidden="true"><path d="M9.5 12.5h-6v-6M3.5 12.5l7-7M12.5 9.5v-6h-6" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" /></svg>
            </button>
            <button class="ib" onclick={() => act({ type: 'minimize', id: t.id })} aria-label="Minimize {label(t)}" title="Minimize to the bar">
              <svg viewBox="0 0 16 16" width="12" height="12" aria-hidden="true"><path d="M4 11.5h8" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" /></svg>
            </button>
            <button class="ib" onclick={() => askClose(t)} aria-label="Close {label(t)}" title="Close the terminal">
              <svg viewBox="0 0 16 16" width="12" height="12" aria-hidden="true"><path d="M4 4l8 8M12 4l-8 8" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" /></svg>
            </button>
          {/snippet}
          {@render body(t)}
        </FloatWindow>
      {/if}
    {/each}
  </div>
{/if}

{#if closing}
  {@const target = closing}
  <Confirm title="Close this terminal?" lines={['Its shell stops, and so does any command running in it.']} action="Close terminal" onconfirm={() => close(target)} oncancel={() => (closing = null)} />
{/if}

<style>
  .termdock { flex: none; display: flex; flex-direction: column; min-width: 0; background: #19181a; border-top: 1px solid var(--line-soft); }
  .termdock.maxed { position: absolute; inset: 0; z-index: 20; }
  .grip { transition: background-color var(--motion-fast) var(--ease); height: 5px; margin-bottom: -3px; cursor: ns-resize; position: relative; z-index: 1; touch-action: none; }
  .grip:hover, .grip:focus-visible, .grip:active { background: var(--iris); opacity: 0.6; outline: none; }
  .strip { display: flex; align-items: center; gap: 2px; height: 26px; padding: 0 var(--s1); background: var(--main); flex: none; min-width: 0; font-size: var(--t-sm); }
  .tablist { display: flex; gap: 2px; min-width: 0; overflow-x: auto; scrollbar-width: none; }
  .tablist::-webkit-scrollbar { display: none; }
  .tab { display: flex; align-items: center; border-radius: 5px; color: var(--muted); flex: none; max-width: 240px; transition: background-color var(--motion-fast) var(--ease), color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .tab:hover { background: var(--hover); color: var(--ink); }
  }
  .tab:active { background: var(--select); }
  .tab.on { background: var(--raise); color: var(--ink); }
  .tname { display: flex; align-items: center; gap: 6px; padding: 3px 2px 3px var(--s2); font-size: var(--t-sm); font-weight: 500; min-width: 0; }
  .tname:focus-visible { outline-offset: -2px; }
  .tl { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .dot { width: 6px; height: 6px; border-radius: 50%; background: var(--sage); flex: none; }
  .tab.dead .dot, .row.dead .dot { background: var(--faint); }
  .tx { width: 18px; height: 18px; margin-right: 2px; display: grid; place-items: center; border-radius: 4px; color: var(--faint); flex: none; opacity: 0; transition: color var(--motion-fast) var(--ease), background-color var(--motion-fast) var(--ease), opacity var(--motion-fast) var(--ease); }
  .tab:hover .tx, .tab.on .tx, .tx:focus-visible { opacity: 1; }
  @media (hover: hover) {
    .tx:hover { color: var(--ink); background: var(--raise-hover); }
  }
  .tx:active { background: var(--select); }
  .add, .ib { width: 22px; height: 22px; display: grid; place-items: center; border-radius: 5px; color: var(--muted); flex: none; }
  .add:not(:disabled), .ib { transition: color var(--motion-fast) var(--ease), background-color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .add:hover:not(:disabled), .ib:hover { color: var(--ink); background: var(--hover); }
  }
  .add:active:not(:disabled), .ib:active { background: var(--select); }
  .bare { display: flex; align-items: center; gap: 6px; padding: 3px var(--s2); border-radius: 5px; color: var(--muted); font-size: var(--t-sm); font-weight: 500; }
  .bare:not(:disabled) { transition: color var(--motion-fast) var(--ease), background-color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .bare:hover:not(:disabled) { color: var(--ink); background: var(--hover); }
  }
  .bare:active:not(:disabled) { background: var(--select); }
  .bare:disabled { opacity: 0.5; }
  .reqchip { padding: 2px var(--s2); border-radius: 5px; background: var(--gold-wash); color: var(--gold); font-size: var(--t-sm); font-weight: 600; white-space: nowrap; transition: background-color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .reqchip:hover { background: var(--gold-wash-hover); }
  }
  .reqchip:active { background: var(--select); }
  .tab.req .dot, .row.req .dot { background: var(--gold); }
  .tab.req.on { background: var(--gold-wash-hover); }
  .slot { margin-left: auto; display: flex; align-items: center; gap: 2px; flex: none; min-width: 0; }
  .cwd { padding: 0 var(--s2); color: var(--faint); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; max-width: 240px; }
  .tab.outside .tl { font-style: italic; }
  .inwin { color: var(--faint); flex: none; }
  .wins { position: absolute; inset: 0; pointer-events: none; z-index: 15; overflow: hidden; }
  .dockbody { display: flex; flex-direction: column; min-height: 0; background: #19181a; border-top: 1px solid var(--line-soft); }
  .maxed .dockbody { flex: 1; }
  .exit { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; padding: var(--s2) 14px; font-size: var(--t-ui); color: var(--ink-2); background: var(--gold-wash); border-bottom: 1px solid var(--line-soft); }
  .exit span { flex: 1; min-width: 180px; }
  .exit button { font-size: var(--t-ui); font-weight: 600; padding: 6px var(--s3); border-radius: 6px; background: var(--raise); color: var(--ink-2); transition: background-color var(--motion-fast) var(--ease), color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .exit button:hover { background: var(--raise-hover); color: var(--ink); }
  }
  .exit button:active { background: var(--select); }
  button.primary { background: var(--iris); color: #18161d; transition: background-color var(--motion-fast) var(--ease), color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    button.primary:hover { background: var(--iris-hover); color: #18161d; }
  }
  button.primary:active { background: var(--select); color: var(--ink); }
  .open { margin-left: var(--s2); padding: 4px var(--s3); border-radius: 6px; background: var(--iris); color: #18161d; font-weight: 600; }
  .quiet { color: var(--faint); font-size: var(--t-ui); padding: var(--s4); margin: 0; }
  .err { margin: 0; padding: 6px 14px; font-size: var(--t-sm); color: var(--coral); background: rgba(239, 138, 128, 0.08); }

  /* Phone: a one-line bar, a list, and a full-screen sheet. */
  .phone { flex: none; border-top: 1px solid var(--line-soft); background: var(--main); }
  .pbar { display: flex; align-items: center; gap: var(--s2); width: 100%; min-height: 36px; padding: 0 var(--s3); color: var(--muted); font-size: var(--t-ui); font-weight: 500; text-align: left; }
  .pbar:active { background: var(--select); }
  .scrim { position: fixed; inset: 0; z-index: 60; background: rgba(0, 0, 0, 0.5); }
  .sheetback { display: contents; }
  .list { position: absolute; left: 0; right: 0; bottom: 0; max-height: 70dvh; overflow-y: auto; background: var(--main); border-radius: 14px 14px 0 0; border-top: 1px solid var(--line-soft); padding: var(--s3) var(--s2) calc(var(--s3) + env(safe-area-inset-bottom)); }
  .list h2 { margin: 0 var(--s2) var(--s2); font-size: var(--t-ui); color: var(--muted); font-weight: 600; }
  .row { display: flex; align-items: center; border-radius: 8px; }
  .rname { flex: 1; min-width: 0; display: flex; align-items: center; gap: var(--s2); min-height: 48px; padding: 0 var(--s3); font-size: var(--t-ui); color: var(--ink); text-align: left; }
  .rname:active { background: var(--select); }
  .rcwd { margin-left: auto; color: var(--faint); font-size: var(--t-sm); white-space: nowrap; }
  .row .tx { opacity: 1; width: 44px; height: 44px; margin: 0; }
  .row.new { min-height: 48px; padding: 0 var(--s3); color: var(--iris); font-size: var(--t-ui); font-weight: 600; width: 100%; text-align: left; }
  .sheet { position: fixed; inset: 0; z-index: 62; display: flex; flex-direction: column; background: #19181a; padding-top: env(safe-area-inset-top); }
  .sheet header { display: flex; align-items: center; gap: var(--s2); position: relative; min-height: 48px; padding: var(--s3) var(--s1) 0 var(--s4); background: var(--main); border-bottom: 1px solid var(--line-soft); flex: none; font-size: var(--t-ui); font-weight: 600; color: var(--ink); touch-action: none; }
  .sheet header .tl { flex: 1; }
  .sheet header .tx { opacity: 1; width: 44px; height: 44px; margin: 0; }
  .grab { position: absolute; top: 6px; left: 50%; width: 36px; height: 4px; margin-left: -18px; border-radius: 2px; background: var(--line); }
  .sheet .exit button { min-height: 44px; }
</style>
