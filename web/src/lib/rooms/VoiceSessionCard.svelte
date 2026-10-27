<script lang="ts">
  // The row for an agent's live voice session: one row when it starts and one
  // when it ends, with the time it used. The owner reads who spent the voice
  // provider's quota. It reads the typed payload of the post, never its text,
  // and draws the same row in the feed and in a session DM.
  import type { EventNotice } from '../api/room.ts'
  import type { EventPost, EventSurface } from './eventRenderers.ts'
  import type { VoiceEndReason } from './events.gen.ts'
  import { voiceSessionEvent } from './eventPayloads.ts'
  import EventDetails from './EventDetails.svelte'
  import EventRow from './EventRow.svelte'
  import CallIcon from '../ui/CallIcon.svelte'

  let { post, notice = post.notice, surface = 'feed' }: { post: EventPost; notice?: EventNotice; surface?: EventSurface } = $props()

  const reasons: Record<VoiceEndReason, string> = {
    'input-closed': 'the agent hung up',
    'time-limit': 'it reached its time limit',
    idle: 'no audio came for 60 seconds',
    'provider-closed': 'the provider closed it',
    error: 'it failed',
    cancelled: 'it was cancelled',
  }

  // used reads the time in minutes and seconds, so a short session is not "0 min".
  function used(seconds: number): string {
    const minutes = Math.floor(seconds / 60)
    return minutes > 0 ? `${minutes} min ${seconds % 60} s` : `${seconds} s`
  }

  let payload = $derived(voiceSessionEvent(post))
  let ended = $derived(payload?.type === 'ended')
  let who = $derived(payload?.session ?? 'An agent')
  let summary = $derived.by(() => {
    if (!payload) return ''
    if (!ended) return `${payload.session} on ${payload.provider}`
    const why = payload.reason ? `, ${reasons[payload.reason]}` : ''
    return `${payload.session} used ${used(payload.seconds ?? 0)} on ${payload.provider}${why}`
  })
  let chips = $derived([{ label: ended ? 'ended' : 'live', tone: ended ? '' : 'sage' }])
</script>

{#snippet icon()}
  <CallIcon size={15} />
{/snippet}

<div class="voice-session" title={who}>
  <EventRow id={post.id} ts={post.ts} {surface} title="Agent voice session" {summary} {chips} {icon} />
  {#if surface === 'dm'}<EventDetails text={post.text} />{/if}
</div>
