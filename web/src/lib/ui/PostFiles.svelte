<script>
  import ReferenceChips from '../rooms/ReferenceChips.svelte'
  import Files from '../files/Files.svelte'
  import { postParts } from './postParts.ts'
  // The file cards of one post. Feed, Pins, forwards and the session view
  // all use this, so a post shows the same cards in every view.
  let { roomId, post, parts: given = null, onopen } = $props()
  let parts = $derived(given ?? postParts(post))
</script>

<ReferenceChips references={post.references ?? []} />
{#if parts.attachments.length}<Files {roomId} postId={post.id} items={parts.attachments} {onopen} />{/if}
