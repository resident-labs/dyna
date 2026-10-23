<script lang="ts">
  import PostActions from '../ui/PostActions.svelte'
  import { postHandlers } from '../ui/postSelect.ts'
  import { createSelection } from '../ui/selection.svelte.ts'
  import ForwardQuote from '../ui/ForwardQuote.svelte'
  import Avatar from '../ui/Avatar.svelte'
  import PostFiles from '../ui/PostFiles.svelte'
  import PostLink from '../ui/PostLink.svelte'
  import PRPost from '../prs/PRPost.svelte'
  import { markdown, stamp, clock } from '../ui/format.ts'
  import { postedTo } from './dm.ts'
  import { speaker } from '../ui/posts.ts'
  import { postParts } from '../ui/postParts.ts'
  import type { Attachment, Member, Room } from '../api/room.ts'
  import type { PostSelection } from '../ui/postSelect.ts'
  import type { MessageRow, Receipt, ThreadPost } from './dm.ts'

  interface Props {
    row: MessageRow
    room: Room
    onforward?: ((post: ThreadPost) => void) | null
    member?: Member
    address: string
    members?: Member[]
    onunqueue?: ((pending: Receipt) => void) | null
    ondismiss?: ((pending: Receipt) => void) | null
    removing?: string
    onopenfile: (attachment: Attachment) => void
    onpost?: ((post: ThreadPost) => void) | null
    inThread?: boolean
    compact?: boolean
    enter?: boolean
    selection?: PostSelection
  }
  let { row, room, onforward = null, member, address, members = [], onunqueue = null, ondismiss = null, removing = '', onopenfile, onpost = null, inThread = false, compact = false, enter = false, selection = createSelection() }: Props = $props()
  // selection is the list's one selected post (postSelect.ts), shared by all
  // its messages; a message alone keeps its own.
  let post = $derived(row.post)
  let actionable = $derived(!!(post?.id && onforward))
  let delivery = $derived(row.delivery || '')
  let person = $derived(row.author === address ? member : members.find((m) => m.address === row.author))
  let owner = $derived(row.author === room.name)
  let name = $derived(speaker(row.post ?? { from: row.author, human: row.human }, room.name))
  let sentTo = $derived(row.post ? postedTo(row.post, address, room.name) : '')
  let hue = $derived(row.post?.fromHue ?? person?.hue ?? 0)
  const timeDescriptionId = $props.id()
  let exactTime = $derived.by(() => {
    if (row.ts === undefined) return undefined
    const date = new Date(row.ts)
    if (Number.isNaN(date.getTime())) return undefined
    return date.toLocaleString(undefined, {
      year: 'numeric', month: 'long', day: 'numeric',
      hour: 'numeric', minute: '2-digit', second: '2-digit', timeZoneName: 'long',
    })
  })
  let typed = $derived(postParts({ text: row.text, attachments: row.post?.attachments }))
</script>
<!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_noninteractive_tabindex -->
<article class="message" class:post={actionable} class:cont={row.cont} class:selected={!!post && selection.get() === post.id} class:enter tabindex={actionable ? 0 : undefined} data-select-id={post?.id} {...(post && actionable ? postHandlers(selection, post.id) : {})}>
  <div class="gutter">
    {#if row.cont}<span class="side-time">{@render when(clock(row.ts))}</span>
    {:else}<Avatar {name} {hue} {owner} emoji={row.post?.fromEmoji || person?.emoji} size={compact ? 28 : 40} />{/if}
  </div>
  <div class="body">
    {#if !row.cont}<div class="head"><span class="from hue-{hue}" class:owner>{name}</span>{@render when(stamp(row.ts))}{#if row.post && !delivery && !inThread && !row.ownerAttachments && sentTo}<span class="tag posted">posted to {sentTo}</span>{/if}</div>{/if}
    {#if row.post?.kind === 'pr'}<PRPost post={row.post} {room} {onforward} selection={actionable ? selection : null} />
    {:else if row.folded}<details class="folded"><summary>Turn result (same as the post above)</summary><div class="prose">{@html markdown(typed.text)}</div></details>
    {:else if typed.text}<div class="prose">{@html markdown(typed.text)}</div>{/if}
    {#if row.post?.forward}<ForwardQuote {room} post={row.post.forward} {onpost} {onopenfile} />{/if}
    <PostFiles roomId={room.id} post={{ id: row.post?.id, text: row.text, attachments: row.post?.attachments }} onopen={onopenfile} />
    {@render receipt()}
  </div>
  {#if actionable && row.post && row.post.kind !== 'pr'}<PostActions {room} p={row.post} {onforward} show={selection.get() === row.post.id} />{/if}
</article>
<!-- A posted message's time opens the original room post. -->
{#snippet when(text: string)}
  {#if exactTime}<time title={exactTime} aria-describedby={exactTime ? timeDescriptionId : undefined}>
    {#if post?.id}<PostLink roomId={room.id} postId={post.id} onopen={onpost}><span title={exactTime} aria-label={exactTime}>{text}</span></PostLink>{:else}{text}{/if}
  </time>
  <span class="time-description" id={timeDescriptionId}>{exactTime}</span>{/if}
{/snippet}
{#snippet receipt()}
  {#if delivery && !row.quietReceipt}
    {@const pending = row.pending}
    <div class="receipt"><span>{delivery}</span>
      {#if pending && ondismiss}
        <span aria-hidden="true"> · </span><button onclick={() => ondismiss(pending)} disabled={!!removing} aria-label="Dismiss this pending message" title="Dismiss with confirmation">Dismiss</button>
      {:else if pending?.cancellable && pending.state === 'waiting' && onunqueue}
        <span aria-hidden="true"> · </span><button onclick={() => onunqueue(pending)} disabled={removing === pending.id} aria-label="Remove this waiting message" title="Remove before it is delivered">Remove</button>
      {/if}
    </div>
  {/if}
{/snippet}
<style>
  .message { position: relative; display: grid; grid-template-columns: var(--gutter, 56px) minmax(0, 1fr); padding: 2px var(--s4) 2px 0; margin-top: 14px; transition: background-color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .message:hover { background: var(--hover); }
  }
  .message:active { background: var(--select); }
  .message.cont { margin-top: 0; }
  .gutter { display: flex; justify-content: center; padding-top: 2px; }
  .body { min-width: 0; overflow-wrap: anywhere; }
  .head { display: flex; align-items: baseline; gap: var(--s2); flex-wrap: wrap; }
  .from { font-size: var(--t-body); font-weight: 600; }
  .from.owner { color: var(--iris); }
  .time-description { position: absolute; width: 1px; height: 1px; padding: 0; margin: -1px; overflow: hidden; clip-path: inset(50%); white-space: nowrap; }
  time { font-size: var(--t-cap); color: var(--faint); }
  .side-time { opacity: 0; padding-top: var(--s1); font-size: var(--t-cap); }
  .message:focus-within .side-time, .message.selected .side-time { opacity: 1; }
  .message:hover .side-time { opacity: 1; }
  .tag { font-size: var(--t-cap); font-weight: 600; color: var(--muted); background: var(--raise); border-radius: 4px; padding: 0 5px; }
  .receipt { display: flex; align-items: center; gap: var(--s1); margin: 2px 0 var(--s1); color: var(--faint); font-size: var(--t-cap); }
  .receipt button { color: inherit; font: inherit; padding: 2px 0; }
  .receipt button:enabled { transition: color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .receipt button:hover:enabled { color: var(--coral); }
  }
  .posted { color: var(--iris); background: var(--iris-wash); }
  .folded summary { cursor: pointer; width: fit-content; color: var(--faint); font-size: var(--t-sm); transition: color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .folded summary:hover { color: var(--ink); }
  }
  .folded[open] summary { margin-bottom: var(--s1); }
</style>
