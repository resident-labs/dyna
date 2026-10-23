<script module lang="ts">
  // The open tab per room or session, kept across view switches.
  const lastTab = new Map<string, string>()
</script>

<script lang="ts">
  // Terminals for a room (its repo folder) or one session (its working
  // directory): a tab per shell, a + to open another, and the shell itself.
  // Shells live on the host, so they survive a reload; the app lists them
  // again and reattaches. On a phone a key row adds Esc, Tab, Ctrl and the
  // arrows, which a soft keyboard lacks.
  import { onMount, untrack, tick } from 'svelte'
  import { api } from '../../api.ts'
  import { base } from '../files/repo.ts'
  import TermPane from './TermPane.svelte'
  import Confirm from '../ui/Confirm.svelte'
  import EmptyState from '../ui/EmptyState.svelte'
  import PhoneKeys from '../ui/PhoneKeys.svelte'
  import { errorText } from '../errorText.ts'
  import type { TermInfo } from '../api/terminal.ts'

  // ref is the room id or a session id; names maps session ids to names
  // for the tab labels.
  // noRepo: the room has no repository, so a room terminal has no folder.
  let { roomId, ref, narrow = false, names = {}, noRepo = false }: { roomId: string; ref: string; narrow?: boolean; names?: Record<string, string>; noRepo?: boolean } = $props()
  let roomless = $derived(noRepo && ref === roomId)

  let all = $state<TermInfo[] | null>(null)
  let error = $state('')
  let active = $state(untrack(() => lastTab.get(ref)) ?? '') // the view is keyed by ref
  let opening = $state(false)
  let exited = $state<Record<string, number>>({}) // id -> exit code
  let closing = $state<TermInfo | null>(null) // the terminal waiting on the close confirm
  let pane = $state<{ key(name: string): void; send(text: string): void; focus(): void }>()
  let ctrl = $state(false)
  let tabsEl = $state<HTMLElement>()

  // A room view lists every shell in the room, its sessions' too; a
  // session view lists that session's.
  let list = $derived((all ?? []).filter((t) => (ref === roomId ? t.roomId === roomId || t.ref === roomId : t.sessionId === ref || t.ref === ref)))
  let current = $derived(list.find((t) => t.id === active))

  $effect(() => { if (active) lastTab.set(ref, active) })

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
        if (!list.some((t) => t.id === active)) active = list.at(-1)?.id ?? ''
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
      active = t.id
      load()
    } catch (e) {
      error = errorText(e)
    } finally {
      opening = false
    }
  }

  async function close(t: TermInfo) {
    closing = null
    try {
      await api.termClose(t.id)
    } catch (e) {
      error = errorText(e)
    }
    all = (all ?? []).filter((x) => x.id !== t.id)
    if (active === t.id) active = list.at(-1)?.id ?? ''
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

  function label(t: TermInfo, i: number) {
    const who = t.sessionId && ref === roomId ? names[t.sessionId] : ''
    return who ? `${who} · ${t.title || 'shell'}` : `${t.title || t.shell?.split('/').pop() || 'shell'}${list.length > 1 ? ` ${i + 1}` : ''}`
  }

  // Ctrl+Shift+T opens a tab, Ctrl+Shift+W closes it, and Ctrl+PageUp or
  // Ctrl+PageDown switch, as in most terminal apps.
  function shortcut(e: KeyboardEvent) {
    if (!e.ctrlKey || e.altKey || e.metaKey) return
    const k = e.key.toLowerCase()
    const i = list.findIndex((t) => t.id === active)
    if (e.shiftKey && k === 't') open()
    else if (e.shiftKey && k === 'w' && current) askClose(current)
    else if (k === 'pagedown' && list.length) active = list[(i + 1) % list.length]?.id ?? active
    else if (k === 'pageup' && list.length) active = list[(i - 1 + list.length) % list.length]?.id ?? active
    else return
    e.preventDefault()
  }

  function tabKey(e: KeyboardEvent) {
    const i = list.findIndex((t) => t.id === active)
    const next = e.key === 'Home' ? 0 : e.key === 'End' ? list.length - 1
      : e.key === 'ArrowRight' ? (i + 1) % list.length
      : e.key === 'ArrowLeft' ? (i - 1 + list.length) % list.length : -1
    if (next >= 0 && list[next]) {
      e.preventDefault()
      active = list[next].id
      tick().then(() => {
        tabsEl?.querySelector<HTMLElement>('[aria-selected="true"]')?.focus({ preventScroll: true })
        revealTab()
      })
    } else if (e.key === 'Delete' && current) askClose(current)
  }

  onMount(() => {
    load().then(() => {
      // A terminal view with no shell has nothing to show; open one.
      if (!list.length && !error && !roomless) open()
    })
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

<svelte:window onkeydown={shortcut} />

<section class="terminal">
  <div class="tabs">
    <!-- svelte-ignore a11y_interactive_supports_focus -->
    <div class="tablist" role="tablist" aria-label="Terminals" bind:this={tabsEl} onkeydown={tabKey}>
      {#each list as t, i (t.id)}
        <div class="tab" class:on={t.id === active} class:dead={t.alive === false || exited[t.id] !== undefined}>
          <button role="tab" aria-selected={t.id === active} tabindex={t.id === active ? 0 : -1} class="tname" onclick={() => (active = t.id)} title={t.cwd}>
            <span class="dot" aria-hidden="true"></span>
            <span class="tl">{label(t, i)}</span>
          </button>
          <button class="tx" onclick={() => askClose(t)} aria-label="Close {label(t, i)}" title="Close (Ctrl+Shift+W)">
            <svg viewBox="0 0 16 16" width="12" height="12" aria-hidden="true"><path d="M4 4l8 8M12 4l-8 8" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" /></svg>
          </button>
        </div>
      {/each}
    </div>
    {#if !roomless}<button class="add" onclick={open} disabled={opening} aria-label="New terminal" title="New terminal (Ctrl+Shift+T)">
      <svg viewBox="0 0 16 16" width="15" height="15" aria-hidden="true"><path d="M8 3v10M3 8h10" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" /></svg>
    </button>{/if}
    {#if current?.cwd}<span class="cwd mono" title={current.cwd}>{base(current.cwd)}</span>{/if}
  </div>

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
  {:else if roomless}
    <EmptyState title="No repository">This room has no repository, so a room terminal has no folder to open in. Set one in room settings, or open a terminal from a session.</EmptyState>
  {:else if all && !opening}
    <EmptyState title="No terminal open">A terminal runs a shell on the host, in {ref === roomId ? "the room's repo folder" : "this session's working folder"}.<br /><button class="primary open" onclick={open}>Open a terminal</button></EmptyState>
  {:else}
    <p class="quiet">Starting a shell…</p>
  {/if}
</section>

{#if closing}
  {@const target = closing}
  <Confirm title="Close this terminal?" lines={['Its shell stops, and so does any command running in it.']} action="Close terminal" onconfirm={() => close(target)} oncancel={() => (closing = null)} />
{/if}

<style>
  .terminal { flex: 1; display: flex; flex-direction: column; min-height: 0; min-width: 0; background: #19181a; }
  .tabs { display: flex; align-items: center; gap: var(--s1); padding: 6px 10px 0; background: var(--main); border-bottom: 1px solid var(--line-soft); flex: none; min-width: 0; }
  .tablist { display: flex; gap: 2px; min-width: 0; overflow-x: auto; scrollbar-width: none; }
  .tablist::-webkit-scrollbar { display: none; }
  .tab { display: flex; align-items: center; border-radius: 7px 7px 0 0; color: var(--muted); flex: none; max-width: 240px; transition: background-color var(--motion-fast) var(--ease), color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .tab:hover { background: var(--hover); color: var(--ink); }
  }
  .tab:active { background: var(--select); }
  .tab.on { background: #19181a; color: var(--ink); }
  .tname { display: flex; align-items: center; gap: var(--s2); padding: 7px var(--s1) var(--s2) var(--s3); font-size: var(--t-ui); font-weight: 500; min-width: 0; }
  .tname:focus-visible { outline-offset: -2px; }
  .tl { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .dot { width: 7px; height: 7px; border-radius: 50%; background: var(--sage); flex: none; }
  .tab.dead .dot { background: var(--faint); }
  .tx { width: 24px; height: 24px; margin-right: var(--s1); display: grid; place-items: center; border-radius: 5px; color: var(--faint); flex: none; transition: color var(--motion-fast) var(--ease), background-color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .tx:hover { color: var(--ink); background: var(--raise-hover); }
  }
  .tx:active { background: var(--select); }
  .add { width: 28px; height: 28px; display: grid; place-items: center; border-radius: 6px; color: var(--muted); flex: none; margin-bottom: 2px; }
  .add:not(:disabled) { transition: color var(--motion-fast) var(--ease), background-color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .add:hover:not(:disabled) { color: var(--ink); background: var(--hover); }
  }
  .add:active:not(:disabled) { background: var(--select); }
  .cwd { margin-left: auto; padding: 0 var(--s1) var(--s1) var(--s3); font-size: var(--t-sm); color: var(--faint); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; flex: 0 1 auto; min-width: 0; }
  .exit { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; padding: var(--s2) 14px; font-size: var(--t-ui); color: var(--ink-2); background: var(--gold-wash); border-bottom: 1px solid var(--line-soft); }
  .exit span { flex: 1; min-width: 180px; }
  .exit button, .open { font-size: var(--t-ui); font-weight: 600; padding: 6px var(--s3); border-radius: 6px; background: var(--raise); color: var(--ink-2); transition: background-color var(--motion-fast) var(--ease), color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .exit button:hover, .open:hover { background: var(--raise-hover); color: var(--ink); }
  }
  .exit button:active, .open:active { background: var(--select); }
  button.primary { background: var(--iris); color: #18161d; transition: background-color var(--motion-fast) var(--ease), color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    button.primary:hover { background: var(--iris-hover); color: #18161d; }
  }
  button.primary:active { background: var(--select); color: var(--ink); }
  .open { margin-top: var(--s3); }
  .quiet { color: var(--faint); font-size: var(--t-ui); padding: var(--s4); }
  .err { margin: 0; padding: 6px 14px; font-size: var(--t-sm); color: var(--coral); background: rgba(239, 138, 128, 0.08); }


  @media (max-width: 760px) {
    .tabs { padding: var(--s1) var(--s1) 0; }
    .tname { padding: var(--s3) var(--s1) var(--s3) var(--s3); }
    .tx { width: 36px; height: 40px; }
    .add { width: 44px; height: 44px; }
    .cwd { display: none; }
    .exit button, .open { min-height: 44px; }
  }
</style>
