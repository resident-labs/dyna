<script lang="ts">
  import { onDestroy, onMount, tick, untrack, type Snippet } from "svelte";
  import { call } from "../client.ts";
  import { decodeEvent } from "../event.ts";
  import { hue } from "../identity.ts";
  import { causes, dayLabel, formatTime } from "../feed.ts";
  import { watchTranscripts } from "../stream.ts";
  import type { Attachment, Principal, PostRequest, TranscriptItem, TranscriptPart, TranscriptReadResponse, TranscriptTurn, TranscriptPage } from "../contract.gen.ts";
  import type { RoomStore } from "../room.svelte.ts";
  import Icon from "./Icon.svelte";
  import Avatar from "./Avatar.svelte";
  import Markdown from "./Markdown.svelte";
  import PostAttachments from "./PostAttachments.svelte";
  import MediaViewer from "./MediaViewer.svelte";
  import { conversationMedia, type MediaItem, type MediaRow } from "../media.ts";
  import TranscriptWorkRow from "./TranscriptWorkRow.svelte";
  import TimelineMinimap from "./TimelineMinimap.svelte";
  import PostTarget from "./PostTarget.svelte";
  import { deriveTimelineGroups, finalTimelineItem, ownPost, postDestination, visibleTimelineText, formatTimelineDuration, mergeTimelineInputs, mergeTimelineItems, queuedTimelinePosts, timelineInputText, timelineItemRunning, pendingStartBrief } from "../timeline.ts";

  let { session, roomName, store, unsent, onunsent, onsettled, onpostlocation, navigate, startingBrief, clock = Date.now }: { startingBrief?: string; session: string; roomName: string; store: RoomStore; unsent: PostRequest[]; onunsent: (request: PostRequest) => void; onsettled: (request: PostRequest) => void; onpostlocation: (room: string, seq: number) => void; navigate: (event: MouseEvent & { currentTarget: HTMLAnchorElement }) => void; clock?: () => number } = $props();
  const view = untrack(() => store.window({ kind: "dm", session }));
  view.onunsent = request => onunsent(request);
  view.onsettled = request => onsettled(request);
  $effect(() => {
    const shown = new Set(untrack(() => [...view.pendingPosts.values()].map(post => post.request)));
    for (const request of unsent) if (!shown.has(request)) view.unsent(request);
  });
  onDestroy(() => store.leave(view));
  // A sent message shows at the end, so the thread goes there with it.
  export async function send(request: PostRequest) { atBottom = true; await view.post(request); }
  // A closed session can have a transcript with no DM events, so the thread is empty only when neither has anything.
  export const empty = () => view.ready && !view.ordered.length && !loading && !turns.length;
  let turns: TranscriptTurn[] = $state([]);
  let pendingBrief = $derived(pendingStartBrief(startingBrief, turns));
  let expandedTurns: Record<string, boolean> = $state({});
  let before: string | undefined = $state();
  let now = $state(clock());
  // An open turn of a closed session or a failed box never ends, so it does not count as running.
  let closed = $derived(store.ready && !store.sessions.has(session));
  let stalled = $derived(closed || store.boxFailures.has(session));
  $effect(() => {
    if (stalled || !turns.some(turn => !turn.ended)) return;
    now = clock();
    const timer = setInterval(() => { now = clock(); }, 1000);
    return () => clearInterval(timer);
  });
  let loading = $state(false), failed = $state(false);
  let pending = false;
  let scroller: HTMLDivElement, content: HTMLDivElement;
  let frameWidth = $state(0), currentTurn = $state<string | null>(null);
  let visibleTurns: string[] = $state([]);
  let minimapTurns = $derived(turns.map(turn => ({ id: turn.id, userText: turn.inputs?.[0] ? timelineInputText(turn.inputs[0], chunks["input:" + turn.inputs[0].id + "/text"]) : null, assistantText: finalTimelineItem(turn.items ?? [], session)?.text ?? null, visible: visibleTurns.includes(turn.id) })));
  let positionFrame = 0;
  function updatePosition() {
    if (positionFrame) return;
    positionFrame = requestAnimationFrame(() => {
    positionFrame = 0;
    const bounds = scroller.getBoundingClientRect();
    const visible = [...scroller.querySelectorAll<HTMLElement>("article[data-turn]")].filter(el => { const rect = el.getBoundingClientRect(); return rect.bottom > bounds.top && rect.top < bounds.bottom; });
    visibleTurns = visible.flatMap(el => el.dataset.turn ? [el.dataset.turn] : []);
    currentTurn = visibleTurns[0] ?? null;
    });
  }
  let atBottom = $state(true);
  let lastTop = 0, clicked = -Infinity;
  const gap = () => scroller.scrollHeight - scroller.scrollTop - scroller.clientHeight;
  // Scrolling up leaves the end at once, so new content never pulls the owner back; coming near it again follows it.
  function scrolled() {
    atBottom = gap() < (scroller.scrollTop < lastTop ? 2 : 80);
    lastTop = scroller.scrollTop;
    updatePosition();
    if (loaded && before && !loading && scroller.scrollTop < 100) void load(true);
  }
  // The thread stays at its end as it grows or the composer resizes. A fold the owner opens stays where it was clicked.
  function resized() {
    if (atBottom && performance.now() - clicked > 200) scroller.scrollTop = scroller.scrollHeight;
    else if (atBottom) atBottom = gap() < 80;
    updatePosition();
  }
  let loaded = $state(false);
  let destroyed = false;
  let queuedPosts = $derived(queuedTimelinePosts(view.posts(), turns));
  let pendingPosts = $derived([...view.pendingPosts.values()]);
  let quiet = $derived(!turns.length && !queuedPosts.length && !pendingPosts.length && !pendingBrief);
  // The first message after a turn may start a new day. It waits outside any turn, so its day mark is here too.
  let waitingSince = $derived(queuedPosts[0]?.at ?? pendingPosts[0]?.at);

  async function load(older = false) {
    if (loading) { if (!older) pending = true; return; }
    loading = true;
    try {
      const page = await call("transcript.read", { session, before: older ? before : undefined });
      if (destroyed) return;
      const incoming = page.turns ?? [];
      if (older) {
        // Measured from the end, so this holds with or without the browser's own scroll anchoring.
        const end = scroller.scrollHeight - scroller.scrollTop;
        turns = [...incoming.reverse(), ...turns];
        before = page.before;
        await tick();
        scroller.scrollTop = scroller.scrollHeight - end;
      } else {
        const ids = new Set(incoming.map(turn => turn.id));
        const overlaps = turns.some(turn => ids.has(turn.id));
        const retained = overlaps ? turns.filter(turn => !ids.has(turn.id)) : [];
        const old = new Map(turns.map(turn => [turn.id, turn]));
        turns = [...retained, ...incoming.reverse().map(turn => {
          const previous = old.get(turn.id);
          return previous ? { ...turn, inputs: mergeTimelineInputs(previous.inputs ?? [], turn.inputs ?? []), items: mergeTimelineItems(previous.items ?? [], turn.items ?? []), olderItems: previous.olderItems || turn.olderItems, nextItem: previous.olderItems ? previous.nextItem : turn.nextItem,
            ...(paging.has(turn.id + "/inputs") ? { after: previous.after } : {}),
            ...(paging.has(turn.id + "/items") ? { mandatoryItems: previous.mandatoryItems, nextMandatory: previous.nextMandatory } : {}),
            ...(paging.has(turn.id + "/posts") ? { mandatoryPosts: previous.mandatoryPosts, nextMandatoryPost: previous.nextMandatoryPost } : {}),
            ...(paging.has(turn.id + "/older") ? { olderItems: previous.olderItems, nextItem: previous.nextItem } : {}) } : turn;
        })];
        if (!retained.length) before = page.before;
      }
      failed = false;
      loaded = true;
    } catch { failed = true; }
    finally {
      loading = false;
      if (pending && !destroyed) { pending = false; void load(); }
      else if (!failed && !destroyed && before) {
        await tick();
        if (!destroyed && !loading && scroller.scrollHeight <= scroller.clientHeight) void load(true);
      }
    }
  }
  function markRead() {
    if (loaded && atBottom && view.ready && view.atPresent && document.visibilityState === "visible") store.see({ kind: "dm", session }, view.newest);
  }
  $effect(markRead);
  onMount(() => {
    document.addEventListener("visibilitychange", markRead);
    const stop = watchTranscripts(id => { if (!id || id === session) void load(); });
    const sizes = new ResizeObserver(resized);
    sizes.observe(scroller);
    sizes.observe(content);
    void load();
    return () => { destroyed = true; cancelAnimationFrame(positionFrame); stop(); sizes.disconnect(); document.removeEventListener("visibilitychange", markRead); };
  });
  const paging = new Set<string>();
  type PageKind = "items" | "posts" | "older";
  const pageCursors: Record<PageKind, { page: TranscriptPage; cursor: (turn: TranscriptTurn) => number; more: (turn: TranscriptTurn) => boolean; patch: (turn: TranscriptTurn) => Partial<TranscriptTurn> }> = {
    items: { page: "mandatory_items", cursor: turn => turn.nextMandatory ?? 0, more: turn => !!turn.mandatoryItems, patch: turn => ({ mandatoryItems: turn.mandatoryItems, nextMandatory: turn.nextMandatory }) },
    posts: { page: "mandatory_posts", cursor: turn => turn.nextMandatoryPost ?? 0, more: turn => !!turn.mandatoryPosts, patch: turn => ({ mandatoryPosts: turn.mandatoryPosts, nextMandatoryPost: turn.nextMandatoryPost }) },
    older: { page: "older_items", cursor: turn => turn.nextItem ?? 0, more: turn => !!turn.olderItems, patch: turn => ({ olderItems: turn.olderItems, nextItem: turn.nextItem }) },
  };
  function readContinuation(turn: TranscriptTurn, kind: PageKind) {
    const key = turn.id + "/" + kind;
    if (paging.has(key)) return;
    paging.add(key);
    const pageKind = pageCursors[kind];
    void (async () => {
      let cursor = pageKind.cursor(turn);
      for (;;) {
        const page = await call("transcript.read", { session, turn: turn.id, page: pageKind.page, cursor });
        const next = page.turns?.[0];
        if (!next) throw new Error("Missing transcript page");
        turns = turns.map(item => item.id === turn.id ? { ...item, items: mergeTimelineItems(item.items ?? [], next.items ?? []), ...pageKind.patch(next) } : item);
        if (!pageKind.more(next)) break;
        const nextCursor = pageKind.cursor(next);
        if (nextCursor === cursor) throw new Error("Transcript cursor did not advance");
        cursor = nextCursor;
      }
    })().catch(() => { failed = true; }).finally(() => { paging.delete(key); });
  }
  function readInputs(turn: TranscriptTurn) {
    const key = turn.id + "/inputs";
    if (paging.has(key)) return;
    paging.add(key);
    void (async () => {
      let after = turn.after;
      while (after) {
        const page = await call("transcript.read", { session, turn: turn.id, afterItem: after });
        const next = page.turns?.[0];
        if (!next || next.after === after) throw new Error("Transcript input cursor did not advance");
        turns = turns.map(item => item.id === turn.id ? { ...item, ...next, after: next.after, inputs: mergeTimelineInputs(item.inputs ?? [], next.inputs ?? []), items: mergeTimelineItems(item.items ?? [], next.items ?? []),
          ...(paging.has(turn.id + "/items") ? pageCursors.items.patch(item) : {}),
          ...(paging.has(turn.id + "/posts") ? pageCursors.posts.patch(item) : {}),
          ...(paging.has(turn.id + "/older") ? pageCursors.older.patch(item) : {}) } : item);
        after = next.after;
      }
    })().catch(() => { failed = true; }).finally(() => { paging.delete(key); });
  }
  // Dyna: Mandatory overflow pages load immediately; older tool pages load on disclosure.
  $effect(() => {
    for (const turn of turns) {
      if (turn.after) void readInputs(turn);
      if (turn.mandatoryItems) void readContinuation(turn, "items");
      if (turn.mandatoryPosts) void readContinuation(turn, "posts");
    }
  });
  let chunks: Record<string, string> = $state({});
  let chunkFailed: Record<string, boolean> = $state({});
  const fetching = new Map<string, Promise<boolean>>();
  // Reports whether the part loaded. A caller that arrives during a read shares its result.
  function fetchPart(turn: string, item: string, part: TranscriptPart): Promise<boolean> {
    const key = item + "/" + part;
    const running = fetching.get(key);
    if (running) return running;
    const read = (async () => {
      let offset: number | undefined = 0, value = "";
      try {
        do {
          const page: TranscriptReadResponse = await call("transcript.read", { session, turn, item, part, offset });
          value += page.chunk ?? "";
          offset = page.nextOffset;
        } while (offset !== undefined);
        chunks[key] = value;
        chunkFailed[key] = false;
        return true;
      } catch { chunkFailed[key] = true; return false; }
      finally { fetching.delete(key); }
    })();
    fetching.set(key, read);
    return read;
  }
  // Dyna: Text can exceed the page budget; load its full content without a disclosure.
  $effect(() => {
    for (const turn of turns) {
      for (const input of turn.inputs ?? []) {
        const key = "input:" + input.id + "/text";
        if (input.more && chunks[key] === undefined && !chunkFailed[key]) void fetchPart(turn.id, "input:" + input.id, "text");
      }
      for (const item of turn.items ?? []) {
        const messages = item.kind === "text" || item.kind === "post" ? [item] : item.posts ?? [];
        for (const message of messages) {
          const key = message.id + "/text";
          if (message.more && chunks[key] === undefined && !chunkFailed[key]) void fetchPart(turn.id, message.id, "text");
        }
      }
    }
  });
  let mediaRows: MediaRow[] = $derived(turns.flatMap(turn => [
    ...(turn.inputs ?? []).map(input => ({ key: input.id, event: input.id, room: input.room, from: input.from, at: input.at, attachments: input.attachments ?? [] })),
    ...(turn.items ?? []).flatMap(item => [item, ...(item.posts ?? [])]).map(item => ({ key: item.id, event: item.event ?? item.id, room: item.room ?? store.room, from: item.from ?? { kind: "engine" as const }, at: item.at, attachments: item.attachments ?? [] })),
  ]).concat(queuedPosts.map(post => ({ key: post.id, event: post.id, room: post.room ?? store.room, from: post.from, at: post.at, attachments: post.body.attachments ?? [] })))
    .concat(pendingPosts.map(post => ({ key: pendingKey(post.id), event: pendingKey(post.id), room: store.room, from: { kind: "owner" as const }, at: post.at, attachments: post.request.attachments ?? [] }))));
  let media = $derived(conversationMedia([...mediaRows].sort((a, b) => a.at.localeCompare(b.at))));
  let mediaIndex: number | undefined = $state();
  function openMedia(key: string, position: number) { const index = media.findIndex(item => item.key === key && item.position === position); if (index >= 0) mediaIndex = index; }
  const mediaName = (id: string) => store.names.get(id) ?? "Session";
  const mediaRoot = (id?: string) => { const entry = id ? store.sessions.get(id) : undefined; return !!entry && !entry.parent; };
  let newest = $derived(new Map(turns.flatMap(turn => (turn.items ?? []).flatMap(item => [item, ...(item.posts ?? [])]).flatMap(item => (item.attachments ?? []).flatMap(file => file.visual ? [[file.visual.id, file.visual] as const] : [])))));
  async function loadVisual(item: MediaItem) {
    const matches = media.filter(entry => entry.file.visual?.id === item.file.visual?.id && entry.event);
    const versions = await Promise.all(matches.map(async entry => {
      const page = await call("events.list", { room: entry.room ?? store.room, event: entry.event, afterSeq: 0, limit: 1 });
      const event = page.events?.[0] && decodeEvent(page.events[0]);
      return event?.type === "post.created" && event.id === entry.event && entry.file.visual ? { event, visual: entry.file.visual } : undefined;
    }));
    return versions.filter(entry => entry !== undefined).reverse();
  }
  const sender = (kind: string, id?: string) => kind === "owner" ? "You" : id ? store.names.get(id) ?? "Session" : "Dyna";
  const sameDay = (a: string, b: string) => new Date(a).toDateString() === new Date(b).toDateString();
  const pendingKey = (id: number) => `pending:${id}`;
</script>

{#snippet message(key: string, text: string, attachments: Attachment[] | null | undefined, event?: string, at?: string)}
  <Markdown rhythm="thread" {text} />
  {#if attachments?.length}<PostAttachments onmedia={position => openMedia(key, position)} {attachments} {newest} {event} {at} />{/if}
{/snippet}

{#snippet textRetry(turn: TranscriptTurn, item: string)}
  {#if chunkFailed[item + "/text"]}<button class="retry" onclick={() => void fetchPart(turn.id, item, "text")}>Text did not load. Try again</button>{/if}
{/snippet}

<!-- One layout from send to delivered: the states show around the bubble and never move it. -->
{#snippet ownerMessage(key: string, at: string | undefined, sending: boolean, body: Snippet, note?: Snippet)}
  <div class="owner-message" data-media-key={key}>
    <div class="bubble" class:sending title={at && formatTime(at)}>{@render body()}</div>
    {@render note?.()}
  </div>
{/snippet}

{#snippet inbound(key: string, from: Principal, at: string, body: Snippet)}
  {@const name = sender(from.kind, from.session)}
  <div class="agent-input" data-media-key={key}>
    <Avatar kind={from.kind} {name} size={32} />
    <div class="agent-body">
      <div class="agent-sender">{#if from.session}<a href={`/r/${encodeURIComponent(store.room)}/s/${encodeURIComponent(from.session)}`} onclick={navigate} style:--hue={hue(name)}>{name}</a>{:else}<strong>{name}</strong>{/if}<time datetime={at}>{formatTime(at)}</time></div>
      {@render body()}
    </div>
  </div>
{/snippet}

{#snippet postItem(turn: TranscriptTurn, item: TranscriptItem, working = false)}
  {@const to = item.kind === "post" ? postDestination(item, session, store.room, roomName, id => sender("session", id)) : undefined}
  {#snippet body()}
    {#if to}<div class="post-destination"><PostTarget {to} onjump={item.room && item.seq ? () => onpostlocation(item.room!, item.seq!) : undefined} /></div>{/if}
    {@render message(item.id, chunks[item.id + "/text"] ?? item.text ?? "", item.attachments, item.event ?? item.id, item.at)}
    {@render textRetry(turn, item.id)}
  {/snippet}
  {#if item.kind === "post" && !ownPost(item, session)}{@render inbound(item.id, item.from ?? { kind: "engine" }, item.at, body)}
  {:else}<div class="agent-message" class:working data-media-key={item.id}>{@render body()}</div>{/if}
{/snippet}

{#snippet dayMark(at: string, before?: string)}
  {#if !before || !sameDay(before, at)}<p class="day">{dayLabel(at, clock())}</p>{/if}
{/snippet}

{#if mediaIndex !== undefined}<MediaViewer room={store.room} {loadVisual} items={media} initial={mediaIndex} name={mediaName} root={mediaRoot} onclose={() => mediaIndex = undefined} onjump={key => scroller.querySelector<HTMLElement>(`[data-media-key="${CSS.escape(key)}"]`)?.scrollIntoView({ block: "center" })} />{/if}

<div class="thread-frame" bind:clientWidth={frameWidth}>
{#if frameWidth >= 880}<TimelineMinimap turns={minimapTurns} current={currentTurn} onselect={(id) => scroller.querySelector<HTMLElement>(`article[data-turn="${CSS.escape(id)}"]`)?.scrollIntoView({ block: "start", behavior: "smooth" })} />{/if}
<div class="thread" bind:this={scroller} onscroll={scrolled}>
  <div class="body" bind:this={content} onclickcapture={() => { clicked = performance.now(); }}>
    {#if before && loaded}<div class="skeleton" aria-hidden="true"><i></i></div>{/if}
    {#if !loaded && !failed && !pendingBrief}<div class="skeleton first" aria-hidden="true"><i></i><i></i><i></i></div>{/if}
    {#if pendingBrief}
      {@render dayMark(new Date(now).toISOString())}
      {#snippet brief()}<Markdown rhythm="thread" text={pendingBrief ?? ""} />{/snippet}
      {@render ownerMessage("brief", undefined, false, brief)}
    {/if}
    {#each turns as turn, index (turn.id)}
      {@const hasWork = !!turn.olderItems || (turn.items ?? []).some(item => item.kind === "tool" || item.kind === "thinking")}
      {@const finalText = turn.ended ? finalTimelineItem(turn.items ?? [], session)?.id : undefined}
      {@render dayMark(turn.started, turns[index - 1]?.started)}
      <article data-turn={turn.id} class:ended={turn.ended || stalled}>
        {#each turn.inputs ?? [] as input (input.id)}
          {#snippet body()}
            {@render message(input.id, timelineInputText(input, chunks["input:" + input.id + "/text"]), input.attachments, input.id, input.at)}
            {@render textRetry(turn, "input:" + input.id)}
          {/snippet}
          {#if input.from.kind === "owner"}{@render ownerMessage(input.id, input.at, false, body)}
          {:else}{@render inbound(input.id, input.from, input.at, body)}{/if}
        {/each}
        <!-- Dyna: Posts and the final answer remain visible outside the work fold. -->
        {#if turn.ended && hasWork}
          <button class="work-header" aria-expanded={!!expandedTurns[turn.id]} onclick={() => { expandedTurns[turn.id] = !expandedTurns[turn.id]; if (expandedTurns[turn.id] && turn.olderItems) void readContinuation(turn, "older"); }}>Worked for {formatTimelineDuration(Date.parse(turn.ended) - Date.parse(turn.started))}<span class:open={expandedTurns[turn.id]}><Icon name="chevron" size={14} /></span></button>
        {/if}
        {#each deriveTimelineGroups(turn.items ?? []) as group (group.id)}
          {#if group.kind === "work"}
            {#if !turn.ended || expandedTurns[turn.id]}
            <TranscriptWorkRow items={group.items} ended={!!turn.ended || stalled} started={turn.started} {now} {chunks}
              onopen={() => { if (turn.olderItems) void readContinuation(turn, "older"); }}
              onload={async (item, part) => { if (!await fetchPart(turn.id, item.id, part)) throw new Error("Part did not load"); }} />
            {/if}
          {:else if group.kind === "posts"}
            {#each group.item.posts ?? [] as post (post.id)}
              {@render postItem(turn, post)}
            {/each}
          {:else if group.item.kind === "text" || group.item.kind === "post"}
            {#if group.item.kind === "post" || visibleTimelineText(group.item.id, finalText, !!turn.ended, hasWork, !!expandedTurns[turn.id])}{@render postItem(turn, group.item, !!turn.ended && hasWork && group.item.kind === "text" && group.item.id !== finalText)}{/if}
          {:else if group.item.kind === "too_large"}<p class="muted">This item is too large to show.</p>{/if}
        {/each}
        {#if turn.outcome === "failed"}<div class="failure"><span class="slot"><Icon name="warning" size={16} /></span>{turn.cause ? causes[turn.cause] : "The turn failed"}</div>
        {:else if turn.outcome === "stopped"}<p class="muted turn-note">Stopped</p>{/if}
        {#if !turn.ended && !stalled && !(turn.items ?? []).some(item => timelineItemRunning(item, false))}<p class="muted turn-note"><span class="slot"><span class="spinner" aria-hidden="true"></span></span>Working for {formatTimelineDuration(now - Date.parse(turn.started))}</p>{/if}
      </article>
    {/each}
    {#if waitingSince}{@render dayMark(waitingSince, turns.at(-1)?.started)}{/if}
    {#each queuedPosts as post (post.id)}
      {#snippet body()}{@render message(post.id, post.body.text, post.body.attachments, post.id, post.at)}{/snippet}
      {#snippet queued()}<p class="owner-note queued">Queued</p>{/snippet}
      {#if post.from.kind === "owner"}{@render ownerMessage(post.id, post.at, false, body, closed ? undefined : queued)}
      {:else if post.from.kind === "session" && post.from.session === session}<div class="agent-message" data-media-key={post.id}>{@render body()}</div>
      {:else}{@render inbound(post.id, post.from, post.at, body)}{/if}
    {/each}
    {#each pendingPosts as post (post.id)}
      {#snippet body()}{@render message(pendingKey(post.id), post.request.text, post.request.attachments)}{/snippet}
      {#snippet unsent()}<p class="owner-note unsent" role="alert">Not sent<button class="retry" onclick={() => void view.retry(post.id)}>Retry</button><button class="retry" onclick={() => view.discard(post.id)}>Discard</button></p>{/snippet}
      {@render ownerMessage(pendingKey(post.id), undefined, !post.failed, body, post.failed ? unsent : undefined)}
    {/each}
    {#if loaded && !failed && quiet}<p class="muted empty">The conversation will appear here.</p>{/if}
    {#if failed}<p class="muted" role="alert">The conversation did not load. <button class="retry" onclick={() => void load()}>Try again</button></p>{/if}
  </div>
  {#if !atBottom}<div class="latest-bar"><button class="latest" onclick={() => scroller.scrollTo({ top: scroller.scrollHeight, behavior: "smooth" })}>Jump to latest</button></div>{/if}
</div>
</div>

<style>
  .thread-frame { position: relative; display: flex; flex-direction: column; height: 100%; min-height: 0; }
  /* The gutter on both edges keeps the column centered over the composer, with or without a classic scrollbar. */
  .thread { flex: 1; min-height: 0; overflow-y: auto; scrollbar-gutter: stable both-edges; }
  .body { display: flex; flex-direction: column; gap: 12px; max-width: 784px; margin: auto; padding: 16px 24px; }
  article { display: flex; flex-direction: column; gap: 12px; min-width: 0; scroll-margin-top: 16px; }
  /* The only line in the thread: the end of a turn, once something follows it. */
  article.ended:not(:last-child) { margin-bottom: 12px; padding-bottom: 24px; border-bottom: 1px solid var(--line); }
  .day { margin: 0; color: var(--muted); font-size: 12.5px; font-weight: 600; font-variant-numeric: tabular-nums; text-align: center; }
  .owner-message { display: flex; flex-direction: column; align-items: flex-end; gap: 4px; }
  .bubble { max-width: 80%; min-width: 0; padding: 12px 16px; background: var(--raised); border-radius: 12px; }
  .bubble.sending { opacity: 0.5; }
  .owner-note { display: flex; gap: 12px; margin: 0; color: var(--muted); font-size: 12.5px; line-height: 1.25; }
  /* Most messages leave the queue at once. The note opens only for one that stays, so a normal send moves nothing. */
  .queued { overflow: hidden; animation: open-note 150ms ease 1s backwards; }
  @keyframes open-note { from { height: 0; opacity: 0; } to { height: 1.25em; opacity: 1; } }
  .unsent { color: var(--danger); }
  .unsent .retry { color: inherit; }
  .agent-message { min-width: 0; }
  .agent-message.working { color: var(--muted); }
  .post-destination { display: flex; min-width: 0; margin-bottom: 4px; }
  .agent-input { display: grid; grid-template-columns: 32px minmax(0, 1fr); gap: 12px; }
  .agent-body { min-width: 0; }
  .agent-sender { display: flex; align-items: baseline; gap: 8px; min-width: 0; }
  .agent-sender :is(a, strong) { min-width: 0; overflow: hidden; color: oklch(var(--id-name) var(--hue)); font-size: 14px; font-weight: 600; text-decoration: none; text-overflow: ellipsis; white-space: nowrap; }
  .agent-sender strong { color: var(--text); }
  .agent-sender a:hover { text-decoration: underline; }
  .agent-sender time { flex: none; color: var(--muted); font-size: 12.5px; font-variant-numeric: tabular-nums; }
  /* These rows share the work rows' grid: a 24 px icon slot and a 6 px gap, at their size. */
  .work-header, .turn-note, .failure { min-height: 24px; font-size: 14px; }
  .work-header { display: flex; align-items: center; align-self: flex-start; gap: 4px; color: var(--muted); }
  .work-header span { display: inline-flex; transform: rotate(-90deg); }
  .work-header span.open { transform: none; }
  .turn-note { display: flex; align-items: center; gap: 6px; margin: 0; }
  .failure { display: flex; gap: 6px; line-height: 24px; overflow-wrap: anywhere; }
  .failure .slot { color: var(--danger); }
  .slot { display: inline-flex; flex: none; align-items: center; justify-content: center; width: 24px; height: 24px; }
  .spinner { width: 12px; height: 12px; border: 1.5px solid var(--muted); border-top-color: transparent; border-radius: 50%; animation: spin 1s linear infinite; }
  @keyframes spin { to { transform: rotate(360deg); } }
  .muted { color: var(--muted); }
  .empty { margin: 0; text-align: center; }
  p { overflow-wrap: anywhere; line-height: 1.45; }
  /* A fast load shows nothing; a slow one shows the shape of the thread. */
  .skeleton { display: grid; gap: 12px; animation: appear 1ms 200ms backwards; }
  @keyframes appear { from { visibility: hidden; } }
  .skeleton i { width: 60%; height: 16px; border-radius: 6px; background: var(--hover); }
  .skeleton.first i:first-child { justify-self: end; width: 40%; height: 44px; border-radius: 12px; }
  .skeleton.first i:last-child { width: 40%; }
  button { padding: 0; border: 0; background: none; color: var(--muted); font: inherit; cursor: pointer; }
  button:hover { color: var(--text); }
  button:focus-visible { outline: 2px solid var(--focus); }
  /* Sticky inside the scroller and without height, so it centers on the column and changes no scroll length. */
  .latest-bar { position: sticky; bottom: 8px; display: flex; justify-content: center; height: 0; }
  .latest { flex: none; height: 28px; padding: 0 12px; transform: translateY(-100%); border: 1px solid var(--line); border-radius: 6px; background: var(--raised); color: var(--text); font-size: 13px; font-weight: 500; }
  @media (max-width: 600px) { .thread { scrollbar-gutter: stable; } .body { padding: 12px 16px; } }
  @media (pointer: coarse) { button { min-height: 40px; } .latest { height: 40px; } }
  @media (prefers-reduced-motion: reduce) {
    .spinner { animation: none; }
    .queued { animation-duration: 1ms; }
  }
</style>
