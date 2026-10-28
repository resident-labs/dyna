<script lang="ts">
  import type { EventNotice } from '../api/room.ts'
  import type { EventPost, EventSurface } from './eventRenderers.ts'
  import { isShellSilenceEvent } from './events.gen.ts'
  import EventRow from './EventRow.svelte'

  let { post, notice = post.notice, surface = 'feed' }: { post: EventPost; notice?: EventNotice; surface?: EventSurface } = $props()
  let payload = $derived(isShellSilenceEvent(post.payload) ? post.payload : undefined)
  let summary = $derived(`${payload?.label ?? 'Shell job'} has no output log growth for ${payload?.minutes ?? 30} minutes.`)
</script>

{#snippet body()}
  <p>Partial lines can be buffered. This notice does not mean the job is stuck.</p>
{/snippet}

<EventRow id={post.id} ts={post.ts} {surface} title="Shell output is quiet" {summary} detail={body} />
