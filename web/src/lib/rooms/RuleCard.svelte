<script lang="ts">
  // The row for a rule change: who changed which rule, and what the rule says
  // now. It reads the typed payload of the post, never its text, and sits in
  // the owner feed without waking anyone.
  import type { EventNotice, Room } from '../api/room.ts'
  import type { EventPost, EventSurface } from './eventRenderers.ts'
  import { ruleEvent } from './eventPayloads.ts'
  import type { RuleVerb } from './events.gen.ts'
  import EventDetails from './EventDetails.svelte'
  import EventRow from './EventRow.svelte'
  import { short } from '../ui/format.ts'

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
  let title = $derived(payload ? `Rule ${payload.ruleId}` : 'Rule changed')
  let line = $derived(payload ? `${actor} ${verbs[payload.verb]} the ${payload.scope} rule` : '')
  let summary = $derived([line, reached.length ? `reached ${reached.length} ${reached.length === 1 ? 'session' : 'sessions'}` : ''].filter(Boolean).join(' · '))
  let hasBody = $derived(!!payload?.text || reached.length > 0)
</script>

{#snippet icon()}
  <span aria-hidden="true">§</span>
{/snippet}

{#snippet body()}
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
{/snippet}

<EventRow id={post.id} ts={post.ts} {surface} {title} {summary} {icon} detail={hasBody ? body : undefined} />

{#if surface === 'dm' && !hasBody}<EventDetails text={post.text} />{/if}

<style>
  .text { margin: 0; white-space: pre-wrap; overflow-wrap: anywhere; }
  .sessions { margin: 4px 0 0; }
  .sessions summary { cursor: pointer; color: var(--faint); font-size: var(--t-cap); }
  .sessions ul { margin: 4px 0 0; padding-left: 18px; color: var(--muted); }
</style>
