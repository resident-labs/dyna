<script lang="ts">
  import type { ComponentProps } from 'svelte'
  import type { Post } from '../api/room.ts'
  import ReferenceChips from '../rooms/ReferenceChips.svelte'
  import Files from '../files/Files.svelte'
  import { postParts } from './postParts.ts'
  // The file cards of one post. Feed, Pins, forwards and the session view
  // all use this, so a post shows the same cards in every view.
  let { roomId, post, parts: given = null, onopen }: { roomId: string; post: Partial<Pick<Post, 'id' | 'text' | 'attachments' | 'references'>>; parts?: ReturnType<typeof postParts> | null; onopen?: ComponentProps<typeof Files>['onopen'] } = $props()
  let parts = $derived(given ?? postParts(post))
</script>

<ReferenceChips references={post.references ?? []} />
{#if parts.attachments.length}<Files {roomId} postId={post.id} items={parts.attachments} onopen={(attachment) => onopen?.(attachment)} />{/if}
