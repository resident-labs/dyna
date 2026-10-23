<script>
  import { speaker } from '../ui/posts.js'
  import { tick, onMount } from 'svelte'
  import HeldCommentNotice from '../prs/HeldCommentNotice.svelte'
  import { refreshHeldComments } from '../prs/heldComments.ts'
  onMount(() => { refreshHeldComments() })
  import { scale } from 'svelte/transition'
  import Avatar from '../ui/Avatar.svelte'
  import PinIcon from '../ui/PinIcon.svelte'
  import PostActions from '../ui/PostActions.svelte'
  import { postHandlers } from '../ui/postSelect.ts'
  import { createSelection } from '../ui/selection.svelte.ts'
  import ForwardQuote from '../ui/ForwardQuote.svelte'
  import HistoryList from './HistoryList.svelte'
  import { focusWhen } from '../ui/focusWhen.js'
  import PRPost from '../prs/PRPost.svelte'
  import GoalPost from './GoalPost.svelte'
  import CallRequestRow from '../voice/CallRequestRow.svelte'
  import CompletionBody from '../ui/CompletionBody.svelte'
  import { feedRows, feedPRRepos, callsInWindow } from './feedRows.js'
  import PostFiles from '../ui/PostFiles.svelte'
  import { postParts } from '../ui/postParts.js'
  import { questionID } from './questions.js'
  import { swipeReply } from '../ui/swipeReply.js'
  import { markdown, clock, stamp, dayLabel, short, resultLine } from '../ui/format.js'
  import { arrivals, motion, FAST } from '../ui/motion.js'
  import SystemNote from '../ui/SystemNote.svelte'
  import { noteGroups } from '../ui/notices.js'
  import { look } from './look.svelte.js'
  import { highlighted, oneLine, scopeOf } from './look.ts'

  let { room, label = 'general', intro = true, posts = [], questions = [], calls = [], goals = [], earlier = 0, newer = 0, onearlier, onnewer, onpresent, onaround, unreadFrom = '', view = 'general', pinned = new Set(), onaddress, onreveal, onreply, onforward, onpin, onopenfile, onissue = null, ondetail, onquestion, onacceptcall, ondeclinecall, ongoal, stuck = $bindable(true) } = $props()

  let scroller = $state()
  let list = $state()
  // On a touch screen there is no hover: a tap on a post shows its actions.
  // The one post whose action bar shows (postSelect.ts).
  const selection = createSelection()

  let rows = $derived(feedRows(posts, unreadFrom, callsInWindow(calls, posts, earlier, newer)))
  // Posts that arrive while the feed is open rise in; the feed opens still.
  const arrived = arrivals()
  let isNew = $derived(arrived(`${room?.id}:${view}`, rows.map((r) => r.key)))
  // A run of Dyna's notices shares one quiet note.
  let notes = $derived(noteGroups(rows, room?.name))

  let showRepo = $derived(feedPRRepos(posts).size > 1)
  let expanded = $state(new Set())
  let foldable = $state(new Set())

  function measureMessage(node, id) {
    const measure = () => {
      const line = parseFloat(getComputedStyle(node).lineHeight)
      const long = node.scrollHeight > line * 3 + 2
      if (foldable.has(id) === long) return
      const next = new Set(foldable)
      if (long) next.add(id)
      else next.delete(id)
      foldable = next
    }
    const observer = new ResizeObserver(measure)
    observer.observe(node)
    return { destroy: () => observer.disconnect() }
  }

  function toggleMessage(id) {
    const next = new Set(expanded)
    if (next.has(id)) next.delete(id)
    else next.add(id)
    expanded = next
  }

  let questionsById = $derived(Object.fromEntries(questions.map((q) => [q.id, q])))
  // Only questions to the owner wait for an answer here. Older questions
  // between agents read as plain posts.
  const ownerQuestion = (p) => {
    const id = questionID(p)
    return id && (questionsById[id]?.to ?? 'owner') === 'owner' ? id : ''
  }
  let byId = $derived(Object.fromEntries(posts.map((p) => [p.id, p])))

  // Stay pinned to the bottom when the feed's height changes (the composer
  // grows, a reply bar opens, the window resizes).
  $effect(() => {
    const el = scroller
    if (!el) return
    const ro = new ResizeObserver(() => {
      if (stuck) el.scrollTop = el.scrollHeight
    })
    ro.observe(el)
    return () => ro.disconnect()
  })

  // Also called by the Pinned panel, so a pin and a reply line scroll and
  // flash the same way.
  export async function jump(id) {
    // Leaving the bottom keeps the app from trimming the pages loaded here.
    stuck = false
    let i = rowOf(id)
    // Direct windows keep a distant reply or pin to one page request.
    if (i < 0 && onaround) {
      if (await onaround(id) === false) return
      await tick()
      i = rowOf(id)
    }
    if (i < 0 && onreveal) {
      // A hidden kind of post the reader asked for shows for good.
      onreveal(id)
      await tick()
      i = rowOf(id)
    }
    if (i < 0) return
    const n = ++jumps
    flash = { key: rows[i].key, id, n }
    list.scrollToIndex(i)
    // Compare the jump number: state wraps the object, so identity differs.
    setTimeout(() => { if (flash?.n === n) flash = null }, 1200)
  }

  // A folded PR row holds several posts; any of them finds the row.
  const rowOf = (id) => rows.findIndex((r) => r.post?.id === id || r.posts?.some((p) => p.id === id))

  // The row a jump went to, and the post in it. The list can draw a row
  // again while it settles the scroll, so the row takes its flash and focus
  // from this state each time it is drawn. A PR row passes the post to the
  // notice it belongs to.
  let flash = $state(null)
  let jumps = 0

  // The label comes from Go (roomsvc.SpeakerLabel): you, root, or a member.
  const who = (p) => speaker(p, room.name)

  function recipients(p) {
    const groups = (p.via || []).filter((v) => /^@(.*:)?(children|siblings|parent)$/.test(v))
    const names = (p.to || []).map((a) => short(a, room.name))
    return { groups, names }
  }

  // The feed choices for this channel (Settings, or its top-bar menu) pick
  // the highlighted posts and the finished turns that fold to one line.
  const scope = $derived(scopeOf(room.id, view))
  let opened = $state({}) // one-line results the owner opened

  function verb(p) {
    return p.kind === 'failed' ? 'failed' : p.kind === 'stopped' ? 'stopped' : 'finished'
  }

  function snippet(p) {
    const t = postParts(p).text.replace(/\s+/g, ' ').trim()
    return t.length > 90 ? t.slice(0, 90) + '…' : t
  }


</script>

{#snippet replyCue()}
  <span class="reply-cue" aria-hidden="true">
    <svg viewBox="0 0 20 20" width="18" height="18"><path d="M8 5 3 10l5 5M3.5 10H12a5 5 0 0 1 5 5v1" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" /></svg>
  </span>
{/snippet}

{#snippet completionPost(p)}
  {#if oneLine(look, scope, p) && !opened[p.id]}
    <span class="gutter"><span class="mark" aria-hidden="true">✓</span></span>
    <div class="body line">
      <button class="name hue-{p.fromHue}" onclick={() => onaddress(p.from)}>{who(p)}</button>
      <span class="what">{verb(p)}{p.turn ? ` turn ${p.turn}` : ''}</span>
      <span class="snip">{resultLine(postParts(p).text)}</span>
      <button class="open" onclick={() => (opened[p.id] = true)}>View</button>
    </div>
  {:else}

    <span class="gutter"><span class="mark" aria-hidden="true">{p.kind === 'failed' ? '✕' : p.kind === 'stopped' ? '⊘' : '✓'}</span></span>
    <div class="body">
      <div class="sysline">
        <button class="name hue-{p.fromHue}" onclick={() => onaddress(p.from)}>{who(p)}</button>
        <span class="what">{verb(p)}{p.turn ? ` turn ${p.turn}` : ''}</span>
        <time title={stamp(p.ts)}>{stamp(p.ts)}</time>
      </div>
      {#if p.text || p.detail}
        <div class="prose small"><CompletionBody post={p} trimLead onopen={ondetail} /></div>
      {/if}
    </div>
  {/if}
{/snippet}

<!-- The pane anchors the jump button to the feed, not to the composer below. -->
<div class="pane">
<div class="feed" bind:this={scroller} >
  {#if rows.length === 0}
    <p class="empty">No messages in this channel yet.</p>
  {/if}
  <!-- Only rows near the viewport render; jump() finds the others by index. -->
  {#key room.id + ':' + view}
  <HistoryList bind:this={list} bind:atBottom={stuck} {scroller} items={rows} key={(r) => r.key} estimate={(r) => (r.kind === 'post' ? 96 : 32)} {earlier} {newer} {onearlier} {onnewer} {onpresent} onhistory={() => (stuck = false)} beginning={intro ? 'Beginning of #' + label : ''}>
  {#snippet children(row, index)}
    {#if row.kind === 'day'}
      <div class="divider"><span>{dayLabel(row.ts)}</span></div>
    {:else if row.kind === 'new'}
      <div class="divider new"><span>New</span></div>
    {:else if row.kind === 'call'}
      <article class="post sysrow" class:enter={isNew(row.key)} data-id={row.key}>
        <CallRequestRow request={row.call} onaccept={onacceptcall} ondecline={ondeclinecall} />
      </article>

    {:else}
      {@const p = row.post}
      {@const parts = postParts(p)}
      <!-- Every stored post owns the same actions. Kind selects its body only. -->
      <!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_noninteractive_element_interactions, a11y_no_noninteractive_tabindex -->
      <article class="post {p.completion ? `sys ${p.kind}` : ''}" class:enter={isNew(row.key)} class:noterow={notes.has(index)} class:notemerged={notes.has(index) && !notes.get(index)} class:prrow={p.kind === 'pr'} class:sysrow={ownerQuestion(p) || ['goal', 'pr'].includes(p.kind)} class:after-sys={p.completion && rows[index - 1]?.post?.completion} class:cont={row.cont} class:unread={p.unread} class:forme={highlighted(look, scope, p, room.name)} class:flash={flash?.key === row.key && !row.posts} use:focusWhen={flash?.key === row.key && !row.posts} data-id={p.id} data-post-ids={row.posts?.map((post) => post.id).join(' ')} tabindex="0" data-select-id={p.id} use:swipeReply={() => onreply?.(p)} {...postHandlers(selection, p.id)}>
        {@render replyCue()}
        {#if (row.posts ?? [p]).some((post) => pinned.has(post.id))}<span class="post-pin" title="Pinned" transition:scale={{ start: 0.6, duration: motion(FAST) }}><PinIcon size={13} filled /></span>{/if}
          {#if p.replyTo}
            <button class="replyline" onclick={() => jump(p.replyTo)}>
              <span class="spine" aria-hidden="true"></span>
              {#if byId[p.replyTo]}
                <span class="rname hue-{byId[p.replyTo].fromHue}">{who(byId[p.replyTo])}</span>
                <span class="rtext">{snippet(byId[p.replyTo])}</span>
              {:else}
                <span class="rtext">Reply to an earlier post</span>
              {/if}
            </button>
          {/if}
      {#if notes.has(index)}
        {#if notes.get(index)}<SystemNote notes={notes.get(index)} roomId={room.id} {onopenfile} {onissue} />{/if}
      {:else if p.kind === 'goal'}
        <GoalPost post={p} {goals} {ongoal} />
      {:else if ownerQuestion(p)}
        {@const q = questionsById[ownerQuestion(p)]}
          <div class="sysline"><span class="question-mark" class:quiet={q && q.state !== 'open'}>?</span><button class="name hue-{p.fromHue}" onclick={() => onaddress(p.from)}>{who(p)}</button><span class="what">asked: {p.text.replace(/^\[QUESTION q\d+\] /, '').split('\n')[0]}</span><span>·</span>
            {#if !q || q.state === 'open'}<button class="answer-link" onclick={() => onquestion(p)}>Answer</button>
            {:else}<button class="question-state {q.state}" onclick={() => onquestion(p)}>{q.state}</button>{/if}
          </div>
      {:else if p.notice?.kind === 'held-comment'}
          <HeldCommentNotice roomId={room.id} prKey={p.notice.changeId} commentId={p.notice.commentId ?? ''} />
      {:else if p.kind === 'pr'}
          <PRPost post={p} posts={row.posts} {showRepo} focus={flash?.key === row.key ? flash.id : ''} to={recipients(p).names} {onaddress} {room} {pinned} {selection} {onreply} {onforward} {onpin} />
      {:else if p.completion}
        {@render completionPost(p)}
      {:else}
          <span class="gutter">
            {#if row.cont}
              <time class="side-time">{clock(p.ts)}</time>
            {:else}
              <button class="av" onclick={() => onaddress(p.from)} aria-label={who(p)}>
                <Avatar name={who(p)} hue={p.fromHue} owner={p.owner} emoji={p.fromEmoji} size={40} />
              </button>
            {/if}
          </span>
          <div class="body">
            {#if !row.cont}
              {@const r = recipients(p)}
              <header>
                <button class="name hue-{p.fromHue}" onclick={() => onaddress(p.from)}>{who(p)}</button>
                {#if p.owner && p.human}<span class="tag">human</span>{/if}
                <time title={new Date(p.ts).toLocaleString()}>{stamp(p.ts)}</time>
                <span class="to">
                  <span class="arrow" aria-hidden="true">→</span>
                  {#each r.groups as g}<span class="group">{g}</span>{/each}
                  {#each r.names as n, i}<button class="rcpt" onclick={() => onaddress(p.to[i])}>{n}</button>{#if i < r.names.length - 1}<span class="comma">,</span>{/if}{/each}
                </span>
              </header>
            {/if}
            {#if parts.text}
              {#if p.human}
                <div class="prose">{@html markdown(parts.text)}</div>
              {:else}
                <div id={"message-" + p.id} class="prose agent-message" class:folded={!expanded.has(p.id)} use:measureMessage={p.id}>{@html markdown(parts.text)}</div>
                {#if foldable.has(p.id)}<button class="message-toggle" aria-expanded={expanded.has(p.id)} aria-controls={"message-" + p.id} onclick={() => toggleMessage(p.id)}>{expanded.has(p.id) ? 'Show less' : 'Show more'}</button>{/if}
              {/if}
            {/if}
          </div>
      {/if}
      {#if p.forward}<ForwardQuote {room} post={p.forward} author={byId[p.forward.id]} onpost={jump} {onopenfile} />{/if}
      {#if parts.attachments.length}<div class="post-files"><PostFiles roomId={room.id} post={p} {parts} onopen={onopenfile} /></div>{/if}
          {#if !notes.has(index) && p.kind !== 'pr'}<PostActions {room} {p} pinned={pinned.has(p.id)} show={selection.get() === p.id} {onreply} {onforward} {onpin} />{/if}
      </article>
    {/if}
  {/snippet}
  </HistoryList>
  {/key}
  <div class="tail"></div>
</div>
</div>

<style>
  .question-state.answered { color: var(--sage); }
  .question-state.withdrawn, .question-id, .question-mark.quiet { color: var(--faint); }
  .question-mark { color: var(--gold); font-weight: 700; }
  .answer-link { color: var(--gold); font-weight: 600; }
  .pane { flex: 1; min-height: 0; display: flex; flex-direction: column; position: relative; }
  .feed {
    flex: 1;
    overflow-y: auto;
    /* VirtualList keeps the reader's place; the browser must not move it too. */
    overflow-anchor: none;
    overflow-x: hidden;
    padding: 0 0 var(--s2);
    position: relative;
  }
  .empty { padding: var(--s1) var(--s4); color: var(--muted); }

  .divider {
    display: flex;
    align-items: center;
    margin: var(--s4) var(--s4) 6px;
    height: 0;
    border-top: 1px solid var(--line);
    justify-content: center;
  }
  .divider span {
    background: var(--main);
    padding: 0 6px;
    font-size: var(--t-sm);
    font-weight: 600;
    color: var(--faint);
    margin-top: -1px;
    transform: translateY(-50%);
  }
  .divider.new { border-color: var(--iris-edge); justify-content: flex-end; }
  .divider.new span { color: #18171a; background: var(--iris); border-radius: 4px; padding: 1px 6px; font-size: var(--t-cap); transform: translateY(-50%); }

  .post {
    position: relative;
    display: grid;
    grid-template-columns: 72px 1fr;
    padding: 2px 48px 2px 0;
    margin-top: 14px;
    transition: background-color var(--motion-fast) var(--ease);
  }
  /* System rows use their own compact layout. */
  .post.sysrow { display: block; padding-left: var(--s4); }
  .post.prrow > :global(.actions) { top: auto; bottom: calc(100% - 4px); }
  .post:global(.swipeable) { transform: var(--reply-transform, none); transition: transform var(--motion-fast) ease-out, background-color var(--motion-fast) var(--ease); }
  .post:global(.swipe-drag) { transition: none; user-select: none; }
  .reply-cue { position: absolute; right: -40px; top: var(--reply-cue-y, 50%); transform: translateY(-50%) scale(0.85); width: 32px; height: 32px; display: grid; place-items: center; border-radius: 50%; background: var(--raise); color: var(--muted); opacity: 0; transition: opacity var(--motion-fast), transform var(--motion-fast), background-color var(--motion-fast); pointer-events: none; }
  .post:global(.swipe-drag) .reply-cue { opacity: 1; }
  .post:global(.swipe-ready) .reply-cue { background: var(--iris); color: var(--rail); transform: translateY(-50%) scale(1); }
  .post.cont { margin-top: 0; }
  @media (hover: hover) {
    .post:hover { background: var(--hover); }
  }
  .post:active { background: var(--select); }
  .body.line { display: flex; align-items: baseline; gap: var(--s2); min-width: 0; font-size: var(--t-sm); color: var(--muted); }
  .body.line .snip { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .body.line .open { flex: none; color: var(--iris); font-size: var(--t-sm); transition: color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .body.line .open:hover { color: var(--iris-hover); }
  }
  .post.forme { background: var(--gold-wash); box-shadow: inset 2px 0 0 var(--gold); }
  @media (hover: hover) {
    .post.forme:hover { background: var(--hover); }
  }
  .post.forme:active { background: var(--select); }
  .post.flash { background: var(--iris-wash); }
  .gutter { display: flex; justify-content: center; padding-top: 2px; }
  .av { border-radius: 50%; height: 40px; }
  .side-time {
    font-size: var(--t-cap);
    color: var(--faint);
    opacity: 0;
    padding-top: 3px;
    font-variant-numeric: tabular-nums;
  }
  .post:hover .side-time { opacity: 1; }
  .body { min-width: 0; }
  header { display: flex; align-items: baseline; flex-wrap: wrap; column-gap: 8px; line-height: var(--leading-tight); }
  .name { font-weight: 600; font-size: var(--t-lead); }
  .name:hover { text-decoration: underline; }
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
  .agent-message.folded { max-height: 4.35em; overflow: hidden; }
  .message-toggle { color: var(--iris); font-size: var(--t-sm); margin-top: var(--s1); }
  .message-toggle:hover { text-decoration: underline; }
  .prose.small { font-size: var(--t-body); color: var(--muted); }

  .replyline {
    grid-column: 1 / -1;
    display: flex;
    align-items: center;
    gap: 6px;
    padding-left: 36px;
    font-size: var(--t-ui);
    color: var(--muted);
    text-align: left;
    min-width: 0;
    margin-bottom: 2px;
  }
  .spine {
    width: 28px;
    height: 10px;
    margin-top: var(--s2);
    border-left: 2px solid #4a474c;
    border-top: 2px solid #4a474c;
    border-top-left-radius: 6px;
    flex: none;
  }
  .rname { font-weight: 600; flex: none; }
  .rtext { white-space: nowrap; overflow: hidden; text-overflow: ellipsis; min-width: 0; }
  .replyline .rtext { transition: color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .replyline:hover .rtext { color: var(--ink); }
  }

  .sys { margin-top: var(--s2); padding-top: var(--s1); padding-bottom: var(--s1); }
  .sys.after-sys { margin-top: 0; }
  .mark {
    width: 22px;
    height: 22px;
    border-radius: 50%;
    display: grid;
    place-items: center;
    font-size: var(--t-sm);
    font-weight: 700;
    color: var(--sage);
    background: color-mix(in srgb, var(--sage) 16%, transparent);
  }
  .sys.failed .mark { color: var(--coral); background: color-mix(in srgb, var(--coral) 16%, transparent); }
  .sys.stopped .mark { color: var(--muted); background: var(--raise); }
  .sysline { display: flex; align-items: baseline; gap: 6px; flex-wrap: wrap; font-size: var(--t-body); }
  .sysline .name { font-size: var(--t-body); }
  .what { color: var(--muted); }
  .sys.failed .what { color: var(--coral); }


  .post-files { grid-column: 2; min-width: 0; }
  .post-pin { position: absolute; right: 18px; top: 5px; color: var(--gold); }
  .tail { height: 16px; }


  /* A Dyna notice is a quiet line, not a post card. */
  .noterow { display: block; padding: 0; background: none; }
  .notemerged { display: none; }
</style>
