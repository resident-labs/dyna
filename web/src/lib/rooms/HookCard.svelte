<script lang="ts">
  import type { EventNotice } from '../api/room.ts'
  import type { EventPost, EventSurface } from './eventRenderers.ts'
  import { hookEvent } from './eventPayloads.ts'
  import type { HookState } from './events.gen.ts'
  import { openHookLog } from '../settings/hookLog.ts'
  import EventDetails from './EventDetails.svelte'
  import EventRow from './EventRow.svelte'
  import HookIcon from '../ui/HookIcon.svelte'
  import { clock, durationMs } from '../ui/format.ts'

  let { post, posts = [post], notice = post.notice, surface = 'feed' }: {
    post: EventPost
    posts?: EventPost[]
    notice?: EventNotice
    surface?: EventSurface
  } = $props()

  const stateWords: Record<HookState, string> = {
    running: 'running',
    passed: 'passed',
    failed: 'failed',
    error: 'error',
  }
  // A failed hook and a hook that could not run both need the owner's eye.
  const stateTone: Record<HookState, string> = {
    running: 'gold',
    passed: 'sage',
    failed: 'coral',
    error: 'coral',
  }

  let payload = $derived(hookEvent(post))
  let name = $derived(payload?.label || payload?.hook || 'Hook')
  let state = $derived<HookState>(payload?.state ?? 'running')
  let runId = $derived(payload?.runId ?? '')
  let runs = $derived(posts.filter((item) => hookEvent(item)?.state === 'passed'))
  let elapsed = $derived(state === 'running' ? `started ${clock(post.ts)}` : runs.length > 1 ? `${runs.length} runs · last ${clock(post.ts)}` : durationMs(payload?.durationMs ?? 0))

  function viewLog() {
    if (!payload || !runId) return
    openHookLog({ repo: payload.repo, hook: payload.hook, runId })
  }
</script>

<EventRow
  id={post.id}
  ts={post.ts}
  {surface}
  title={name}
  chips={[...(payload?.builtin ? [{ label: 'Automatic', tone: 'iris' }] : []), { label: stateWords[state], tone: stateTone[state], failure: state === 'failed' || state === 'error' }]}
  summary={[elapsed, payload?.detail ?? ''].filter(Boolean).join(' · ')}
>
  {#snippet icon()}<HookIcon size={15} />{/snippet}
  {#snippet detail()}
    {#if runs.length > 1}
      <ul>{#each runs as run}<li>{clock(run.ts)} · {durationMs(hookEvent(run)?.durationMs ?? 0)}</li>{/each}</ul>
    {/if}
    {#if payload?.detail}<p class="detail-text">{payload.detail}</p>{/if}
    {#if runId}<button class="link" type="button" onclick={viewLog} aria-label={`View the log of ${name}`}>View log</button>{/if}
    {#if surface === 'dm'}<EventDetails text={post.text} />{/if}
  {/snippet}
</EventRow>

<style>
  .detail-text { margin: 0 0 var(--s2); color: var(--muted); overflow-wrap: anywhere; }
</style>
