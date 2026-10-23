<script lang="ts">
  // The live viewer. Toolbar on top, stream below, stats and action feed
  // under it. Control state lives in the toolbar and the frame only: when
  // the owner has control, NOTHING is drawn over the stream. The only
  // overlay is the "Starting stream…" / "Stream ended" note, which shows
  // when there is no stream to cover, and control cannot be on then.
  import { onMount, type ComponentProps } from 'svelte'
  import { api } from '../../api.ts'
  import { openStream, savedStreamMode, type StreamHandle, type StreamMode } from './computerStream.ts'
  import { stateWord, hintText, escTwice, viewKey, popOut, type FeedItem } from './computers.ts'
  import { statsLine, type MeterSnapshot } from './rfbStats.ts'
  import ComputerShot from './ComputerShot.svelte'
  import ComputerChat from './ComputerChat.svelte'
  import { badgeText } from './preview.ts'
  import PhoneKeys from '../ui/PhoneKeys.svelte'
  import { phoneKeyboard, type Modifiers } from './phoneKeyboard.ts'
  import { errorText } from '../errorText.ts'
  import type { ComputerInfo, ComputerStream } from '../api/computer.ts'

  // ShownComputer is the part of a computer row the viewer reads. stream is
  // partial because the app passes an empty stream until the room list has
  // the session's row.
  type ShownComputer = Omit<ComputerInfo, 'stream'> & { stream?: Partial<ComputerStream> | null }
  // The viewer's own phase; a failed or ended stream can be retried.
  type ViewerPhase = 'connecting' | 'connected' | 'ended' | 'failed'
  // Zoom and pan of the picture inside the frame, and a point on the screen.
  interface Zoom { s: number; x: number; y: number }
  interface Point { x: number; y: number }
  type Gesture = { kind: 'pinch'; d: number; m: Point; z: Zoom; r: DOMRect } | { kind: 'pan'; m: Point; z: Zoom; r: DOMRect }

  interface Props {
    // c: the computer's status. feed: its recent actions [{line, at}].
    // room: the room ID, for Post shot. popout: this is its own window.
    // chat: what the floating chat needs {session, room, members, onsend,
    // onopenfile, onpost}. Without it (the pop-out) no chat shows.
    c: ShownComputer
    room: string
    feed?: FeedItem[]
    popout?: boolean
    chat?: ComponentProps<typeof ComputerChat> | null
  }
  let { c, room, feed = [], popout = false, chat = null }: Props = $props()
  let chatEl = $state<{ streamClick: () => void } | null>(null)

  let keyboardInput = $state<HTMLTextAreaElement | undefined>()
  let keyboardOpen = $state(false)
  let modifiers = $state<Modifiers>({ ctrl: false, alt: false })
  let keyboard: ReturnType<typeof phoneKeyboard> | null = null
  function bindKeyboard(node: HTMLTextAreaElement) {
    keyboard = phoneKeyboard(node, {
      sendKey: (symbol, code, down) => { if (human && phase === 'connected') stream?.rfb?.sendKey(symbol, code ?? null, down) },
      changed: (value) => (modifiers = value),
    })
    return { destroy() { keyboard?.destroy(); keyboard = null } }
  }
  function toggleKeyboard() {
    keyboardOpen = !keyboardOpen
    if (keyboardOpen) keyboardInput?.focus({ preventScroll: true })
    else keyboardInput?.blur()
  }
  $effect(() => { if (!human) { keyboardOpen = false; keyboardInput?.blur() } })

  const key = viewKey()
  let target = $state<HTMLDivElement | undefined>()
  let phase = $state<ViewerPhase>('connecting') // connecting | connected | ended | failed
  let note = $state('')
  let human = $state(false)
  let busy = $state(false)
  let error = $state('')
  let posted = $state('')
  let stats = $state<MeterSnapshot>({ fps: 0, mbit: 0, codec: '', latency: null })
  let stream: StreamHandle | null = null
  let screenEl = $state<HTMLDivElement | undefined>()
  let present = $state<HTMLCanvasElement | undefined>()
  // presenting: the page paints the picture itself. WebKitGTK scales a canvas
  // with CSS by dropping pixel rows, which wrecks small text. An image or a
  // canvas drawn with smoothing is sharp, so when the picture shrinks, paint a
  // second canvas with high-quality smoothing and hide noVNC's.
  let presenting = $state(false)
  let dirty = true
  let mode = $state(savedStreamMode(typeof localStorage !== 'undefined' ? localStorage.getItem('dyna.computer.mode') : null))
  function setMode(next: StreamMode) {
    if (mode === next) return
    mode = next
    try { localStorage.setItem('dyna.computer.mode', next) } catch {}
    stream?.setMode(next)
  }

  const word = $derived(stateWord(phase, human))
  // "Live" shows on the badge for a moment after the first frame.
  let liveShown = $state(false)
  let liveTimer: ReturnType<typeof setTimeout> | undefined
  function retry() { error = ''; stream?.reconnect() }

  onMount(() => {
    if (!target) return
    const s = openStream({
      target,
      mode,
      url: () => api.computerViewURL(c.session, key),
      on(state, detail) {
        if (state === 'connected') {
          phase = 'connected'
          note = ''
          liveShown = true
          clearTimeout(liveTimer)
          liveTimer = setTimeout(() => (liveShown = false), 1600)
        }
        else if (state === 'connecting') { phase = 'connecting'; human = false }
        else if (state === 'fallback') note = detail.reason ?? ''
        else {
          // The lease ends with the stream, so control is back with the agent.
          human = false
          phase = detail.retrying ? 'connecting' : state === 'failed' ? 'failed' : 'ended'
          note = detail.reason ?? ''
        }
      },
    })
    stream = s
    const t = setInterval(() => (stats = s.meter.snapshot()), 1000)
    s.onFrame = () => (dirty = true)
    const ro = new ResizeObserver(() => (dirty = true))
    if (screenEl) ro.observe(screenEl)
    let raf = 0
    const loop = () => {
      raf = requestAnimationFrame(loop)
      if (dirty) paint()
    }
    loop()
    return () => {
      cancelAnimationFrame(raf)
      ro.disconnect()
      clearInterval(t)
      clearTimeout(liveTimer)
      s.close()
    }
  })

  function paint() {
    const src = target?.querySelector<HTMLCanvasElement>('canvas')
    if (!src || !present || !screenEl || !src.width) return
    dirty = false
    const dpr = window.devicePixelRatio || 1
    const cw = screenEl.clientWidth, ch = screenEl.clientHeight
    const w = Math.round(cw * dpr), h = Math.round(ch * dpr)
    const shrink = w < src.width && w > 0
    if (shrink !== presenting) presenting = shrink
    if (!shrink) return
    if (present.width !== w || present.height !== h) { present.width = w; present.height = h }
    const x = present.getContext('2d')
    if (!x) return
    x.imageSmoothingEnabled = true
    x.imageSmoothingQuality = 'high'
    const k = src.width / cw
    // The visible part of the picture, in source pixels.
    x.drawImage(src, (-zoom.x / zoom.s) * k, (-zoom.y / zoom.s) * k, (cw / zoom.s) * k, (ch / zoom.s) * k, 0, 0, w, h)
  }
  $effect(() => { zoom; dirty = true })

  // The daemon may take control back (for example, the lease ended).
  // Only a status that arrives after the owner took control counts: the one
  // the page held at that moment still says "agent".
  let takenWith: ShownComputer | null = null
  $effect(() => {
    if (human && c !== takenWith && c.control === 'agent') {
      human = false
      stream?.setControl(false)
    }
  })

  // The ⋯ menu. It closes on an outside click or Esc.
  let menuOpen = $state(false)
  let menuEl = $state<HTMLSpanElement | undefined>()
  $effect(() => {
    if (!menuOpen) return
    const away = (e: PointerEvent) => { if (!(e.target instanceof Node && menuEl?.contains(e.target))) menuOpen = false }
    const esc = (e: KeyboardEvent) => { if (e.key === 'Escape') menuOpen = false }
    addEventListener('pointerdown', away, true)
    addEventListener('keydown', esc)
    return () => { removeEventListener('pointerdown', away, true); removeEventListener('keydown', esc) }
  })
  // Esc twice within 600 ms returns control. The first Esc still reaches the computer.
  const twice = escTwice()
  $effect(() => {
    if (!human) return
    // Esc typed in the floating chat is not for the computer.
    const k = (e: KeyboardEvent) => { if (e.key === 'Escape' && !(e.target instanceof Element && e.target.closest('[data-chat]')) && twice(performance.now())) giveBack() }
    addEventListener('keydown', k, true)
    return () => removeEventListener('keydown', k, true)
  })

  async function take() {
    busy = true
    error = ''
    try {
      await api.computerControl(c.session, key, 'human')
      takenWith = c
      human = true
      stream?.setControl(true)
    } catch (e) {
      error = `Could not take control: ${errorText(e)}`
    } finally {
      busy = false
    }
  }

  async function giveBack() {
    busy = true
    error = ''
    stream?.setControl(false)
    human = false
    try {
      await api.computerControl(c.session, key, 'agent')
    } catch (e) {
      error = `Could not give control back: ${errorText(e)}`
    } finally {
      busy = false
    }
  }

  async function postShot() {
    error = ''
    posted = ''
    try {
      const r = await fetch(api.computerShotURL(c.session, true), { credentials: 'same-origin' })
      if (!r.ok) throw new Error(`HTTP ${r.status}`)
      const bytes = new Uint8Array(await r.arrayBuffer())
      let bin = ''
      for (let i = 0; i < bytes.length; i += 0x8000) bin += String.fromCharCode(...bytes.subarray(i, i + 0x8000))
      const att = await api.attach(room, `computer-${c.name || c.session}-${Date.now()}.png`, btoa(bin))
      await api.send(room, c.name ? `@${c.name} a screenshot of your computer` : 'A screenshot of the computer', '', [att])
      posted = 'Screenshot posted to the room'
    } catch (e) {
      error = `Could not post the screenshot: ${errorText(e)}`
    }
  }


  // Pinch zoom and pan for a phone, while the owner only watches. noVNC
  // takes every touch on its canvas (it calls preventDefault), so the
  // browser's own pinch cannot work there. This zooms the picture inside
  // the frame. In control mode, noVNC owns touch: two fingers scroll the
  // computer, and the zoom resets so taps land where the finger is.
  let zoom = $state<Zoom>({ s: 1, x: 0, y: 0 })
  const zoomed = $derived(zoom.s > 1.001)
  $effect(() => { if (human) zoom = { s: 1, x: 0, y: 0 } })
  const clampZoom = (z: Zoom, w: number, h: number): Zoom => ({ s: z.s, x: Math.min(0, Math.max(w * (1 - z.s), z.x)), y: Math.min(0, Math.max(h * (1 - z.s), z.y)) })
  function touchZoom(node: HTMLElement) {
    let g: Gesture | null = null
    const pts = (e: TouchEvent): Point[] => [...e.touches].map((t) => ({ x: t.clientX, y: t.clientY }))
    const mid = (a: Point, b: Point): Point => ({ x: (a.x + b.x) / 2, y: (a.y + b.y) / 2 })
    const dist = (a: Point, b: Point): number => Math.hypot(a.x - b.x, a.y - b.y)
    function start(e: TouchEvent) {
      if (human) return
      const p = pts(e)
      const r = node.getBoundingClientRect()
      const [a, b] = p
      if (p.length === 2 && a && b) g = { kind: 'pinch', d: dist(a, b), m: mid(a, b), z: { ...zoom }, r }
      else if (p.length === 1 && a && zoom.s > 1.001) g = { kind: 'pan', m: a, z: { ...zoom }, r }
      else g = null
    }
    function move(e: TouchEvent) {
      if (!g || human) return
      const p = pts(e)
      const [a, b] = p
      if (g.kind === 'pinch' && a && b) {
        e.preventDefault()
        const s = Math.min(5, Math.max(1, g.z.s * dist(a, b) / g.d))
        const m = mid(a, b)
        // The content point under the start midpoint follows the fingers.
        const cx = (g.m.x - g.r.left - g.z.x) / g.z.s
        const cy = (g.m.y - g.r.top - g.z.y) / g.z.s
        zoom = clampZoom({ s, x: m.x - g.r.left - cx * s, y: m.y - g.r.top - cy * s }, g.r.width, g.r.height)
      } else if (g.kind === 'pan' && p.length === 1 && a) {
        e.preventDefault()
        zoom = clampZoom({ s: g.z.s, x: g.z.x + a.x - g.m.x, y: g.z.y + a.y - g.m.y }, g.r.width, g.r.height)
      }
    }
    const end = (e: TouchEvent) => { if (e.touches.length === 0) g = null; else start(e) }
    // Capture phase: noVNC stops the events at its canvas, below this node.
    node.addEventListener('touchstart', start, { capture: true, passive: true })
    node.addEventListener('touchmove', move, { capture: true, passive: false })
    node.addEventListener('touchend', end, { capture: true })
    node.addEventListener('touchcancel', end, { capture: true })
    return { destroy() {
      node.removeEventListener('touchstart', start, { capture: true })
      node.removeEventListener('touchmove', move, { capture: true })
      node.removeEventListener('touchend', end, { capture: true })
      node.removeEventListener('touchcancel', end, { capture: true })
    } }
  }

  const ratio = $derived(c.width && c.height ? c.width / c.height : 1.6)
  const lines = $derived(statsLine(stats, { stream: c.stream ?? undefined }))
  const lastActs = $derived(feed.slice(-3).map((a) => a.line))
  // The picture choices of the ⋯ menu.
  const MODES: StreamMode[] = ['auto', 'sharp', 'smooth']
  // autocorrect is a real browser attribute, but the Svelte types do not name it.
  const noAutocorrect = { autocorrect: 'off' }
</script>

<div class="viewer">
  <div class="top">
    <span class="state state-label" class:gold={human}><span class="dot" class:live={phase === 'connected'} class:gold={human}></span>{word}</span>
    <span class="sp"></span>
    {#if human}
      <button class="round keyboard" onpointerdown={(e) => e.preventDefault()} onclick={toggleKeyboard} aria-label="Phone keyboard" aria-pressed={keyboardOpen} title="Phone keyboard">⌨</button>
      <button class="pill gold" onclick={giveBack} disabled={busy}>Return to agent</button>
    {:else}
      <button class="pill" onclick={take} disabled={busy || phase !== 'connected'}>Take control</button>
    {/if}
    <span class="more" bind:this={menuEl}>
      <button class="round" onclick={() => (menuOpen = !menuOpen)} aria-haspopup="menu" aria-expanded={menuOpen} aria-label="More" title="More">⋯</button>
      {#if menuOpen}
        <div class="menu" role="menu">
          {#each MODES as choice}
            <button role="menuitemradio" aria-checked={mode === choice} onclick={() => { setMode(choice); menuOpen = false }}>{choice.charAt(0).toUpperCase() + choice.slice(1)}<span>{mode === choice ? '✓' : ''}</span></button>
          {/each}
          {#if zoomed}<button role="menuitem" onclick={() => { zoom = { s: 1, x: 0, y: 0 }; menuOpen = false }}>Fit to window</button>{/if}
          <hr />
          <button role="menuitem" onclick={() => { menuOpen = false; postShot() }}>Post screenshot to room</button>
          {#if !popout}<button role="menuitem" class="nophone" onclick={() => { menuOpen = false; popOut(room, c.session) }}>Open in its own window</button>{/if}
          <hr />
          <div class="statline" role="note"><i>Stream</i><span>{lines.filter((l) => !l.startsWith('latency')).join(' · ')}</span></div>
        </div>
      {/if}
    </span>
  </div>

  <div class="stage">
   <div class="frame" style:--r={ratio}>
    <div class="screen" class:control={human} use:touchZoom bind:this={screenEl} onpointerdowncapture={() => chatEl?.streamClick()}>
      <canvas class="present" class:on={presenting} bind:this={present}></canvas>
      <div class="still" class:gone={phase === 'connected'}>
        <ComputerShot session={c.session} shotAt={c.shotAt} phase={c.state} alt="Latest picture of the computer" />
      </div>
      <div class="rfb" class:hide={presenting || phase !== 'connected'} bind:this={target} style:transform={zoomed && !presenting ? `translate(${zoom.x}px, ${zoom.y}px) scale(${zoom.s})` : null}></div>
      {#if badgeText(phase, liveShown)}
        <div class="badge" class:bad={phase === 'failed' || phase === 'ended'} class:ok={phase === 'connected'} role="status">
          {#if phase === 'connecting'}<span class="spin" aria-hidden="true"></span>{/if}
          {badgeText(phase, liveShown)}
          {#if phase === 'failed' || phase === 'ended'}<span class="sep">·</span><button class="retry" onclick={retry}>Retry</button>{/if}
        </div>
      {/if}
    </div>
    {#if chat}<ComputerChat bind:this={chatEl} {...chat} {c} name={c.name} driving={human} />{/if}
   </div>
  </div>

  {#if human}
    <textarea class="keyboard-input" aria-label="Type on computer" bind:this={keyboardInput} use:bindKeyboard onblur={() => (keyboardOpen = false)} autocapitalize="off" autocomplete="off" {...noAutocorrect} spellcheck="false" rows="1"></textarea>
    {#if keyboardOpen}
      <PhoneKeys {...modifiers} label="Computer keys" onkey={(key: number) => { keyboard?.key(key); keyboardInput?.focus({ preventScroll: true }) }} />
    {/if}
  {/if}

  <div class="foot">
    {#if !chat}<span class="now">{lastActs.length ? lastActs.at(-1) : 'no actions yet'}</span>{/if}
    <span class="sp"></span>
    {#if error}<span class="err" role="alert">{error}</span>
    {:else if posted}<span class="okmsg" role="status">{posted}</span>
    {:else if note && phase === 'connected'}<span class="hint">{note}</span>
    {:else}<span class="hint">{hintText(human)}</span>{/if}
  </div>
</div>

<style>
  .viewer { flex: 1; display: flex; flex-direction: column; min-height: 0; padding: 14px 20px 12px; gap: 12px; }
  .sp { flex: 1; }
  .keyboard-input { position: fixed; left: 0; bottom: 0; width: 1px; height: 1px; padding: 0; border: 0; opacity: 0; font-size: 16px; pointer-events: none; }
  .keyboard { flex: none; }

  .top { display: flex; align-items: center; gap: 14px; min-height: 36px; }
  .state { display: inline-flex; align-items: center; gap: 8px; white-space: nowrap; }
  .state.gold { color: var(--gold); }
  .dot { width: 7px; height: 7px; border-radius: 50%; background: var(--faint); flex: none; box-shadow: 0 0 0 3px transparent; }
  .dot.live, .dot.gold { background: var(--gold); box-shadow: 0 0 0 3px rgba(230, 192, 123, 0.14); }
  .pill { border: 1px solid rgba(236, 233, 228, 0.28); border-radius: 18px; padding: 6px 16px; font-size: 13px; color: var(--ink); background: none; white-space: nowrap; transition: background-color var(--motion-fast), border-color var(--motion-fast); }
  .pill:not(:disabled) { transition: background-color var(--motion-fast) var(--ease), border-color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .pill:hover:not(:disabled) { background: var(--hover); border-color: rgba(236, 233, 228, 0.45); }
  }
  .pill:active:not(:disabled) { background: var(--select); }
  .pill:disabled { opacity: 0.45; cursor: default; }
  .pill.gold { border-color: var(--gold); color: var(--gold); transition: background-color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .pill.gold:hover { background: var(--hover); }
  }
  .pill.gold:active { background: var(--select); }
  .round { width: 32px; height: 32px; border-radius: 50%; border: 1px solid var(--hair-strong); color: var(--muted); font-size: 16px; line-height: 1; display: grid; place-items: center; transition: background-color var(--motion-fast) var(--ease), color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .round:hover { background: var(--hover); color: var(--ink); }
  }
  .round:active { background: var(--select); }
  .round[aria-expanded='true'] { background: var(--select); color: var(--ink); }
  .more { position: relative; }
  .menu { position: absolute; right: 0; top: 40px; z-index: 20; min-width: 230px; background: var(--raise); border: 1px solid var(--line); border-radius: var(--r-sm); padding: 6px; box-shadow: 0 14px 40px rgba(0, 0, 0, 0.5); }
  .menu button { width: 100%; display: flex; justify-content: space-between; align-items: center; gap: 16px; text-align: left; padding: 8px 10px; border-radius: 7px; font-size: 13px; color: var(--ink-2); }
  .menu button:not(:disabled) { transition: background-color var(--motion-fast) var(--ease), color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .menu button:hover:not(:disabled) { background: var(--raise-hover); color: var(--ink); }
  }
  .menu button:active:not(:disabled) { background: var(--select); }
  .menu button:disabled { opacity: 0.45; cursor: default; }
  .menu button span { font: 11px var(--mono); color: var(--faint); }
  .menu hr { border: 0; border-top: 1px solid var(--line-soft); margin: 5px 4px; }
  .statline { display: flex; justify-content: space-between; gap: 12px; padding: 6px 10px 4px; font-size: 12px; color: var(--muted); }
  .statline i { font-style: normal; }
  .statline span { font: 11px var(--mono); color: var(--faint); text-align: right; }
  /* Top-aligned: the frame sits right under the top line, with no gap above it. */
  /* The bottom gap holds the floating chat while the owner drives. */
  .stage { position: relative; flex: 1; min-height: 0; display: grid; place-items: start center; container-type: size; padding-bottom: 54px; }
  .frame { position: relative; width: min(100cqw, 100cqh * var(--r)); aspect-ratio: var(--r); }
  /* The largest box of the screen's shape that fits the stage. The frame is
     hairline and soft; driving is a gold hairline and a quiet glow. Nothing
     is ever drawn over the stream. */
  .screen { position: absolute; inset: 0; border-radius: var(--r-lg); overflow: hidden; border: 1px solid var(--hair); background: #0d0d0f; box-shadow: 0 24px 60px rgba(0, 0, 0, 0.45); touch-action: pan-x pan-y pinch-zoom; transition: border-color var(--motion-slow), box-shadow var(--motion-slow); }
  .screen.control { border: 1.5px solid var(--gold); box-shadow: 0 0 0 8px rgba(230, 192, 123, 0.08), 0 24px 60px rgba(0, 0, 0, 0.45); }
  .rfb { position: absolute; inset: 0; transform-origin: 0 0; transition: opacity var(--motion-slow) var(--ease); }
  /* noVNC's canvas stays on top for pointer events, but invisible. */
  .rfb.hide { opacity: 0; }
  .present { position: absolute; inset: 0; width: 100%; height: 100%; display: none; pointer-events: none; }
  .present.on { display: block; }
  .still { position: absolute; inset: 0; transition: opacity var(--motion-slow) var(--ease); }
  .still.gone { opacity: 0; pointer-events: none; }
  .badge { position: absolute; left: 12px; top: 12px; z-index: 2; display: inline-flex; align-items: center; gap: 6px; background: rgba(20, 19, 22, 0.82); color: var(--ink-2); font-size: 12px; padding: 3px 11px; border-radius: 12px; pointer-events: none; animation: fadein var(--motion) var(--ease); }
  .badge.ok { color: var(--gold); animation: fadeout var(--motion-slow) var(--ease) forwards; }
  .badge.bad { color: var(--coral); pointer-events: auto; }
  .badge .sep { opacity: 0.5; }
  .retry { color: var(--ink); text-decoration: underline; font-size: 12px; }
  .spin { width: 9px; height: 9px; border-radius: 50%; border: 2px solid var(--faint); border-top-color: var(--gold); animation: spin 0.8s linear infinite; }
  @keyframes spin { to { transform: rotate(360deg); } }
  @keyframes fadein { from { opacity: 0; } }
  @keyframes fadeout { 0%, 70% { opacity: 1; } 100% { opacity: 0; } }
  .foot { display: flex; gap: 14px; align-items: baseline; font-size: 12px; min-width: 0; flex-wrap: nowrap; }
  .foot .now { font: 11.5px var(--mono); color: var(--faint); min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .foot .hint { color: var(--faint); white-space: nowrap; flex: none; }
  .foot .err { color: var(--coral); flex: none; }
  .foot .okmsg { color: var(--sage); flex: none; }
  @media (max-width: 760px) {
    .viewer { padding: 8px 10px; gap: 8px; }
    .top { gap: 8px; }
    .pill { padding-inline: 10px; }
    .foot .hint { display: none; }
    .nophone { display: none; }
  }
</style>
