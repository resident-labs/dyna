<script lang="ts">
  import { onMount } from 'svelte'
  import { pair, connect } from '../../api.ts'
  import { signIn } from './link.ts'
  // The web app's first screen on a new device. The owner runs
  // `dyna engine run --pair` on the computer and types the code here. The
  // server then sets this browser's device cookie.
  // A pairing link or QR code carries the code in #pair=CODE. Take it and
  // drop it from the address bar and history.
  function linkCode() {
    const h = new URLSearchParams(location.hash.slice(1))
    const c = h.get('pair') ?? ''
    if (c) history.replaceState(null, '', location.pathname + location.search)
    return /^[A-Za-z0-9-]{1,32}$/.test(c) ? c : ''
  }
  let code = $state(linkCode())
  let name = $state(guessName())
  let busy = $state(false)
  let error = $state('')

  onMount(() => { if (code) void submit() })

  function guessName() {
    const ua = navigator.userAgent
    if (/wails.io/.test(ua)) return 'Dyna desktop'
    if (/iPhone/.test(ua)) return 'iPhone'
    if (/iPad/.test(ua)) return 'iPad'
    if (/Android/.test(ua)) return /Mobile/.test(ua) ? 'Android phone' : 'Android tablet'
    if (/Firefox\//.test(ua)) return 'Firefox'
    if (/Edg\//.test(ua)) return 'Edge'
    if (/Chrome\//.test(ua)) return 'Chrome'
    if (/Safari\//.test(ua)) return 'Safari'
    return 'browser'
  }

  // The Connect button leaves for the relay; the relay sends us back.
  async function connectIn() {
    if (busy) return
    busy = true
    error = ''
    try {
      await signIn()
    } catch (err) {
      error = String(err)
      busy = false
    }
  }

  async function submit(e?: Event) {
    e?.preventDefault()
    if (busy || !code.trim()) return
    busy = true
    error = ''
    try {
      await pair(code, name)
      location.reload()
    } catch (err) {
      error = String(err)
      busy = false
    }
  }
</script>

<main class="pair">
  <form onsubmit={submit}>
    <svg class="logo" viewBox="0 0 24 24" width="40" height="40" aria-hidden="true"><path d="M5 4h6.2c4.6 0 7.8 3.2 7.8 8s-3.2 8-7.8 8H5z" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linejoin="round" /></svg>
    <h1>{connect?.linked ? 'Sign in' : 'Pair this device'}</h1>
    {#if connect?.linked}
      <button type="button" class="btn fill lg" disabled={busy} onclick={connectIn}>Sign in with Resident Labs</button>
      <p class="or">Or pair this device with a code.</p>
    {/if}
    {#if code}
      <p>The pairing link filled in the code.</p>
    {:else}
      <p>On the computer that runs Dyna, open Settings, then Devices, then Pair a device. Or run this command. Then type the code it shows.</p>
      <pre>dyna engine run --pair</pre>
    {/if}
    <label>
      <span>Pairing code</span>
      <input bind:value={code} placeholder="XXXXX-XXXXX" autocomplete="one-time-code" autocapitalize="characters" autocorrect="off" spellcheck="false" maxlength="16" required />
    </label>
    <label>
      <span>Device name</span>
      <input bind:value={name} maxlength="40" autocomplete="off" />
    </label>
    {#if error}<p class="err" role="alert">{error}</p>{/if}
    <button type="submit" class="btn fill lg" disabled={busy || !code.trim()}>{busy ? 'Pairing…' : 'Pair'}</button>
    <p class="note">A paired device keeps the scopes on its pairing code for 30 days. A code with no extra scopes can read and post. Remove it in Settings, under Devices, or with <code>dyna engine run --revoke</code>.</p>
  </form>
</main>

<style>
  .pair { min-height: 100dvh; display: grid; place-items: center; padding: var(--s5) var(--s4) calc(var(--s5) + env(safe-area-inset-bottom)); background: var(--main); }
  form { width: min(380px, 100%); display: flex; flex-direction: column; gap: 14px; }
  .logo { color: var(--iris); }
  h1 { margin: 0; font-size: var(--t-display); font-weight: 700; }
  p { margin: 0; color: var(--muted); font-size: var(--t-body); line-height: 1.45; }
  pre { margin: 0; padding: 10px var(--s3); background: var(--field); border-radius: 8px; font-family: var(--mono); font-size: var(--t-body); color: var(--ink-2); overflow-x: auto; }
  label { display: flex; flex-direction: column; gap: 6px; }
  label span { font-size: var(--t-sm); font-weight: 600; color: var(--muted); text-transform: uppercase; letter-spacing: 0.03em; }
  input { font: inherit; font-size: var(--t-title); color: var(--ink); background: var(--field); border: 1px solid transparent; border-radius: 8px; padding: 0 var(--s3); height: 46px; outline: none; }
  input:focus { border-color: #3d3b40; }
  input[autocomplete='one-time-code'] { font-family: var(--mono); letter-spacing: 0.08em; text-transform: uppercase; }
  /* The pairing page's one action fills the column. */
  .btn { height: 46px; font-size: var(--t-lead); }
  .err { color: var(--coral); }
  .or { text-align: center; }
  .note { font-size: var(--t-sm); color: var(--faint); }
  code { font-family: var(--mono); font-size: var(--t-sm); }
</style>
