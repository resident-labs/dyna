<script lang="ts">
  import type { EventNotice } from '../api/room.ts'
  import type { EventPost, EventSurface } from './eventRenderers.ts'
  import { isGitHubQuotaEvent } from './events.gen.ts'
  import EventRow from './EventRow.svelte'

  let { post, notice = post.notice, surface = 'feed' }: { post: EventPost; notice?: EventNotice; surface?: EventSurface } = $props()
  let quota = $derived(isGitHubQuotaEvent(post.payload) ? post.payload : undefined)
  let title = $derived(`GitHub quota low · ${quota?.host ?? ''} · ${quota?.resource ?? ''}`)
  let summary = $derived(`${quota?.remaining ?? 0} of ${quota?.limit ?? 0} remaining · Caller attribution is unavailable.`)
</script>

<EventRow id={post.id} ts={post.ts} {surface} {title} {summary} chips={[{ label: 'low quota', tone: 'coral' }]}>
  {#snippet icon()}◷{/snippet}
  {#snippet detail()}
    <p>Resets at {quota?.reset}. Caller attribution is unavailable.</p>
  {/snippet}
</EventRow>
