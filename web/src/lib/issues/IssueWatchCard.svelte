<script lang="ts">
  // The card for one new fact about an upstream GitHub issue that a room
  // watches: a comment, a close or a reopen. It reads the typed payload of the
  // post and shows the summary and the link. The comment text stays out of the
  // card, because the stored body carries the external-data wrapper for agents.
  import type { EventPost, EventSurface } from '../rooms/eventRenderers.ts'
  import { issueWatchEvent } from '../rooms/eventPayloads.ts'
  import type { IssueWatchType } from '../rooms/events.gen.ts'
  import EventRow from '../rooms/EventRow.svelte'

  let { post, surface = 'feed' }: { post: EventPost; surface?: EventSurface } = $props()

  const typeWords: Record<IssueWatchType, string> = {
    comment: 'comment',
    closed: 'closed',
    reopened: 'reopened',
  }
  // A close reads quiet, like a finished issue; a comment or a reopen needs a look.
  const typeTone: Record<IssueWatchType, string> = {
    comment: 'iris',
    closed: 'sage',
    reopened: 'gold',
  }

  let payload = $derived(issueWatchEvent(post))
  let legacyTitle = $derived((post.text ?? '').split('\n')[0] ?? '')
  let ref = $derived(payload ? `${payload.repo}#${payload.number}` : 'Upstream issue')
</script>

<EventRow
  id={post.id}
  ts={post.ts}
  {surface}
  title={ref}
  chips={payload ? [{ label: typeWords[payload.type], tone: typeTone[payload.type] }] : []}
  summary={payload?.summary ?? legacyTitle}
>
  {#snippet icon()}◇{/snippet}
  {#snippet detail()}
  <div class="body">
    {#if payload?.url}<a class="link" href={payload.url} target="_blank" rel="noopener noreferrer">Open on GitHub</a>{/if}
  </div>
  {/snippet}
</EventRow>

<style>
  .body { display: flex; align-items: center; gap: 6px; flex-wrap: wrap; }
  .link { color: var(--ink-2); font-size: var(--t-sm); }
</style>
