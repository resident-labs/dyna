<script>
  import { roomControlLabels } from './roomSessionControls.ts'
  let { value, onresume, onreload } = $props()
  let state = $derived(value?.state)
</script>

{#if state?.paused}
  <div class="pause-status" role="status"><strong>Sessions paused</strong><button disabled={value.busy} onclick={onresume}>Resume all</button><p>New turns and post delivery wait. Running turns finish naturally.</p><p>{state.running} running · {state.held} held</p><p>Workflows and shell jobs continue.</p></div>
{/if}
{#if value?.busy}<p class="status" role="status">{roomControlLabels[value.action]} · Working…</p>{/if}
{#if value?.error || value?.statusError}<p class="error" role="alert">{value.error || value.statusError} <button disabled={value.busy || value.loading} onclick={onreload}>Refresh status</button></p>{/if}

<style>
  .pause-status, .status, .error { margin: var(--s2); font-size: var(--t-sm); line-height: var(--leading); overflow-wrap: anywhere; }
  .pause-status { color: var(--muted); }
  .pause-status strong { color: var(--iris); font-weight: 600; }
  button { color: var(--iris); font-size: inherit; }
  .pause-status button { float: right; }
  button:disabled { opacity: .55; }
  p { margin: var(--s1) 0; }
  .status { color: var(--iris); }
  .error { color: var(--coral); }
</style>
