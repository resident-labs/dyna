<script lang="ts">
  // The row for a rule change: who changed which rule, and what the rule says
  // now. It reads the typed payload of the post, never its text, and sits in
  // the owner feed without waking anyone.
  import type { EventNotice, Room } from '../api/room.ts'
  import type { EventPost, EventSurface } from './eventRenderers.ts'
  import { ruleEvent } from './eventPayloads.ts'
  import type { RuleVerb } from './events.gen.ts'
  import EventDetails from './EventDetails.svelte'
  import { clock, short } from '../ui/format.ts'

  let {
    post,
    notice = post.notice,
    surface = 'feed',
    room = undefined,
  }: { post: EventPost; notice?: EventNotice; surface?: EventSurface; room?: Room } = $props()

  const verbs: Record<RuleVerb, string> = { added: 'added', edited: 'edited', removed: 'removed' }

  let payload = $derived(ruleEvent(post))
  let reached = $derived(payload?.sessions ?? [])
  let actor = $derived(payload ? short(payload.actor, room?.name) : '')
  let line = $derived.by(() => {
    if (!payload) return ''
    return `${actor} ${verbs[payload.verb]} the ${payload.scope} rule ${payload.ruleId}`
  })
</script>

<div class="card {surface}" data-id={post.id}>
  <div class="sum">
    <span class="glyph" aria-hidden="true">§</span>
    <span class="line">{line}</span>
    {#if reached.length}<span class="reach">· reached {reached.length} {reached.length === 1 ? 'session' : 'sessions'}</span>{/if}
    <time title={new Date(post.ts ?? 0).toLocaleString()}>{clock(post.ts)}</time>
  </div>
  {#if payload?.text}<p class="text">{payload.text}</p>{/if}
  {#if reached.length}
    <details class="sessions">
      <summary>Sessions reached</summary>
      <ul>
        {#each reached as session (session)}<li>{short(session, room?.name)}</li>{/each}
      </ul>
    </details>
  {/if}
  {#if surface === 'dm'}<EventDetails text={post.text} />{/if}
</div>

<style>
  .card { margin: 6px var(--s4); color: var(--muted); font-size: var(--t-sm); }
  .card.dm { margin: 6px 0; }
  .sum { display: flex; align-items: baseline; gap: 8px; }
  .glyph { flex: none; color: var(--iris); }
  .line { flex: 1; min-width: 0; overflow-wrap: anywhere; }
  .reach { flex: none; color: var(--muted); font-size: var(--t-cap); }
  time { flex: none; color: var(--faint); font-size: var(--t-cap); white-space: nowrap; }
  .text { margin: 2px 0 0 24px; color: var(--ink-2); overflow-wrap: anywhere; }
  .sessions { margin: 4px 0 0 24px; }
  .sessions summary { cursor: pointer; color: var(--faint); font-size: var(--t-cap); }
  .sessions ul { margin: 4px 0 0; padding-left: 18px; color: var(--muted); }
  @media (max-width: 760px) {
    .card { margin: 6px var(--s2); }
    .sum { flex-wrap: wrap; gap: 4px 8px; }
    time { display: none; }
  }
</style>
