<script module lang="ts">
  // available says whether this build has a terminal renderer. While it is
  // false the app hides every way into the terminal.
  export const available = true
</script>

<script lang="ts">
  // One live terminal, drawn by libghostty-vt on a canvas (see ghostty/).
  // It attaches to the shell on the host, replays the scrollback, then
  // streams output; keys go back as base64 bytes. Unmounting detaches but
  // never stops the shell, so a reload or a tab switch reattaches where it was.
  import { onMount } from 'svelte'
  import { api, openExternal } from '../../api.ts'
  import { errorText } from '../errorText.ts'
  import type { TermEvent } from '../api/terminal.ts'
  import type { GhosttyTerminalSurface } from './ghostty/surface.ts'
  import { terminalFocusHint, terminalFocusOnOpen, terminalReady } from './terminalFocus.ts'

  // autofocus null follows the layout: a wide pane takes focus, a phone pane
  // leaves it so the soft keyboard stays down.
  let { id, narrow = false, onexit = () => {}, autofocus = null, ctrl = $bindable(false) }: {
    id: string
    narrow?: boolean
    onexit?: (code: number) => void
    autofocus?: boolean | null
    ctrl?: boolean
  } = $props()

  let host = $state<HTMLElement>()
  let failed = $state('')
  let term = $state<GhosttyTerminalSurface | null>(null)
  // The attach stream reports a missing terminal or a refused attachment later,
  // as an exit event, so readiness follows both flags instead of one assignment.
  let attached = $state(false)
  let exited = $state(false)
  let focused = $state(false)
  const ready = $derived(terminalReady({ attached, failed: failed !== '', exited }))
  const takesFocus = $derived(terminalFocusOnOpen(autofocus, narrow))
  const showHint = $derived(terminalFocusHint({ ready, focused }))

  const enc = new TextEncoder()
  const toB64 = (bytes: Uint8Array) => {
    let s = ''
    for (let i = 0; i < bytes.length; i += 0x8000) s += String.fromCharCode(...bytes.subarray(i, i + 0x8000))
    return btoa(s)
  }
  const fromB64 = (b64: string) => Uint8Array.from(atob(b64), (c) => c.charCodeAt(0))

  // send writes text to the shell. A held Ctrl from the phone key row turns
  // one letter into its control character.
  export function send(text: string) {
    if (ctrl && text.length === 1) {
      const c = text.toUpperCase().charCodeAt(0)
      if (c >= 64 && c <= 95) text = String.fromCharCode(c - 64)
      ctrl = false
    }
    api.termWrite(id, toB64(enc.encode(text))).catch((e: unknown) => (failed = errorText(e)))
  }

  // key sends a named key, using the shell's cursor mode for arrows.
  export function key(name: string) {
    const app = term?.isApplicationCursorKeys()
    const arrows: Record<string, string> = { up: 'A', down: 'B', right: 'C', left: 'D' }
    const arrow = arrows[name]
    if (arrow) send(`\x1b${app ? 'O' : '['}${arrow}`)
    else send(({ esc: '\x1b', tab: '\t' } as Record<string, string>)[name] ?? '')
    term?.focus()
  }

  export function focus() {
    term?.focus()
  }

  // rgb reads a #rrggbb app color token as the {r, g, b} libghostty wants.
  const rgb = (hex: string) => {
    const m = /^#?([0-9a-f]{6})$/i.exec(hex.trim())
    const n = m ? parseInt(m[1] ?? '', 16) : 0
    return { r: (n >> 16) & 255, g: (n >> 8) & 255, b: n & 255 }
  }

  function theme() {
    const css = getComputedStyle(document.documentElement)
    const v = (n: string) => css.getPropertyValue(n).trim()
    // ANSI order: black, red, green, yellow, blue, magenta, cyan, white,
    // then the bright set.
    const palette = [
      '#2b2a2d', v('--coral'), v('--sage'), v('--gold'), v('--hue-4'), v('--hue-5'), v('--hue-1'), v('--ink-2'),
      '#6c675f', '#f4a59c', '#b4dcb2', '#f0d39c', '#bccbff', '#e6c6f7', '#b0d9ee', v('--ink'),
    ]
    return {
      background: rgb('#19181a'),
      foreground: rgb(v('--ink-2')),
      cursor: rgb(v('--iris')),
      palette: palette.map(rgb),
      selectionBackground: 'rgba(183, 168, 255, 0.28)',
    }
  }

  // Canvas font strings reject CSS keywords like ui-monospace, so pass only
  // the named faces; the surface adds its own monospace fallbacks.
  function font() {
    const mono = getComputedStyle(document.documentElement).getPropertyValue('--mono')
    const family = mono.split(',').map((f) => f.trim()).filter((f) => f && !/^(ui-)?monospace$/.test(f)).join(', ')
    return { family, size: narrow ? 12 : 13 }
  }

  // The phone layout uses a smaller font. Apply it when narrow changes.
  let fontSize = 0
  $effect(() => {
    const f = font()
    if (term && f.size !== fontSize) term.setFont(f)
    if (term) fontSize = f.size
  })

  onMount(() => {
    let gone = false
    let detach: () => void = () => {}
    let dropFocusListeners: () => void = () => {}
    ;(async () => {
      try {
        // The WASM runtime and the symbol font load only when a terminal opens.
        const { GhosttyTerminalSurface: Surface } = await import('./ghostty/surface.ts')
        if (gone || !host) return
        const made = await Surface.create(host, {
          theme: theme(),
          font: font(),
          // Typed keys, pastes and terminal replies (such as device attribute
          // answers) all go back to the shell.
          onData: (d) => send(d),
          onResize: (cols, rows) => api.termResize(id, cols, rows).catch(() => {}),
          onSelectionChange: () => {},
          beforeKey: () => true,
          onLinkActivate: (text) => openExternal(text),
          canActivateLink: (text) => /^https?:\/\//i.test(text),
        })
        if (gone) {
          made.dispose()
          return
        }
        term = made
        // The surface types into a hidden textarea, so its focus is the pane's
        // focus: when it blurs, this pane shows the hint instead of going quiet.
        const input = made.input
        const onFocus = () => (focused = true)
        const onBlur = () => (focused = false)
        input.addEventListener('focus', onFocus)
        input.addEventListener('blur', onBlur)
        dropFocusListeners = () => {
          input.removeEventListener('focus', onFocus)
          input.removeEventListener('blur', onBlur)
        }
        // The first event replays the history. resetAndWrite keeps the
        // terminal from answering old queries in it (such as nvim's device
        // attribute requests), which would reach the shell as typed text.
        let replayed = false
        detach = await api.termAttach(id, (ev: TermEvent) => {
          const data = ev.data ? fromB64(ev.data) : new Uint8Array()
          if (ev.reset || !replayed) made.resetAndWrite(data)
          else if (data.length) made.write(data)
          replayed = true
          // A terminal that is gone, or an attachment this host refused, shows
          // up here; typing then goes nowhere, so drop readiness at once.
          if (ev.exited) {
            exited = true
            onexit(ev.exitCode ?? 0)
          }
        })
        if (gone) detach()
        attached = true
      } catch (e) {
        // A shell that is gone (the host restarted, or it closed elsewhere)
        // shows as exited; the tab offers a new one.
        const msg = errorText(e)
        if (/no such terminal|not found/i.test(msg)) onexit(-1)
        else failed = msg
      }
      // Focus first, so a pane that takes focus never flashes the hint. A pane
      // whose surface or attach failed has nothing to type into, so no hint.
      if (!gone && takesFocus && term) term.focus()
    })()
    return () => {
      gone = true
      detach?.()
      dropFocusListeners()
      term?.dispose()
      term = null
      attached = false
      exited = false
      focused = false
    }
  })
</script>

<div class="pane">
  {#if failed}<p class="err">{failed}</p>{/if}
  <div class="term-frame">
    <div class="term-host" bind:this={host}></div>
    {#if showHint}<p class="type-hint" role="status">Click to type</p>{/if}
  </div>
</div>

<style>
  .pane { flex: 1; display: flex; flex-direction: column; min-height: 0; min-width: 0; background: #19181a; }
  .term-frame { position: relative; flex: 1; display: flex; min-height: 0; min-width: 0; padding: var(--s1) 0 0 var(--s2); }
  /* The hint never takes the click: it points at the canvas under it, whose
     pointerdown already focuses the terminal. */
  .type-hint { position: absolute; left: 50%; bottom: 12px; z-index: 1; margin: 0; transform: translateX(-50%); padding: 3px 10px; border-radius: 999px; background: rgba(236, 233, 228, 0.14); color: var(--ink-2); font-size: var(--t-sm); pointer-events: none; }
  .term-host { position: relative; flex: 1; min-height: 0; min-width: 0; overflow: hidden; }
  .term-host :global(.ghostty-canvas) { display: block; width: 100%; height: 100%; cursor: text; }
  .term-host :global(.ghostty-scrollbar) { position: absolute; top: 4px; right: 1px; bottom: 4px; z-index: 1; width: 8px; cursor: default; touch-action: none; }
  .term-host :global(.ghostty-scrollbar-thumb) { position: absolute; left: 1px; right: 1px; top: 0; border-radius: 3px; background: rgba(236, 233, 228, 0.16); transition: background-color var(--motion-fast) ease-out; }
  .term-host :global(.ghostty-scrollbar:focus-visible .ghostty-scrollbar-thumb) { background: rgba(236, 233, 228, 0.3); }
  .term-host :global(.ghostty-scrollbar .ghostty-scrollbar-thumb) { transition: background-color var(--motion-fast) var(--ease); }
  .term-host :global(.ghostty-scrollbar:hover .ghostty-scrollbar-thumb) { background: var(--scrollbar-active); }
  .err { margin: 0; padding: 6px var(--s3); font-size: var(--t-sm); color: var(--coral); background: rgba(239, 138, 128, 0.08); }
  @media (max-width: 760px) {
    .term-frame { padding: 2px 0 0 2px; }
  }
</style>
