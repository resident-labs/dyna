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
  import EventRow from './EventRow.svelte'

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

{#snippet body()}
  {#if output.length}<pre class="output">{output.join('\n')}</pre>{/if}
  {#if surface === 'dm'}<EventDetails text={post.text} />{/if}
{/snippet}

<EventRow
  id={post.id}
  ts={post.ts}
  {surface}
  title={payload?.label ?? 'Shell command'}
  chips={[{ label: stateWords[jobState], tone: stateTone[jobState], failure: jobState === 'failed' }, ...((payload?.exitCode ?? 0) !== 0 ? [{ label: `exit ${payload?.exitCode}`, tone: 'coral', failure: true }] : [])]}
  summary={[elapsed, output.length ? `${output.length} output line${output.length === 1 ? '' : 's'}` : ''].filter(Boolean).join(' · ')}
  open={shown}
  ontoggle={(value) => (open = value)}
  detail={output.length || surface === 'dm' ? body : undefined}
>
  {#snippet icon()}$_{/snippet}
</EventRow>

<style>
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
</style>
