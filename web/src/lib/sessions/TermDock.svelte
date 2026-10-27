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
  import PhoneKeys from '../ui/PhoneKeys.svelte'
  import { errorText } from '../errorText.ts'
  import { loadDock, saveDock } from '../ui/prefs.ts'
  import { dockReduce, clampHeight, type DockAction } from './termDock.ts'
  import type { TermInfo } from '../api/terminal.ts'

  // session set: only that session's shells show, and + opens one in its
  // folder (a session view). owner names the room's folder in the chips; names
  // maps session ids to names. request counts up when the app wants the dock
  // open (the old terminal link, the palette).
  // noRepo: the room has no repository, so a room terminal has no folder.
  let { roomId, session = '', owner = '', narrow = false, names = {}, noRepo = false, request = 0 }: { roomId: string; session?: string; owner?: string; narrow?: boolean; names?: Record<string, string>; noRepo?: boolean; request?: number } = $props()
  let ref = $derived(session || roomId)
  let roomless = $derived(noRepo && ref === roomId)

  let dock = $state(untrack(() => loadDock(roomId)))
  let all = $state<TermInfo[] | null>(null)
  let error = $state('')
  let opening = $state(false)
  let exited = $state<Record<string, number>>({}) // id -> exit code
  let closing = $state<TermInfo | null>(null) // the terminal waiting on the close confirm
  let pane = $state<{ key(name: string): void; send(text: string): void; focus(): void }>()
  let ctrl = $state(false)
  let tabsEl = $state<HTMLElement>()
  let rootEl = $state<HTMLElement>()
  let avail = $state(800) // height of the main area
  let dragging = $state(false)
  let listing = $state(false) // phone: the list of terminals
  let sheet = $state(false) // phone: the open terminal

  const act = (a: DockAction) => (dock = dockReduce(dock, a))

  // A room lists every shell in the room, its sessions' too; a session view
  // lists that session's.
  let list = $derived((all ?? []).filter((t) => (ref === roomId ? t.roomId === roomId || t.ref === roomId : t.sessionId === ref || t.ref === ref)))
  // The saved tab, or the newest shell when it is gone or this ref has none yet.
  let active = $derived(list.find((t) => t.id === dock.active[ref])?.id ?? list.at(-1)?.id ?? '')
  let current = $derived(list.find((t) => t.id === active))
  let height = $derived(clampHeight(dock.height, avail))

  // Not while dragging: one write when the handle is let go.
  $effect(() => { const d = dock; if (!dragging) saveDock(roomId, d) })

  $effect(() => {
    const el = rootEl?.parentElement
    if (!el) return
    avail = el.clientHeight
    const observer = new ResizeObserver(() => (avail = el.clientHeight))
    observer.observe(el)
    return () => observer.disconnect()
  })

  let seen = untrack(() => request)
  $effect(() => { if (request !== seen) { seen = request; show() } })

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
      untrack(() => {
        if (!list.some((t) => t.id === active)) act({ type: 'tab', ref, id: list.at(-1)?.id ?? '' })
      })
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

  function pick(t: TermInfo) {
    act({ type: 'tab', ref, id: t.id })
    if (narrow) { listing = false; sheet = true } else act({ type: 'show' })
  }

  async function close(t: TermInfo) {
    closing = null
    try {
      await api.termClose(t.id)
    } catch (e) {
      error = errorText(e)
    }
    all = (all ?? []).filter((x) => x.id !== t.id)
    if (active === t.id) act({ type: 'tab', ref, id: list.at(-1)?.id ?? '' })
    if (!list.length) { act({ type: 'hide' }); sheet = false }
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
    if (!dock.open || !(e.target instanceof Node && rootEl?.contains(e.target))) return
    const k = e.key.toLowerCase()
    const i = list.findIndex((t) => t.id === active)
    const go = (n: number) => { const t = list[n]; if (t) act({ type: 'tab', ref, id: t.id }) }
    if (e.shiftKey && k === 't') open()
    else if (e.shiftKey && k === 'w' && current) askClose(current)
    else if (k === 'pagedown' && list.length) go((i + 1) % list.length)
    else if (k === 'pageup' && list.length) go((i - 1 + list.length) % list.length)
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

  // The top edge resizes the dock; the arrow keys do the same in steps.
  function drag(e: PointerEvent) {
    if (!rootEl) return
    const grip = e.currentTarget as HTMLElement
    grip.setPointerCapture(e.pointerId)
    dragging = true
    const bottom = rootEl.getBoundingClientRect().bottom
    const strip = rootEl.querySelector<HTMLElement>('.strip')?.offsetHeight ?? 0
    const move = (m: PointerEvent) => act({ type: 'resize', height: bottom - strip - m.clientY, avail })
    const up = () => { dragging = false; grip.removeEventListener('pointermove', move); grip.removeEventListener('pointerup', up); grip.removeEventListener('pointercancel', up) }
    grip.addEventListener('pointermove', move)
    grip.addEventListener('pointerup', up)
    grip.addEventListener('pointercancel', up)
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

{#snippet body()}
  {#if error}<p class="err">{error}</p>{/if}
  {#if current}
    {#if exited[current.id] !== undefined || current.alive === false}
      <div class="exit">
        <span>{exitText(current)}</span>
        <button onclick={() => close(current)}>Close tab</button>
        {#if !roomless}<button class="primary" onclick={async () => { const old = current; await open(); close(old) }}>Start a new shell</button>{/if}
      </div>
    {/if}
    {#key current.id}
      <TermPane bind:this={pane} bind:ctrl id={current.id} {narrow} onexit={(code) => (exited = { ...exited, [current.id]: code })} />
    {/key}
    {#if narrow}
      <PhoneKeys {ctrl} onkey={(k: string) => { if (k === 'ctrl') { ctrl = !ctrl; pane?.focus() } else pane?.key(k) }} />
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
      {#if !list.length}<svg viewBox="0 0 16 16" width="12" height="12" aria-hidden="true"><path d="M8 3v10M3 8h10" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" /></svg>{/if}
    </button>
  </div>
  {#if listing}
    <button class="scrim" aria-label="Close the list" onclick={() => (listing = false)}></button>
    <div class="list" role="dialog" aria-modal="true" aria-label="Terminals">
      <h2>Terminals</h2>
      {#each list as t (t.id)}
        <div class="row" class:dead={t.alive === false || exited[t.id] !== undefined}>
          <button class="rname" onclick={() => pick(t)}><span class="dot" aria-hidden="true"></span><span class="tl">{label(t)}</span><span class="rcwd mono">{base(t.cwd)}</span></button>
          <button class="tx" onclick={() => askClose(t)} aria-label="Close {label(t)}"><svg viewBox="0 0 16 16" width="12" height="12" aria-hidden="true"><path d="M4 4l8 8M12 4l-8 8" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" /></svg></button>
        </div>
      {/each}
      {#if error}<p class="err">{error}</p>{/if}
      {#if roomless}<p class="quiet">This room has no repository, so a room terminal has no folder. Open one from a session.</p>
      {:else}<button class="row new" onclick={open} disabled={opening}>+ New terminal</button>{/if}
    </div>
  {/if}
  {#if sheet && current}
    <div class="sheet" role="dialog" aria-modal="true" aria-label="Terminal {label(current)}">
      <header role="presentation" ontouchstart={swipeStart} ontouchend={swipeEnd}>
        <span class="grab" aria-hidden="true"></span>
        <span class="tl">{label(current)}</span>
        <button class="tx" onclick={() => (sheet = false)} aria-label="Back to the room"><svg viewBox="0 0 16 16" width="14" height="14" aria-hidden="true"><path d="M4 4l8 8M12 4l-8 8" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" /></svg></button>
      </header>
      {@render body()}
    </div>
  {/if}
{:else}
  <section class="termdock" class:maxed={dock.open && dock.maxed} bind:this={rootEl}>
    {#if dock.open}
      <!-- svelte-ignore a11y_no_noninteractive_tabindex, a11y_no_noninteractive_element_interactions -->
      <div class="grip" role="separator" aria-orientation="horizontal" aria-label="Resize the terminal" aria-valuenow={height} aria-valuemin={120} tabindex="0" onpointerdown={drag} onkeydown={gripKey}></div>
    {/if}
    <div class="strip">
      {#if !list.length && !dock.open}
        <button class="bare" onclick={show} disabled={roomless} title={roomless ? 'This room has no repository, so a room terminal has no folder' : 'Open a terminal (Ctrl+`)'}>
          <svg viewBox="0 0 16 16" width="12" height="12" aria-hidden="true"><path d="M8 3v10M3 8h10" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" /></svg>
          Terminal
        </button>
      {:else}
        <!-- svelte-ignore a11y_interactive_supports_focus -->
        <div class="tablist" role="tablist" aria-label="Terminals" bind:this={tabsEl} onkeydown={tabKey}>
          {#each list as t (t.id)}
            <div class="tab" class:on={dock.open && t.id === active} class:dead={t.alive === false || exited[t.id] !== undefined}>
              <button role="tab" aria-selected={dock.open && t.id === active} tabindex={t.id === active ? 0 : -1} class="tname" onclick={() => pick(t)} title={t.cwd}>
                <span class="dot" aria-hidden="true"></span>
                <span class="tl">{label(t)}</span>
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
      <!-- The right end: a later requests chip goes before the dock buttons. -->
      <div class="slot">
        {#if dock.open}
          {#if current?.cwd}<span class="cwd mono" title={current.cwd}>{base(current.cwd)}</span>{/if}
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
        {@render body()}
      </div>
    {/if}
  </section>
{/if}

{#if closing}
  {@const target = closing}
  <Confirm title="Close this terminal?" lines={['Its shell stops, and so does any command running in it.']} action="Close terminal" onconfirm={() => close(target)} oncancel={() => (closing = null)} />
{/if}

<style>
  .termdock { flex: none; display: flex; flex-direction: column; min-width: 0; background: #19181a; border-top: 1px solid var(--line-soft); }
  .termdock.maxed { position: absolute; inset: 0; z-index: 20; }
  .grip { height: 5px; margin-bottom: -3px; cursor: ns-resize; position: relative; z-index: 1; touch-action: none; }
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
  .bare { display: flex; align-items: center; gap: 6px; padding: 3px var(--s2); border-radius: 5px; color: var(--muted); font-size: var(--t-sm); font-weight: 500; transition: color var(--motion-fast) var(--ease), background-color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .bare:hover:not(:disabled) { color: var(--ink); background: var(--hover); }
  }
  .bare:disabled { opacity: 0.5; }
  .slot { margin-left: auto; display: flex; align-items: center; gap: 2px; flex: none; min-width: 0; }
  .cwd { padding: 0 var(--s2); color: var(--faint); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; max-width: 240px; }
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
  .list { position: fixed; left: 0; right: 0; bottom: 0; z-index: 61; max-height: 70dvh; overflow-y: auto; background: var(--main); border-radius: 14px 14px 0 0; border-top: 1px solid var(--line-soft); padding: var(--s3) var(--s2) calc(var(--s3) + env(safe-area-inset-bottom)); }
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
