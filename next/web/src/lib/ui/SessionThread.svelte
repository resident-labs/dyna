<script lang="ts">
  import { onDestroy, onMount, tick, untrack } from "svelte";
  import { call } from "../client.ts";
  import { decodeEvent } from "../event.ts";
  import { hue } from "../identity.ts";
  import { causes, formatTime } from "../feed.ts";
  import { watchTranscripts } from "../stream.ts";
  import type { PostRequest, TranscriptItem, TranscriptPart, TranscriptReadResponse, TranscriptTurn, TranscriptInput, TranscriptPage } from "../contract.gen.ts";
  import type { RoomStore } from "../room.svelte.ts";
  import Icon from "./Icon.svelte";
  import Avatar from "./Avatar.svelte";
  import StatusPill from "./StatusPill.svelte";
  import Markdown from "./Markdown.svelte";
  import PostAttachments from "./PostAttachments.svelte";
  import MediaViewer from "./MediaViewer.svelte";
  import { conversationMedia, type MediaItem, type MediaRow } from "../media.ts";
  import TranscriptWorkRow from "./TranscriptWorkRow.svelte";
  import TimelineMinimap from "./TimelineMinimap.svelte";
  import PostTarget from "./PostTarget.svelte";
  import { deriveTimelineGroups, finalTimelineItem, ownPost, postDestination, visibleTimelineText, formatTimelineDuration, mergeTimelineInputs, mergeTimelineItems, timelineInputText, timelineItemRunning, pendingStartBrief } from "../timeline.ts";

  let { session, roomName, store, unsent, onunsent, onsettled, onpostlocation, navigate, startingBrief, clock = Date.now }: { startingBrief?: string; session: string; roomName: string; store: RoomStore; unsent: PostRequest[]; onunsent: (request: PostRequest) => void; onsettled: (request: PostRequest) => void; onpostlocation: (room: string, seq: number) => void; navigate: (event: MouseEvent & { currentTarget: HTMLAnchorElement }) => void; clock?: () => number } = $props();
  const view = untrack(() => store.window({ kind: "dm", session }));
  view.onunsent = request => onunsent(request);
  view.onsettled = request => onsettled(request);
  $effect(() => {
    const shown = new Set(untrack(() => [...view.pendingPosts.values()].map(post => post.request)));
    for (const request of unsent) if (!shown.has(request)) view.unsent(request);
  });
  onDestroy(() => store.leave(view));
  export async function send(request: PostRequest) { await view.post(request); }
  // A closed session can have a transcript with no DM events, so the thread is empty only when neither has anything.
  export const empty = () => view.ready && !view.ordered.length && !loading && !turns.length;
  let turns: TranscriptTurn[] = $state([]);
  let pendingBrief = $derived(pendingStartBrief(startingBrief, turns));
  let expandedTurns: Record<string, boolean> = $state({});
  let before: string | undefined = $state();
  let now = $state(clock());
  $effect(() => {
    if (!turns.some(turn => !turn.ended)) return;
    now = clock();
    const timer = setInterval(() => { now = clock(); }, 1000);
    return () => clearInterval(timer);
  });
  let loading = $state(false), failed = $state(false);
  let pending = false;
  let scroller: HTMLDivElement;
  let frameWidth = $state(0), currentTurn = $state<string | null>(null);
  let visibleTurns: string[] = $state([]);
  let minimapTurns = $derived(turns.map(turn => ({ id: turn.id, userText: turn.inputs?.[0]?.text ?? null, assistantText: finalTimelineItem(turn.items ?? [], session)?.text ?? null, visible: visibleTurns.includes(turn.id) })));
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
  let loaded = $state(false);
  let destroyed = false;
  let lastSeq = $derived(Math.max(0, ...turns.flatMap(turn => [...(turn.inputs ?? []).map(input => input.seq), ...(turn.items ?? []).flatMap(item => [item.seq ?? 0, ...(item.posts ?? []).map(post => post.seq ?? 0)])])));
  let queuedPosts = $derived(view.posts().filter(post => post.seq > lastSeq));

  async function load(older = false) {
    if (loading) { if (!older) pending = true; return; }
    loading = true;
    try {
      const page = await call("transcript.read", { session, before: older ? before : undefined });
      if (destroyed) return;
      const incoming = page.turns ?? [];
      if (older) {
        const height = scroller.scrollHeight;
        turns = [...incoming.reverse(), ...turns];
        before = page.before;
        await tick();
        scroller.scrollTop += scroller.scrollHeight - height;
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
        if (loaded && atBottom) { await tick(); scroller.scrollTop = scroller.scrollHeight; }
      }
      failed = false;
      if (!loaded) { loaded = true; requestAnimationFrame(() => { if (scroller) scroller.scrollTop = scroller.scrollHeight; }); }
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
    void load();
    return () => { destroyed = true; cancelAnimationFrame(positionFrame); stop(); document.removeEventListener("visibilitychange", markRead); };
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
  ]).concat(queuedPosts.map(post => ({ key: post.id, event: post.id, room: post.room ?? store.room, from: post.from, at: post.at, attachments: post.body.attachments ?? [] }))));
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
</script>

{#snippet inputAttachments(input: TranscriptInput)}
  <div data-media-key={input.id}><PostAttachments onmedia={position => openMedia(input.id, position)} attachments={input.attachments ?? []} {newest} event={input.id} at={input.at} /></div>
{/snippet}

{#snippet postItem(turn: TranscriptTurn, item: TranscriptItem)}
  {@const room = item.room}
  {@const seq = item.seq}
  {@const from = item.from}
  {@const inbound = item.kind === "post" && !ownPost(item, session)}
  {@const to = item.kind === "post" ? postDestination(item, session, store.room, roomName, id => sender("session", id)) : undefined}
  <div data-media-key={item.id} class:agent-input={inbound} class:message={!inbound}>{#if inbound}<Avatar kind={from?.kind ?? "engine"} name={sender(from?.kind ?? "engine", from?.session)} size={32} />{/if}<div class="agent-body">{#if inbound}<div class="agent-sender">{#if from?.session}<a href={`/r/${encodeURIComponent(store.room)}/s/${encodeURIComponent(from.session)}`} onclick={navigate} style:--hue={hue(sender(from.kind, from.session))}>{sender(from.kind, from.session)}</a>{:else}<strong>{sender(from?.kind ?? "engine")}</strong>{/if}<time datetime={item.at}>{formatTime(item.at)}</time></div>{/if}{#if to}<div class="post-destination"><PostTarget {to} onjump={room && seq ? () => onpostlocation(room, seq) : undefined} /></div>{/if}<Markdown rhythm="thread" text={chunks[item.id + "/text"] ?? item.text} /><PostAttachments onmedia={position => openMedia(item.id, position)} attachments={item.attachments ?? []} {newest} event={item.event ?? item.id} at={item.at} /></div></div>
  {#if chunkFailed[item.id + "/text"]}<button onclick={() => void fetchPart(turn.id, item.id, "text")}>Text did not load. Try again</button>{/if}
{/snippet}

{#if mediaIndex !== undefined}<MediaViewer room={store.room} {loadVisual} items={media} initial={mediaIndex} name={mediaName} root={mediaRoot} onclose={() => mediaIndex = undefined} onjump={key => scroller.querySelector<HTMLElement>(`[data-media-key="${CSS.escape(key)}"]`)?.scrollIntoView({ block: "center" })} />{/if}

<div class="thread-frame" bind:clientWidth={frameWidth}>
{#if frameWidth >= 880}<TimelineMinimap turns={minimapTurns} current={currentTurn} onselect={(id) => scroller.querySelector<HTMLElement>(`article[data-turn="${CSS.escape(id)}"]`)?.scrollIntoView({ block: "start", behavior: "smooth" })} />{/if}
<div class="thread" bind:this={scroller} onscroll={() => { atBottom = scroller.scrollHeight - scroller.scrollTop - scroller.clientHeight < 80; updatePosition(); if (loaded && before && !loading && scroller.scrollTop < 100) void load(true); }}>
  <div class="body">
    {#if pendingBrief}<div class="owner-message"><Markdown rhythm="thread" text={pendingBrief} /></div>{/if}
    {#each turns as turn (turn.id)}
      {@const hasWork = !!turn.olderItems || (turn.items ?? []).some(item => item.kind === "tool" || item.kind === "thinking")}
      {@const finalText = turn.ended ? finalTimelineItem(turn.items ?? [], session)?.id : undefined}
      <article data-turn={turn.id}>
        {#each turn.inputs ?? [] as input (input.id)}
          {#if input.from.kind === "owner"}
            <div class="owner-message" title={formatTime(input.at)}><Markdown text={timelineInputText(input, chunks["input:" + input.id + "/text"])} />{@render inputAttachments(input)}{#if chunkFailed["input:" + input.id + "/text"]}<button onclick={() => void fetchPart(turn.id, "input:" + input.id, "text")}>Text did not load. Try again</button>{/if}</div>
          {:else}
            <div class="agent-input"><Avatar kind={input.from.kind} name={sender(input.from.kind, input.from.session)} size={32} /><div class="agent-body"><div class="agent-sender">{#if input.from.session}<a href={`/r/${encodeURIComponent(store.room)}/s/${encodeURIComponent(input.from.session)}`} onclick={navigate} style:--hue={hue(sender(input.from.kind, input.from.session))}>{sender(input.from.kind, input.from.session)}</a>{:else}<strong>{sender(input.from.kind, input.from.session)}</strong>{/if}<time datetime={input.at}>{formatTime(input.at)}</time></div><Markdown text={timelineInputText(input, chunks["input:" + input.id + "/text"])} />{@render inputAttachments(input)}{#if chunkFailed["input:" + input.id + "/text"]}<button onclick={() => void fetchPart(turn.id, "input:" + input.id, "text")}>Text did not load. Try again</button>{/if}</div></div>
          {/if}
        {/each}
        <!-- Dyna: Posts and the final answer remain visible outside the work fold. -->
        {#if turn.ended && hasWork}
          <button class="work-header" aria-expanded={!!expandedTurns[turn.id]} onclick={() => { expandedTurns[turn.id] = !expandedTurns[turn.id]; if (expandedTurns[turn.id] && turn.olderItems) void readContinuation(turn, "older"); }}>Worked for {formatTimelineDuration(Date.parse(turn.ended) - Date.parse(turn.started))}<span class:open={expandedTurns[turn.id]}><Icon name="chevron" size={14} /></span></button>
        {/if}
        {#each deriveTimelineGroups(turn.items ?? []) as group (group.id)}
          {#if group.kind === "work"}
            {#if !turn.ended || expandedTurns[turn.id]}
            <TranscriptWorkRow items={group.items} ended={!!turn.ended} started={turn.started} {now} {chunks}
              onopen={() => { if (turn.olderItems) void readContinuation(turn, "older"); }}
              onload={async (item, part) => { if (!await fetchPart(turn.id, item.id, part)) throw new Error("Part did not load"); }} />
            {/if}
          {:else if group.kind === "posts"}
            {#each group.item.posts ?? [] as post (post.id)}
              {@render postItem(turn, post)}
            {/each}
          {:else if group.item.kind === "text" || group.item.kind === "post"}
            {#if group.item.kind === "post" || visibleTimelineText(group.item.id, finalText, !!turn.ended, hasWork, !!expandedTurns[turn.id])}{@render postItem(turn, group.item)}{/if}
          {:else if group.item.kind === "too_large"}<p class="muted">This item is too large to show.</p>{/if}
        {/each}
        {#if turn.outcome === "failed"}<div class="failure"><Icon name="warning" size={16} /><div><strong>{turn.cause ? causes[turn.cause] : "The turn failed"}</strong></div></div>{/if}
        {#if !turn.ended && !(turn.items ?? []).some(item => timelineItemRunning(item, false))}<p class="muted working"><span class="spinner" aria-hidden="true"></span>Working for {formatTimelineDuration(now - Date.parse(turn.started))}</p>{/if}
      </article>
    {:else}{#if !loading && !failed && !pendingBrief}<p class="muted">The conversation will appear here.</p>{/if}{/each}
    {#each queuedPosts as post (post.id)}
      {@const own = post.from.kind === "session" && post.from.session === session}
      <div class:input={!own} class:message={own} data-media-key={post.id}>{#if !own}<div class="who"><Avatar kind={post.from.kind} name={sender(post.from.kind, post.from.session)} size={22} /><strong>{sender(post.from.kind, post.from.session)}</strong></div>{/if}<Markdown rhythm="thread" text={post.body.text} /><PostAttachments onmedia={position => openMedia(post.id, position)} attachments={post.body.attachments ?? []} {newest} event={post.id} at={post.at} /></div>
    {/each}
    {#each [...view.pendingPosts.values()] as post (post.id)}
      <div class="input" class:pending={!post.failed}><strong>You</strong><Markdown rhythm="thread" text={post.request.text} /><PostAttachments onmedia={() => {}} attachments={post.request.attachments ?? []} {newest} />{#if post.failed}<StatusPill tone="failed" label="Not sent" word /> <button onclick={() => void view.retry(post.id)}>Retry</button> <button onclick={() => view.discard(post.id)}>Discard</button>{/if}</div>
    {/each}
    {#if failed}<p class="muted" role="alert">The conversation did not load. <button onclick={() => void load()}>Try again</button></p>{/if}
  </div>
</div>
{#if !atBottom}<div class="latest-bar"><button class="latest" onclick={() => scroller.scrollTo({ top: scroller.scrollHeight, behavior: "smooth" })}>Jump to latest</button></div>{/if}
</div>

<style>
  .thread-frame { position: relative; display: flex; flex-direction: column; height: 100%; min-height: 0; }
  .thread { flex: 1; min-height: 0; overflow-y: auto; }
  .body { max-width: 784px; margin: auto; padding: 16px 24px; }
  article { padding-bottom: 20px; margin-bottom: 20px; border-bottom: 1px solid var(--line); }
  .failure { display: flex; gap: 10px; color: var(--muted); margin: 12px 0; }
  .failure strong { color: var(--text); }
  .spinner { display: inline-block; width: 12px; height: 12px; border: 1.5px solid var(--muted); border-top-color: transparent; border-radius: 50%; animation: spin 1s linear infinite; }
  /* The spinner sits in the work rows' 24 px icon slot, so the words line up with theirs. */
  .working .spinner { margin: 0 12px 0 6px; }
  @keyframes spin { to { transform: rotate(360deg); } }
  @media (prefers-reduced-motion: reduce) { .spinner { animation: none; } }
  .agent-input { display: grid; grid-template-columns: 32px minmax(0, 1fr); gap: 12px; margin: 14px 0; }
  .agent-body { min-width: 0; }
  .agent-sender { display: flex; align-items: baseline; gap: 8px; }
  .agent-sender a { color: oklch(var(--id-name) var(--hue)); font-weight: 600; text-decoration: none; }
  .agent-sender a:hover { text-decoration: underline; }
  .owner-message { width: fit-content; max-width: 75%; margin: 12px 0 12px auto; padding: 12px 16px; background: var(--raised); border-radius: 14px; }
  .input { background: var(--raised); padding: 12px 16px; border-radius: 14px; margin: 12px 0; }
  p { white-space: pre-wrap; overflow-wrap: anywhere; line-height: 1.45; }
  .who { display: flex; align-items: center; gap: 8px; }
  .who strong, .who .muted { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .who .identity { flex-shrink: 0; max-width: 60%; color: oklch(var(--id-name) var(--hue)); }
  .who time { flex: none; margin-left: auto; color: var(--muted); font-size: 11.5px; font-variant-numeric: tabular-nums; white-space: nowrap; }
  .input :global(.markdown) { margin: 4px 0 0; }
  .pending { opacity: 0.5; }
  .message { margin: 20px 0 12px; }
  .post-destination { display: flex; min-width: 0; margin-bottom: 6px; }
  .work-header { display: flex; align-items: center; gap: 4px; width: 100%; color: var(--muted); font-size: 14px; margin-top: 40px; padding: 8px 4px 12px; border-bottom: 1px solid var(--line); }
  .work-header span { display: inline-flex; transform: rotate(-90deg); }
  .work-header span.open { transform: none; }
  .muted { color: var(--muted); }
  button { padding: 0; border: 0; background: none; color: var(--muted); font: inherit; cursor: pointer; }
  button:hover { color: var(--text); }
  button:focus-visible { outline: 2px solid var(--focus); }
  .latest-bar { position: absolute; inset: auto 0 8px; display: flex; justify-content: center; pointer-events: none; }
  .latest-bar button { pointer-events: auto; }
  .latest { background: var(--raised); padding: 8px 12px; border-radius: 6px; }
  @media (max-width: 600px) { .body { padding: 12px 16px; } }
  @media (pointer: coarse) { button { min-height: 40px; } }
</style>
