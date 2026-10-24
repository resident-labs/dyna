<script lang="ts">
  // One post of another conversation, as a session's DM shows it: who wrote it,
  // where it lives, its preview or registered event card, and a jump to the post.
  // It is a reference, never a copy of the post.
  import Avatar from '../ui/Avatar.svelte'
  import { stamp } from '../ui/format.ts'
  import { referenceHome } from './dm.ts'
  import { rendererFor } from '../rooms/eventRenderers.ts'
  import { systemEvent } from '../rooms/eventPayloads.ts'
  import { isEventPayload } from '../rooms/events.gen.ts'
  import UnknownEventRow from '../rooms/UnknownEventRow.svelte'
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
  // A typed reference keeps the feed's own card and payload. Its agent-facing
  // text is not a second summary, and a legacy card alone keeps that old text.
  let eventPost = $derived({ id: ref.postId, ts: ref.ts, text: isEventPayload(ref.kind, ref.notice?.kind, ref.payload) ? '' : ref.preview, kind: ref.kind, from: ref.from, fromHue: ref.fromHue, speaker: ref.speaker, notice: ref.notice, payload: ref.payload })
  let typed = $derived(systemEvent(eventPost))
  let Card = $derived(rendererFor(eventPost))
</script>

<article class="reference-row" class:enter>
  <div class="gutter"><Avatar name={ref.speaker} hue={ref.fromHue} size={28} /></div>
  <div class="body">
    {#if typed}
      <div class="reference typed">
        <span class="head">
          <strong class="hue-{ref.fromHue}">{ref.speaker}</strong>
          <span class="home">from {home}</span>
          <time>{stamp(ref.ts)}</time>
        </span>
        <div class="event-preview">
          {#if Card}
            <Card post={eventPost} notice={ref.notice} surface="dm" {room} />
          {:else}
            <UnknownEventRow dm />
          {/if}
        </div>
        <button class="foot jump" title="Open this post where it was written" onclick={() => onpost?.(ref.postId)} disabled={!onpost}>
          {#if ref.attachments}<span class="files">{files}<span aria-hidden="true"> · </span></span>{/if}Jump to message
        </button>
      </div>
    {:else}
      <button class="reference" title="Open this post where it was written" onclick={() => onpost?.(ref.postId)} disabled={!onpost}>
        <span class="head">
          <strong class="hue-{ref.fromHue}">{ref.speaker}</strong>
          <span class="home">from {home}</span>
          <time>{stamp(ref.ts)}</time>
        </span>
        <span class="preview">{ref.preview || 'No text'}</span>
        <span class="foot">{#if ref.attachments}<span class="files">{files}<span aria-hidden="true"> · </span></span>{/if}Jump to message</span>
      </button>
    {/if}
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
  .jump { width: fit-content; padding: 0; border: 0; background: none; font: inherit; text-align: left; cursor: pointer; }
  .jump:disabled { opacity: 1; cursor: default; }
  .event-preview { min-width: 0; }
  @media (max-width: 760px) {
    .head time { display: none; }
  }
</style>
