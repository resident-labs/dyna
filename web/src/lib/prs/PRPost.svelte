<script lang="ts">
  import { openExternal } from '../../api.js'
  import { markdown, stamp, clock } from '../ui/format.js'
  import { shortSha } from './pr.ts'
  import { feedPRDetail, feedPREvents } from '../rooms/feedRows.js'
  import Row from '../ui/Row.svelte'
  import Chip from '../ui/Chip.svelte'
  import PostActions from '../ui/PostActions.svelte'
  import PinIcon from '../ui/PinIcon.svelte'
  import { revealWhen } from '../ui/focusWhen.js'
  import { postHandlers, type PostSelection } from '../ui/postSelect.ts'
  import type { Room } from '../api/room.ts'
  import type { ThreadPost } from '../sessions/dm.ts'

  interface Props {
    post: ThreadPost
    posts?: ThreadPost[]
    showRepo?: boolean
    to?: string[]
    onaddress?: (to: string) => void
    room: Room
    pinned?: Set<string>
    selection?: PostSelection | null
    onreply?: (post: ThreadPost) => void
    onforward?: ((post: ThreadPost) => void) | null
    onpin?: (post: ThreadPost) => void
    focus?: string
  }
  let { post, posts = [post], showRepo = false, to = [], onaddress, room, pinned = new Set(), selection = null, onreply, onforward, onpin, focus = '' }: Props = $props()
  // selection is the view's one selected post (postSelect.ts); each notice is
  // selectable, by tap, hover or Tab, and shows its own bar, so the post itself has none.
  let details = $derived(posts.map(feedPRDetail))
  let pr = $derived(details.at(-1) ?? feedPRDetail(post))
  let events = $derived(feedPREvents(details))
  let more = $state(false)
  // A jump to one of these notices opens the history to show it; the
  // toggle event then keeps it open.
  let open = $derived(more || !!focus)
</script>

<details class="pr" {open} ontoggle={(e) => (more = e.currentTarget.open)}>
  <summary>
  <Row dense>
    {#snippet lead()}<span class="glyph" aria-hidden="true">↑</span>{/snippet}
    {#snippet children()}
      <span class="headline"><strong>{pr.number ? `PR #${pr.number}` : 'PR watcher'}</strong>
        {#each events as event}<Chip tone={event.tone}>{event.label}</Chip>{/each}
      </span>
      <span class="meta">{[showRepo ? pr.repo : '', shortSha(pr.head), posts.length > 1 ? `${posts.length} updates` : ''].filter(Boolean).join(' · ')}</span>
    {/snippet}
    {#snippet trail()}<time title={new Date(post.ts ?? 0).toLocaleString()}>{clock(post.ts)}</time><span class="chevron" aria-hidden="true">›</span>{/snippet}
  </Row>
  </summary>
    <div class="details">
      {#if pr.url}<button class="link" onclick={() => openExternal(pr.url)}>Open PR #{pr.number} on GitHub</button>{/if}
      {#if to.length}<span class="to">To {#each to as name, i}<button class="link" onclick={() => { const target = post.to?.[i]; if (target) onaddress?.(target) }}>{name}</button>{/each}</span>{/if}
      {#each details as detail, i}
        {@const notice = posts[i] ?? post}
        <!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_noninteractive_element_interactions -->
        <section class="notice" class:flash={notice.id === focus} data-notice-id={notice.id} data-select-id={notice.id} tabindex="0" use:revealWhen={notice.id === focus} {...(selection ? postHandlers(selection, notice.id) : {})}>
          <div class="notice-head"><time>{stamp(notice.ts)}</time>{#if pinned.has(notice.id)}<span class="notice-pin" title="Pinned"><PinIcon size={13} filled /></span>{/if}</div>
          {#if open}
          {#if detail.dropped}<p class="noise">{detail.dropped} dropped as noise</p>{/if}
          {#if detail.body}<div class="prose">{@html markdown(detail.body)}</div>{/if}
          {#if detail.log}<details class="log"><summary>CI log tail</summary><pre>{detail.log}</pre></details>{/if}
          {/if}
          <div class="notice-actions"><PostActions {room} p={notice} pinned={pinned.has(notice.id)} show={selection?.get() === notice.id} {onreply} {onforward} {onpin} /></div>
        </section>
      {/each}
    </div>
</details>

<style>
  .pr { min-width: 0; }
  .pr > summary { list-style: none; cursor: pointer; }
  .pr > summary::-webkit-details-marker { display: none; }
  .pr[open] .chevron { transform: rotate(90deg); }
  .notice-head { display: flex; align-items: center; gap: var(--s2); }
  .notice-pin { display: inline-flex; color: var(--muted); }
  .notice:focus-visible { outline: 2px solid var(--iris); outline-offset: var(--s1); }
  .notice.flash { background: var(--iris-wash); }
  .notice-actions { margin-top: var(--s2); }
  .notice-actions :global(.actions) { position: static; width: fit-content; max-width: 100%; transform: none; }
  .glyph { width: 24px; height: 24px; display: grid; place-items: center; color: var(--muted); font-size: var(--t-title); }
  .headline { display: flex; align-items: center; flex-wrap: wrap; gap: var(--s1) var(--s2); font-size: var(--t-body); }
  strong { font-weight: 600; }
  .meta { display: block; margin-top: 2px; color: var(--muted); font-size: var(--t-sm); overflow-wrap: anywhere; }
  time, .chevron { flex: none; color: var(--faint); font-size: var(--t-cap); }
  .chevron { font-size: var(--t-title); }
  .details { padding: var(--s2) var(--s2) var(--s3) 44px; color: var(--ink-2); font-size: var(--t-sm); overflow-wrap: anywhere; }
  section + section { margin-top: var(--s3); padding-top: var(--s2); border-top: 1px solid var(--hair); }
  .link { color: var(--iris); font-size: var(--t-sm); }
  .link:hover { text-decoration: underline; }
  .to { display: flex; align-items: baseline; flex-wrap: wrap; gap: var(--s2); color: var(--muted); margin-top: var(--s2); }
  .noise { color: var(--faint); margin: var(--s2) 0; }
  .log { margin-top: var(--s2); }
  .log summary { cursor: pointer; color: var(--muted); }
  .log pre { max-height: 280px; overflow: auto; padding: var(--s3); background: var(--side); border-radius: var(--r-sm); white-space: pre-wrap; overflow-wrap: anywhere; font: var(--t-sm)/1.5 var(--mono); }
  @media (max-width: 760px) { .details { padding-left: var(--s2); } }
</style>
