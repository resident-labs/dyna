<script lang="ts">
  // The card for a change: one quiet row in the feed and in a session DM, with
  // the change's latest facts and a way to its posts. It reads the typed
  // payload of the post, never its text, and keeps no feed-only state: the
  // optional props are the row's actions, which a DM does not pass.
  import { openExternal } from '../../api.ts'
  import { markdown, stamp } from '../ui/format.ts'
  import { prEvent } from '../rooms/eventPayloads.ts'
  import { commitUrl, refName, shortSha } from './pr.ts'
  import { chipOf, prView } from './prCard.ts'
  import EventRow from '../rooms/EventRow.svelte'
  import PostActions from '../ui/PostActions.svelte'
  import PinIcon from '../ui/PinIcon.svelte'
  import HeldCommentNotice from './HeldCommentNotice.svelte'
  import EventDetails from '../rooms/EventDetails.svelte'
  import type { EventPost, EventSurface } from '../rooms/eventRenderers.ts'
  import type { PostSelection } from '../ui/postSelect.ts'
  import type { Room } from '../api/room.ts'

  interface Props {
    post: EventPost
    surface?: EventSurface
    // posts is the folded posts of one PR, oldest first. A DM passes one.
    posts?: EventPost[]
    showRepo?: boolean
    to?: string[]
    onaddress?: (to: string) => void
    room?: Room
    pinned?: Set<string>
    // selection and the three actions belong to the room's post bar. A card
    // without them, such as one in a DM, still shows the change.
    selection?: PostSelection | null
    onreply?: (post: EventPost) => void
    onforward?: ((post: EventPost) => void) | null
    onpin?: (post: EventPost) => void
    focus?: string
  }
  let { post, surface = 'feed', posts = [post], showRepo = false, to = [], onaddress, room, pinned = new Set(), selection = null, onreply, onforward, onpin, focus = '' }: Props = $props()
  // The posts this card folds, in the shape the view reads. A feed row keeps
  // every post of the PR; a DM passes the one it shows.
  let folded = $derived(posts.length ? posts : [post])
  let view = $derived(prView(folded))
  let more = $state(false)
  // A jump to one of these posts opens the history to show it.
  let open = $derived(more || !!focus)
  // The head commit's page, so the row's pushed commit is not just a hash.
  let headUrl = $derived(/^[0-9a-f]{7,40}$/i.test(view.head) ? commitUrl(view.url, view.head) : '')
</script>

<EventRow
  id={post.id}
  ts={post.ts}
  {surface}
  title={view.number ? `PR #${view.number}` : view.title || 'PR watcher'}
  chips={view.chips.map((chip) => ({ ...chip, failure: chip.tone === 'coral' }))}
  summary={[view.title, showRepo ? view.repo : '', view.head ? `pushed ${refName(view.head)}` : ''].filter(Boolean).join(' · ')}
  summaryWrap={true}
  {open}
  ontoggle={(value) => (more = value)}
>
  {#snippet icon()}↑{/snippet}
  {#snippet footer()}
    {#if folded.length > 1}
      <details><summary>Show steps ({folded.length})</summary>
        {#each folded as step (step.id)}{@const event = prEvent(step)}<div class="step-note">{stamp(step.ts)} · {event ? chipOf(event).label : 'Update'}</div>{/each}
      </details>
    {/if}
  {/snippet}
  {#snippet detail()}
    <div class="details">
      {#if view.url}<button class="link" onclick={() => openExternal(view.url)}>Open PR #{view.number} on GitHub</button>{/if}
      {#if headUrl}<button class="link" onclick={() => openExternal(headUrl)}>Open commit {shortSha(view.head)} on GitHub</button>{/if}
      {#if to.length}<span class="to">To {#each to as name, i}<button class="link" onclick={() => { const target = post.to?.[i]; if (target) onaddress?.(target) }}>{name}</button>{/each}</span>{/if}
      {#each view.notices as notice, i}
        {@const source = folded[i] ?? post}
        <!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_noninteractive_element_interactions -->
        <section class="notice" class:flash={source.id === focus} data-notice-id={source.id} data-select-id={source.id} tabindex="0">
          <div class="notice-head"><time>{stamp(source.ts)}</time>{#if pinned.has(source.id)}<span class="notice-pin" title="Pinned"><PinIcon size={13} filled /></span>{/if}</div>
          {#if notice.held && room}
            <HeldCommentNotice roomId={room.id} prKey={notice.changeId} commentId={notice.commentId} />
          {/if}
          {#if open}
            {#if notice.dropped}<p class="noise">{notice.dropped} dropped as noise</p>{/if}
            {#if notice.body}<div class="prose">{@html markdown(notice.body)}</div>{/if}
            {#if notice.log}<details class="log"><summary>CI log tail</summary><pre>{notice.log}</pre></details>{/if}
          {/if}
          {#if room}
            <div class="notice-actions"><PostActions {room} p={source} pinned={pinned.has(source.id)} show={selection?.get() === source.id} {onreply} {onforward} {onpin} /></div>
          {/if}
        </section>
      {/each}
      {#if surface === 'dm'}<EventDetails text={post.text} />{/if}
    </div>
  {/snippet}
</EventRow>

<style>
  .step-note { color: var(--muted); }
  .notice-head { display: flex; align-items: center; gap: var(--s2); }
  .notice-pin { display: inline-flex; color: var(--muted); }
  .notice:focus-visible { outline: 2px solid var(--iris); outline-offset: var(--s1); }
  .notice.flash { background: var(--iris-wash); }
  .notice-actions { margin-top: var(--s2); }
  .notice-actions :global(.actions) { position: static; width: fit-content; max-width: 100%; transform: none; }
  section + section { margin-top: var(--s3); padding-top: var(--s2); border-top: 1px solid var(--hair); }
  .link { color: var(--iris); font-size: var(--t-sm); }
  .link:hover { text-decoration: underline; }
  .to { display: flex; align-items: baseline; flex-wrap: wrap; gap: var(--s2); color: var(--muted); margin-top: var(--s2); }
  .noise { color: var(--faint); margin: var(--s2) 0; }
  .log { margin-top: var(--s2); }
  .log summary { cursor: pointer; color: var(--muted); }
  .log pre { max-height: 280px; overflow: auto; padding: var(--s3); background: var(--side); border-radius: var(--r-sm); white-space: pre-wrap; overflow-wrap: anywhere; font: var(--t-sm)/1.5 var(--mono); }
</style>
