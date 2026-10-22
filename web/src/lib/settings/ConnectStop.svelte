<script lang="ts">
  // A full screen that stops the app. A key mismatch has no way past it: the
  // machine key does not match the one Resident Labs Connect gave. Any other
  // error offers a way back to the sign-in screen.
  let { key = false, error = '' }: { key?: boolean; error?: string } = $props()
</script>

<main class="stop">
  <div>
    <svg class="logo" viewBox="0 0 24 24" width="40" height="40" aria-hidden="true"><path d="M5 4h6.2c4.6 0 7.8 3.2 7.8 8s-3.2 8-7.8 8H5z" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linejoin="round" /></svg>
    {#if key}
      <h1>Stop</h1>
      <p role="alert">This machine's key does not match the one Resident Labs Connect gave. Do not continue.</p>
    {:else}
      <h1>Sign-in failed</h1>
      <p class="err" role="alert">{error}</p>
      <a href="/">Back to sign in</a>
    {/if}
  </div>
</main>

<style>
  .stop { min-height: 100dvh; display: grid; place-items: center; padding: var(--s5) var(--s4); background: var(--main); }
  div { width: min(380px, 100%); display: flex; flex-direction: column; gap: 14px; }
  .logo { color: var(--coral); }
  h1 { margin: 0; font-size: var(--t-display); font-weight: 700; }
  p { margin: 0; color: var(--ink-2); font-size: var(--t-lead); line-height: 1.45; }
  .err { color: var(--coral); }
  a { color: var(--iris); font-weight: 600; }
</style>
