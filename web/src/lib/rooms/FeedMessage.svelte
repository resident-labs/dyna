<script lang="ts">
  // The regular message layout shared by feed posts and turn results. In the
  // feed the root has display:contents, so its gutter and body join the post's
  // existing grid. A DM supplies the same grid itself.
  import type { Snippet } from 'svelte'
  import Avatar from '../ui/Avatar.svelte'
  import { markdown, stamp, clock } from '../ui/format.ts'
  import { speaker } from '../ui/posts.ts'
  import { postParts } from '../ui/postParts.ts'
  import { postPreview } from '../ui/preview.ts'
  import RunAvatar from '../workflows/RunAvatar.svelte'
  import WorkflowMessage from './WorkflowMessage.svelte'
  import { scriptSender, scriptLabel, scriptHref } from './scriptSender.ts'
  import { look } from './look.svelte.ts'
  import { setting } from './look.ts'
  import type { Post } from '../api/room.ts'

  type MessagePost = Omit<Partial<Post>, 'ts'> & { id: string; ts?: string | number }
  interface Recipients { groups: string[]; names: string[] }
  interface Props {
    post: MessagePost
    room: { name: string }
    surface?: 'feed' | 'dm'
    cont?: boolean
    fullText?: boolean
    turnResult?: boolean
    recipients?: Recipients
    expanded?: boolean
    ontoggle?: () => void
    onaddress?: (address: string) => void
    footer?: Snippet
  }
  let {
    post,
    room,
    surface = 'feed',
    cont = false,
    fullText = false,
    turnResult = false,
    recipients = undefined,
    expanded = undefined,
    ontoggle = undefined,
    onaddress = undefined,
    footer = undefined,
  }: Props = $props()

  let localExpanded = $state(false)
  let isExpanded = $derived(expanded ?? localExpanded)
  let script = $derived(scriptSender(post))
  let name = $derived(script ? scriptLabel(script) : speaker(post, room.name))
  let parts = $derived(postParts(post))
  let preview = $derived(postPreview(parts.text))
  // Root's ordinary posts always stay open. A turn result is not an ordinary
  // root post, so it follows the same opt-in trim rule as other agents.
  let rootPost = $derived(post.kind !== 'turn' && post.speaker === 'root')
  let trim = $derived(surface === 'feed' && !fullText && !rootPost && setting(look, 'trim') === true)
  let shown = $derived(!trim || post.human || isExpanded ? parts.text : preview.text)
  let bodyId = $derived(`message-${surface === 'dm' ? 'dm-' : ''}${post.id}`)

  function toggle() {
    if (ontoggle) ontoggle()
    else localExpanded = !localExpanded
  }
</script>

<div class="message {surface}" data-id={turnResult ? post.id : undefined}>
  <span class="gutter">
    {#if cont}<time class="side-time">{clock(post.ts)}</time>
    {:else if script}<a class="av" href={scriptHref(post.roomId ?? '', script)} aria-label="Open workflow run {name}"><RunAvatar size={40} /></a>
    {:else}<button class="av" onclick={() => onaddress?.(post.from ?? '')} aria-label={name}>
      <Avatar {name} hue={post.fromHue} owner={post.owner} emoji={post.fromEmoji} size={40} />
    </button>{/if}
  </span>
  <div class="body" class:turn-result={turnResult}>
    {#if !cont}
      <header>
        {#if script}<a class="name run-name" href={scriptHref(post.roomId ?? '', script)} title="Open workflow run {script.runId}">{name}</a><span class="tag">workflow</span>
        {:else}<button class="name hue-{post.fromHue}" onclick={() => onaddress?.(post.from ?? '')}>{name}</button>{/if}{#if post.source === 'card'}<span class="tag" title="A card wrote this text. You confirmed it before it was sent.">card</span>{/if}
        {#if post.fromRoom}<span class="tag" title="The owner sent this from the {post.fromRoom} room. Your reply goes back there.">from room {post.fromRoom}</span>{/if}
        <time title={new Date(post.ts ?? 0).toLocaleString()}>{stamp(post.ts)}</time>
        {#if recipients}
          <span class="to">
            <span class="arrow" aria-hidden="true">→</span>
            {#each recipients.groups as group}<span class="group">{group}</span>{/each}
            {#each recipients.names as recipient, i}<button class="rcpt" onclick={() => onaddress?.(post.to?.[i] ?? '')}>{recipient}</button>{#if i < recipients.names.length - 1}<span class="comma">,</span>{/if}{/each}
          </span>
        {/if}
      </header>
    {/if}
    {#if parts.text && script}
      <WorkflowMessage id={bodyId} text={shown} />
      {#if trim && !post.human && preview.truncated}<button class="message-toggle" aria-expanded={isExpanded} aria-controls={bodyId} onclick={toggle}>{isExpanded ? 'Show less' : 'Show more'}</button>{/if}
    {:else if parts.text}
      <div id={bodyId} class="prose">{@html markdown(shown)}</div>
      {#if trim && !post.human && preview.truncated}<button class="message-toggle" aria-expanded={isExpanded} aria-controls={bodyId} onclick={toggle}>{isExpanded ? 'Show less' : 'Show more'}</button>{/if}
    {/if}
    {@render footer?.()}
  </div>
</div>

<style>
  .message.feed { display: contents; }
  .message.dm { display: grid; grid-template-columns: var(--gutter, 56px) minmax(0, 1fr); padding: 2px var(--s4) 2px 0; margin-top: 14px; }
  .gutter { display: flex; justify-content: center; padding-top: 2px; }
  .av { border-radius: 50%; height: 40px; }
  .side-time {
    font-size: var(--t-cap);
    color: var(--faint);
    opacity: 0;
    padding-top: 3px;
    font-variant-numeric: tabular-nums;
  }
  .message:hover .side-time, .message:focus-within .side-time { opacity: 1; }
  .body { min-width: 0; }
  header { display: flex; align-items: baseline; flex-wrap: wrap; column-gap: 8px; line-height: var(--leading-tight); }
  .name { font-weight: 600; font-size: var(--t-lead); }
  .name:hover { text-decoration: underline; }
  .run-name { color: var(--gold); text-decoration: none; }
  .run-name:hover { text-decoration: underline; }
  .tag {
    font-size: var(--t-cap);
    font-weight: 600;
    color: var(--muted);
    background: var(--raise);
    border: 1px solid var(--line);
    border-radius: 4px;
    padding: 0 5px;
    line-height: 16px;
    align-self: center;
  }
  time { font-size: var(--t-sm); color: var(--faint); }
  .to { font-size: var(--t-sm); color: var(--faint); display: inline-flex; gap: var(--s1); align-items: baseline; flex-wrap: wrap; }
  .arrow { color: var(--faint); }
  .group { color: var(--muted); font-family: var(--mono); font-size: var(--t-cap); }
  .rcpt { color: var(--muted); transition: color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .rcpt:hover { color: var(--ink); text-decoration: underline; }
  }
  .comma { margin-left: -4px; }
  .prose { margin-top: 1px; }
  .body.turn-result .prose { overflow-wrap: break-word; }
  .message-toggle { color: var(--iris); font-size: var(--t-sm); margin-top: var(--s1); }
  .message-toggle:hover { text-decoration: underline; }
  .message.dm .name { font-size: var(--t-body); }
  .message.dm time { font-size: var(--t-cap); }
</style>
