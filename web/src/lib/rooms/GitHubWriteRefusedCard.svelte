<script lang="ts">
  import type { EventPost, EventSurface } from './eventRenderers.ts'
  import { isGitHubWriteRefusedEvent } from './events.gen.ts'
  import type { GitHubWriteReason } from './events.gen.ts'
  import EventRow from './EventRow.svelte'

  let { post, surface = 'feed' }: { post: EventPost; surface?: EventSurface } = $props()
  let event = $derived(isGitHubWriteRefusedEvent(post.payload) ? post.payload : undefined)
  const reasons: Record<GitHubWriteReason, string> = {
    'repository-unknown': 'Use --repo, a pull request URL, or a checkout with an origin remote.',
    'read-only-room': 'This room does not allow GitHub writes. Ask the owner to enable them.',
    'not-room-repository': 'This repository is not registered to the room. Choose a room repository.',
    'upstream-branch-delete': 'An upstream close cannot delete a branch. Remove the branch deletion option.',
    'not-room-account-item': 'The room account did not author this item. Choose an item it authored.',
    'foreign-comment': 'The room account did not author this comment. The owner can change it on GitHub.',
    'comment-account-unavailable': 'Dyna could not select the room GitHub account. Check the account and retry.',
    'comment-author-unavailable': 'Dyna could not read the comment author. Check access to the comment and retry.',
    'upstream-edit-foreign-head': 'The pull request head is not in the room fork. Choose a pull request from the fork.',
    'upstream-verb-not-allowed': 'This operation is not allowed on the upstream. Use a supported operation or the room repository.',
  }
  let summary = $derived(event ? `${event.requester} cannot run ${event.verb} on ${event.repository}.` : 'A GitHub write was refused.')
</script>

{#snippet body()}
  {#if event}
    <p>{reasons[event.reason]}</p>
    <p>Policy rule: <code>{event.rule}</code>.</p>
  {/if}
{/snippet}
<EventRow id={post.id} ts={post.ts} {surface} title="GitHub write refused" {summary} detail={body} />
