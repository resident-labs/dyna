<script lang="ts">
  import type { EventNotice } from '../api/room.ts'
  import type { EventPost, EventSurface } from './eventRenderers.ts'
  import { isShellInactivityEvent } from './events.gen.ts'
  import EventRow from './EventRow.svelte'

  let { post, notice = post.notice, surface = 'feed' }: { post: EventPost; notice?: EventNotice; surface?: EventSurface } = $props()
  let payload = $derived(isShellInactivityEvent(post.payload) ? post.payload : undefined)
  let summary = $derived(`${payload?.label ?? 'Shell job'} started by ${payload?.session ?? 'the owner'} has observed local output inactivity for ${payload?.minutes ?? 30} minutes.`)
</script>

{#snippet body()}
  <p>CPU activity is not measured. This notice does not mean the job is stuck.</p>
{/snippet}

<EventRow id={post.id} ts={post.ts} {surface} title="Shell output is quiet" {summary} detail={body} />
