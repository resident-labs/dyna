<script lang="ts">
  import { heldComments } from './heldComments.ts'
  import HeldCommentCard from './HeldCommentCard.svelte'

  let { roomId, prKey, commentId }: { roomId: string; prKey: string; commentId: string } = $props()
  let comment = $derived($heldComments.comments.find((item) =>
    item.roomId === roomId && item.key === prKey && item.id === commentId))
</script>

{#if comment}
  <HeldCommentCard {comment} />
{:else if $heldComments.error}
  <p role="alert">Could not load this held comment: {$heldComments.error}</p>
{:else}
  <p class="quiet">{$heldComments.loaded ? 'This held comment is no longer pending.' : 'Loading held comment…'}</p>
{/if}

<style>
  p { color: var(--muted); font-size: 13px; margin: 8px 0; }
</style>
