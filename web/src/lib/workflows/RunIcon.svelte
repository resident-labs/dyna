<script lang="ts">
  // One status glyph for runs and agents: a turning ring while running, a
  // check, a cross, or a quiet dot. Color follows the app: gold live, sage
  // done, coral failed.
  let { status = '', size = 18 }: { status?: string; size?: number } = $props()
  let kind = $derived(status === 'running' ? 'run' : status === 'ok' ? 'ok' : status === 'error' || status === 'failed' ? 'bad' : status === 'canceled' || status === 'stopped' ? 'stop' : 'idle')
</script>

<span class="ic {kind}" style="--s:{size}px" aria-hidden="true">
  {#if kind === 'run'}
    <svg viewBox="0 0 20 20"><circle cx="10" cy="10" r="7.5" fill="none" stroke="currentColor" stroke-opacity="0.22" stroke-width="2.4" /><path d="M10 2.5a7.5 7.5 0 0 1 7.5 7.5" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round" /></svg>
  {:else if kind === 'ok'}
    <svg viewBox="0 0 20 20"><circle cx="10" cy="10" r="9" fill="currentColor" fill-opacity="0.16" /><path d="M6 10.4l2.6 2.6L14 7.6" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" /></svg>
  {:else if kind === 'bad'}
    <svg viewBox="0 0 20 20"><circle cx="10" cy="10" r="9" fill="currentColor" fill-opacity="0.16" /><path d="M7 7l6 6M13 7l-6 6" stroke="currentColor" stroke-width="2" stroke-linecap="round" /></svg>
  {:else if kind === 'stop'}
    <svg viewBox="0 0 20 20"><circle cx="10" cy="10" r="9" fill="currentColor" fill-opacity="0.14" /><rect x="7" y="7" width="6" height="6" rx="1.2" fill="currentColor" /></svg>
  {:else}
    <svg viewBox="0 0 20 20"><circle cx="10" cy="10" r="4" fill="currentColor" /></svg>
  {/if}
</span>

<style>
  .ic { width: var(--s); height: var(--s); display: inline-grid; place-items: center; flex: none; }
  svg { width: 100%; height: 100%; }
  .run { color: var(--gold); }
  .run svg { animation: turn 1s linear infinite; }
  .ok { color: var(--sage); }
  .bad { color: var(--coral); }
  .stop, .idle { color: var(--faint); }
  @keyframes turn { to { transform: rotate(360deg); } }
</style>
