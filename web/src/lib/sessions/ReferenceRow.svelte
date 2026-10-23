<script lang="ts">
  // One post of another conversation, as a session's DM shows it: who wrote it,
  // where it lives, its first line, and one action that opens the post there.
  // It is a reference, never a copy of the post.
  import Avatar from '../ui/Avatar.svelte'
  import { stamp } from '../ui/format.ts'
  import { referenceHome } from './dm.ts'
  import type { Member, Room } from '../api/room.ts'
  import type { ReferenceRow } from './dm.ts'

  interface Props {
    row: ReferenceRow
    room: Room
    members?: Member[]
    onpost?: ((postId: string) => void) | null
    enter?: boolean
  }
  let { row, room, members = [], onpost = null, enter = false }: Props = $props()
  let ref = $derived(row.reference)
  let home = $derived(referenceHome(ref.conversation, members))
  let files = $derived(ref.attachments === 1 ? '1 file' : `${ref.attachments} files`)
</script>

<article class="reference-row" class:enter>
  <div class="gutter"><Avatar name={ref.speaker} hue={ref.fromHue} size={28} /></div>
  <div class="body">
    <button class="reference" title="Open this post where it was written" onclick={() => onpost?.(ref.postId)} disabled={!onpost}>
      <span class="head">
        <strong class="hue-{ref.fromHue}">{ref.speaker}</strong>
        <span class="home">from {home}</span>
        <time>{stamp(ref.ts)}</time>
      </span>
      <span class="preview">{ref.preview || 'No text'}</span>
      <span class="foot">{#if ref.attachments}<span class="files">{files}<span aria-hidden="true"> · </span></span>{/if}Jump to message</span>
    </button>
  </div>
</article>

<style>
  .reference-row { display: grid; grid-template-columns: var(--gutter, 56px) minmax(0, 1fr); padding: 2px var(--s4) 2px 0; margin-top: var(--s2); }
  .gutter { display: flex; justify-content: center; padding-top: 6px; }
  .body { min-width: 0; }
  .reference { display: flex; flex-direction: column; gap: 2px; width: 100%; min-width: 0; padding: 6px 10px; border: 1px solid var(--line-soft); border-radius: 8px; background: var(--side); color: var(--muted); font: inherit; text-align: left; transition: background-color var(--motion-fast) var(--ease); }
  .reference:disabled { opacity: 1; }
  @media (hover: hover) {
    .reference:hover { background: var(--hover); }
  }
  .reference:active { background: var(--select); }
  .head { display: flex; align-items: baseline; gap: var(--s2); min-width: 0; font-size: var(--t-ui); }
  .head strong { white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .home { color: var(--faint); font-size: var(--t-cap); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .head time { margin-left: auto; color: var(--faint); font-size: var(--t-cap); white-space: nowrap; }
  .preview { min-width: 0; overflow: hidden; white-space: nowrap; text-overflow: ellipsis; color: var(--faint); }
  .foot { color: var(--iris); font-size: var(--t-cap); }
  .files { color: var(--faint); }
  @media (max-width: 760px) {
    .head time, .preview { display: none; }
  }
</style>
