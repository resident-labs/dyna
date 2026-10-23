<script lang="ts">
  // The row for a detached shell's result: what the command was, how it ended,
  // its exit code when it failed, and how long it ran. Its output sits behind
  // the row's own Output toggle, collapsed, so the feed shows facts. It reads
  // the typed payload, never the post text, and keeps the job's opaque ID out
  // of everything it shows.
  import type { EventNotice } from '../api/room.ts'
  import type { EventPost, EventSurface } from './eventRenderers.ts'
  import { shellEvent } from './eventPayloads.ts'
  import type { ShellState } from './events.gen.ts'
  import EventDetails from './EventDetails.svelte'
  import { clock } from '../ui/format.ts'

  let { post, notice = post.notice, surface = 'feed' }: { post: EventPost; notice?: EventNotice; surface?: EventSurface } = $props()

  const stateWords: Record<ShellState, string> = { completed: 'done', failed: 'failed', stopped: 'stopped' }
  const stateTone: Record<ShellState, string> = { completed: 'sage', failed: 'coral', stopped: 'gold' }

  let payload = $derived(shellEvent(post))
  let jobState = $derived<ShellState>(payload?.state ?? 'completed')
  let output = $derived(payload?.output ?? [])
  let open = $state(false)
  // A failed job opens by itself: its output is why the reader is here.
  let shown = $derived(open || jobState === 'failed')
  // A run under a second reads in milliseconds, so a quick command does not
  // look like a clock that failed to move.
  let elapsed = $derived.by(() => {
    const ms = payload?.durationMs ?? 0
    if (ms <= 0) return ''
    return ms < 1000 ? `${ms} ms` : `${(ms / 1000).toFixed(1)} s`
  })
</script>

<div class="row {surface}" data-id={post.id}>
  <div class="sum">
    <span class="glyph" aria-hidden="true">$_</span>
    <span class="label" title={payload?.label ?? 'Shell command'}>{payload?.label ?? 'Shell command'}</span>
    <span class="state {stateTone[jobState]}">{stateWords[jobState]}</span>
    {#if (payload?.exitCode ?? 0) !== 0}<span class="exit" title="Exit code">{payload?.exitCode}</span>{/if}
    {#if elapsed}<span class="fact">{elapsed}</span>{/if}
    {#if output.length}
      <button class="btn" type="button" aria-expanded={shown} onclick={() => (open = !open)}>{shown ? 'Hide output' : 'Output'}</button>
    {/if}
    <time title={new Date(post.ts ?? 0).toLocaleString()}>{clock(post.ts)}</time>
  </div>
  {#if shown && output.length}
    <pre class="output">{output.join('\n')}</pre>
  {/if}
  {#if surface === 'dm'}<EventDetails text={post.text} />{/if}
</div>

<style>
  .row {
    margin: var(--s1) var(--s4);
    border: 1px solid var(--line);
    border-radius: 8px;
    background: var(--side);
    overflow: hidden;
  }
  .row.dm { margin: var(--s1) 0; }
  .sum { display: flex; align-items: baseline; gap: var(--s2); padding: 8px 12px; color: var(--ink-2); font-size: var(--t-sm); }
  .glyph { flex: none; color: var(--muted); font-family: var(--mono); font-size: var(--t-cap); }
  .label { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-family: var(--mono); }
  .state { flex: none; font-size: var(--t-cap); font-weight: 600; }
  .state.sage { color: var(--sage); }
  .state.coral { color: var(--coral); }
  .state.gold { color: var(--gold); }
  .exit { flex: none; padding: 0 6px; border-radius: var(--r-xs); background: var(--coral-wash, var(--raise)); color: var(--coral); font: var(--t-cap)/1.6 var(--mono); }
  .fact { flex: none; color: var(--muted); font-variant-numeric: tabular-nums; }
  .btn {
    flex: none;
    min-height: 26px;
    padding: 2px 10px;
    border: 1px solid var(--line);
    border-radius: 6px;
    background: var(--raise);
    color: var(--ink-2);
    font: inherit;
    font-size: var(--t-cap);
    cursor: pointer;
    transition: color var(--motion-fast) var(--ease), background-color var(--motion-fast) var(--ease);
  }
  .btn:hover { background: var(--raise-hover); color: var(--ink); }
  .btn:active { background: var(--select); }
  time { flex: none; color: var(--faint); font-size: var(--t-cap); white-space: nowrap; }
  .output {
    margin: 0;
    padding: var(--s2) 12px;
    border-top: 1px solid var(--hair);
    max-height: 260px;
    overflow: auto;
    background: var(--rail);
    color: var(--ink-2);
    font: var(--t-cap)/1.5 var(--mono);
    white-space: pre-wrap;
    overflow-wrap: anywhere;
  }
  @media (max-width: 760px) {
    .row { margin: var(--s1) var(--s2); }
    .sum { flex-wrap: wrap; min-height: 44px; padding: 6px var(--s2); gap: 4px var(--s2); }
    time { display: none; }
  }
</style>
