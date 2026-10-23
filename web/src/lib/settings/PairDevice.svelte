<script lang="ts">
  import { dialog } from '../ui/dialog.ts'
  // Pair a new phone or browser: a one-time code, and a QR code of the web
  // app link that carries it (#pair=CODE). The QR is drawn here, so the link
  // never leaves the page. The web app address is editable, for a
  // Tailscale or Cloudflare name, or another port.
  import { onMount, onDestroy } from 'svelte'
  import { api, copyText } from '../../api.ts'
  import { qrPath } from './qr.ts'
  import { errorText } from '../errorText.ts'

  // The one-time pairing the engine makes: its link and when it ends.
  interface Pairing {
    url: string
    expires: string
    code?: string
  }
  let { onclose }: { onclose: () => void } = $props()

  // In a browser the web app is this page. On the desktop the server picks
  // (the Connect origin, or the local serve address) until the owner types one.
  let origin = $state(location.origin)
  let pairing = $state<Pairing | null>(null)
  let error = $state('')
  let busy = $state(false)
  let now = $state(Date.now())
  let copied = $state(false)

  let qr = $derived(pairing?.url ? qrPath(pairing.url) : null)
  let left = $derived(pairing ? Math.max(0, Math.round((Date.parse(pairing.expires) - now) / 1000)) : 0)
  let expired = $derived(!!pairing && left === 0)

  async function make() {
    busy = true
    error = ''
    try {
      const made: Pairing = await api.createPairing(origin.trim().replace(/\/+$/, ''))
      pairing = made
      if (!origin) origin = new URL(made.url).origin
    } catch (e) {
      error = errorText(e)
    } finally {
      busy = false
    }
  }

  const tick = setInterval(() => (now = Date.now()), 1000)
  onDestroy(() => clearInterval(tick))
  onMount(() => { void make() })

  // Update makes a new code with the address as typed.
  const update = () => void make()

  async function copy() {
    if (!pairing) return
    await copyText(pairing.url)
    copied = true
    setTimeout(() => (copied = false), 1400)
  }

  const mmss = (s: number) => `${Math.floor(s / 60)}:${String(s % 60).padStart(2, '0')}`
</script>

<!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
<div class="backdrop">
  <!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
  <div class="card" use:dialog={{ close: onclose }} tabindex="-1" role="dialog" aria-modal="true" aria-label="Pair a device" onclick={(e) => e.stopPropagation()}>
    <button class="x" onclick={onclose} aria-label="Close">
      <svg viewBox="0 0 16 16" width="14" height="14" aria-hidden="true"><path d="M4 4l8 8M12 4l-8 8" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" /></svg>
    </button>
    <h2>Pair a device</h2>
    <p class="lede">Scan the code with the phone's camera. Or open the web app on the device and type the pairing code.</p>
    <div class="pairbox">
      <div class="qr" class:dim={expired || busy}>
        {#if qr}
          <svg viewBox="-3 -3 {qr.size + 6} {qr.size + 6}" role="img" aria-label="QR code for the pairing link" shape-rendering="crispEdges">
            <rect x="-3" y="-3" width={qr.size + 6} height={qr.size + 6} fill="#fff" rx="2" />
            <path d={qr.d} fill="#151416" />
          </svg>
        {:else}
          <div class="ph">{error ? '—' : 'Making a code…'}</div>
        {/if}
        {#if expired}<button class="btn fill lg again" onclick={make}>Make a new code</button>{/if}
      </div>
      <div class="side">
        <span class="lab">Pairing code</span>
        <span class="code mono" class:dead={expired}>{pairing?.code ?? '·····'}</span>
        <span class="exp" class:soon={left > 0 && left < 60}>
          {#if !pairing}&nbsp;{:else if expired}Expired{:else}Works once, for {mmss(left)}{/if}
        </span>
        {#if pairing && !expired}
          <button class="btn" onclick={copy}>{copied ? 'Copied' : 'Copy link'}</button>
        {/if}
      </div>
    </div>

    <label class="field">
      <span class="lab">Web app address</span>
      <span class="row">
        <input bind:value={origin} placeholder="https://dyna.example.com" class="mono" autocomplete="off" autocapitalize="off" spellcheck="false" onkeydown={(e) => e.key === 'Enter' && update()} />
        <button class="btn" onclick={update} disabled={busy}>{busy ? '…' : 'Update'}</button>
      </span>
      <span class="hint">The phone opens this address. Use the name it reaches the computer by, such as a Tailscale or Cloudflare address.</span>
    </label>
    {#if error}<p class="err">{error}</p>{/if}
    <p class="note">A paired device acts as you, with the same power as a shell on this computer. Revoke it here at any time.</p>
  </div>
</div>

<style>
  .backdrop { position: fixed; inset: 0; background: rgba(8, 8, 10, 0.62); display: grid; place-items: center; z-index: 60; padding: var(--s6); }
  .card { position: relative; width: min(520px, 100%); max-height: 100%; overflow-y: auto; background: var(--side); border-radius: 12px; box-shadow: 0 24px 64px rgba(0, 0, 0, 0.5); padding: 20px; outline: none; }
  .x { position: absolute; top: 14px; right: 14px; width: 30px; height: 30px; border-radius: 50%; display: grid; place-items: center; color: var(--muted); transition: background-color var(--motion-fast) var(--ease), color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .x:hover { background: var(--hover); color: var(--ink); }
  }
  .x:active { background: var(--select); }
  h2 { margin: 0 0 var(--s1); font-size: var(--t-head); font-weight: 600; }
  .lede { margin: 0 0 var(--s4); color: var(--muted); font-size: var(--t-body); padding-right: var(--s5); }
  .pairbox { display: flex; gap: 20px; align-items: center; background: var(--main); border: 1px solid var(--line-soft); border-radius: 12px; padding: var(--s4); }
  .qr { position: relative; width: 176px; height: 176px; flex: none; }
  .qr svg { width: 100%; height: 100%; display: block; border-radius: 8px; }
  .qr.dim svg { opacity: 0.18; }
  .ph { width: 100%; height: 100%; border-radius: 8px; background: var(--raise); display: grid; place-items: center; color: var(--faint); font-size: var(--t-ui); }
  .again { position: absolute; inset: 0; margin: auto; width: max-content; height: max-content; }
  .side { display: flex; flex-direction: column; gap: 6px; min-width: 0; align-items: flex-start; }
  .lab { font-size: var(--t-sm); font-weight: 700; color: var(--muted); text-transform: uppercase; letter-spacing: 0.06em; }
  .code { font-size: var(--t-display); font-weight: 600; letter-spacing: 0.08em; color: var(--ink); overflow-wrap: anywhere; }
  .code.dead { color: var(--faint); text-decoration: line-through; }
  .exp { font-size: var(--t-ui); color: var(--muted); font-variant-numeric: tabular-nums; }
  .exp.soon { color: var(--gold); }
  .side .btn { margin-top: 6px; }
  .field { display: flex; flex-direction: column; gap: 6px; margin-top: var(--s4); }
  .row { display: flex; gap: var(--s2); }
  input { flex: 1; min-width: 0; background: var(--rail); border: 1px solid var(--line); border-radius: 7px; color: var(--ink); font-size: var(--t-body); padding: 7px 10px; outline: none; }
  input:focus { border-color: var(--iris-edge); }
  .hint { font-size: var(--t-sm); color: var(--faint); line-height: 1.45; }
  .err { color: var(--coral); font-size: var(--t-body); margin: 10px 0 0; }
  .note { margin: 14px 0 0; font-size: var(--t-sm); color: var(--faint); }
  @media (max-width: 760px) {
    .backdrop { padding: 0; align-items: end; }
    .card { width: 100%; border-radius: 14px 14px 0 0; padding-bottom: calc(20px + env(safe-area-inset-bottom)); max-height: 94dvh; }
    .x { width: 44px; height: 44px; top: 8px; right: 8px; }
    .pairbox { flex-direction: column; text-align: center; }
    .side { align-items: center; }
    .qr { width: 200px; height: 200px; }
    input { font-size: var(--t-title); }
    .btn { min-height: 44px; padding: 0 var(--s4); }
  }
</style>
