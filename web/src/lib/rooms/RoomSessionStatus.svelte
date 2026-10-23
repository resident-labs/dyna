<script>
  import { roomControlLabels, roomControlResult } from './roomSessionControls.ts'
  let { value, members = [], onresume, onreload } = $props()
  let state = $derived(value?.state)
  let accepted = $derived(value?.results.filter((r) => r.ok).length ?? 0)
  let refused = $derived(value?.results.filter((r) => !r.ok).length ?? 0)
  const name = (id) => members.find((m) => m.id === id)?.address ?? id
</script>

{#if state?.paused}
  <div class="pause-status" role="status"><strong>Sessions paused</strong><button disabled={value.busy} onclick={onresume}>Resume all</button><p>New turns and post delivery wait. Running turns finish naturally.</p><p>{state.running} running · {state.held} held</p><p>Workflows and shell jobs continue.</p></div>
{/if}
{#if value?.busy}<p class="status" role="status">{roomControlLabels[value.action]} · Working…</p>{/if}
{#if value?.error || value?.statusError}<p class="error" role="alert">{value.error || value.statusError} <button disabled={value.busy || value.loading} onclick={onreload}>Refresh status</button></p>{/if}
{#if value?.action && !value.busy && !value.error && !value.results.length}<p class="status" role="status">{value.action === 'clearDormant' ? 'Closed 0' : `${roomControlLabels[value.action]} · No sessions needed a change.`}</p>{/if}
{#if value?.results.length}
  <details class="results" open={refused > 0}>
    <summary>{value.action === 'clearDormant' ? `Closed ${accepted}${refused ? ` · Kept ${refused}` : ''}` : `${roomControlLabels[value.action]} · ${accepted} accepted${refused ? ` · ${refused} refused` : ''}`}</summary>
    <ul>{#each value.results as result, i (`${i}:${result.session}`)}<li class:error={!result.ok}><strong>{name(result.session)}</strong><span>{roomControlResult(result, value.action)}</span></li>{/each}</ul>
  </details>
{/if}

<style>
  .pause-status, .status, .error, .results { margin: var(--s2); font-size: var(--t-sm); line-height: var(--leading); overflow-wrap: anywhere; }
  .pause-status { color: var(--muted); }
  .pause-status strong { color: var(--iris); font-weight: 600; }
  button { color: var(--iris); font-size: inherit; }
  .pause-status button { float: right; }
  button:disabled { opacity: .55; }
  p { margin: var(--s1) 0; }
  .status { color: var(--iris); }
  .error { color: var(--coral); }
  .results { color: var(--muted); }
  summary { cursor: pointer; }
  ul { padding: 0; margin: 6px 0; list-style: none; }
  li { display: grid; gap: 2px; margin: var(--s2) 0; }
  li strong { color: var(--ink-2); font-weight: 500; }
</style>
