<script lang="ts">
  import { onDestroy, onMount, tick, untrack } from "svelte";
  import { call } from "../client.ts";
  import { decodeEvent } from "../event.ts";
  import { hue } from "../identity.ts";
  import type { VisualVersion } from "../visual.ts";
  import { causes, formatTime } from "../feed.ts";
  import { watchTranscripts } from "../stream.ts";
  import type { PostRequest, TranscriptItem, TranscriptPart, TranscriptReadResponse, TranscriptTurn, TranscriptInput, VisualCard } from "../contract.gen.ts";
  import type { RoomStore } from "../room.svelte.ts";
  import Icon from "./Icon.svelte";
  import { elapsed } from "../steps.ts";
  import Avatar from "./Avatar.svelte";
  import StatusPill from "./StatusPill.svelte";
  import Markdown from "./Markdown.svelte";
  import PostAttachments from "./PostAttachments.svelte";
  import TranscriptWorkRow from "./TranscriptWorkRow.svelte";
  import TimelineMinimap from "./TimelineMinimap.svelte";
  import { deriveTimelineGroups, formatTimelineDuration, mergeTimelineInputs, mergeTimelineItems } from "../timeline.ts";

  let { session, roomName, store, unsent, onunsent, onsettled, onpostlocation, navigate, onvisual, clock = Date.now }: { session: string; roomName: string; store: RoomStore; unsent: PostRequest[]; onunsent: (request: PostRequest) => void; onsettled: (request: PostRequest) => void; onpostlocation: (room: string, seq: number) => void; navigate: (event: MouseEvent & { currentTarget: HTMLAnchorElement }) => void; onvisual: (versions: VisualVersion[], selected: number, send: (text: string, event: string) => Promise<void>) => void; clock?: () => number } = $props();
  const view = untrack(() => store.window({ kind: "dm", session }));
  view.onunsent = request => onunsent(request);
  view.onsettled = request => onsettled(request);
  $effect(() => {
    const shown = new Set(untrack(() => [...view.pendingPosts.values()].map(post => post.request)));
    for (const request of unsent) if (!shown.has(request)) view.unsent(request);
  });
  onDestroy(() => store.leave(view));
  export async function send(request: PostRequest) { await view.post(request); }
  let turns: TranscriptTurn[] = $state([]);
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
  let minimapTurns = $derived(turns.map(turn => ({ id: turn.id, userText: turn.inputs?.[0]?.text ?? null, assistantText: turn.items?.findLast(item => item.kind === "text")?.text ?? null, visible: visibleTurns.includes(turn.id) })));
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
  const pageCursors: Record<PageKind, { cursor: (turn: TranscriptTurn) => number; more: (turn: TranscriptTurn) => boolean; patch: (turn: TranscriptTurn) => Partial<TranscriptTurn> }> = {
    items: { cursor: turn => turn.nextMandatory ?? 0, more: turn => !!turn.mandatoryItems, patch: turn => ({ mandatoryItems: turn.mandatoryItems, nextMandatory: turn.nextMandatory }) },
    posts: { cursor: turn => turn.nextMandatoryPost ?? 0, more: turn => !!turn.mandatoryPosts, patch: turn => ({ mandatoryPosts: turn.mandatoryPosts, nextMandatoryPost: turn.nextMandatoryPost }) },
    older: { cursor: turn => turn.nextItem ?? 0, more: turn => !!turn.olderItems, patch: turn => ({ olderItems: turn.olderItems, nextItem: turn.nextItem }) },
  };
  function readContinuation(turn: TranscriptTurn, kind: PageKind) {
    const key = turn.id + "/" + kind;
    if (paging.has(key)) return;
    paging.add(key);
    const pageKind = pageCursors[kind];
    void (async () => {
      let cursor = pageKind.cursor(turn);
      for (;;) {
        const request = kind === "items" ? { mandatoryItems: true, mandatoryIndex: cursor } : kind === "posts" ? { mandatoryPosts: true, mandatoryPost: cursor } : { olderItems: true, itemIndex: cursor };
        const page = await call("transcript.read", { session, turn: turn.id, ...request });
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
  const fetching = new Set<string>();
  async function fetchPart(turn: string, item: string, part: TranscriptPart) {
    const key = item + "/" + part;
    if (fetching.has(key)) return;
    fetching.add(key);
    let offset: number | undefined = 0, value = "";
    try {
      do {
        const page: TranscriptReadResponse = await call("transcript.read", { session, turn, item, part, offset });
        value += page.chunk ?? "";
        offset = page.nextOffset;
      } while (offset !== undefined);
      chunks[key] = value;
      chunkFailed[key] = false;
    } catch { chunkFailed[key] = true; }
    finally { fetching.delete(key); }
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
  let newest = $derived(new Map(turns.flatMap(turn => (turn.items ?? []).flatMap(item => [item, ...(item.posts ?? [])]).flatMap(item => (item.attachments ?? []).flatMap(file => file.visual ? [[file.visual.id, file.visual] as const] : [])))));
  async function openVisual(visual: VisualCard, room: string, eventId: string) {
    const page = await call("events.list", { room, event: eventId, afterSeq: 0, limit: 1 });
    const event = page.events?.[0] && decodeEvent(page.events[0]);
    if (!event || event.id !== eventId || event.type !== "post.created") return;
    onvisual([{ event, visual }], visual.version, async (text, id) => { await call("post", { room, text, visualReply: id }); });
  }
  const sender = (kind: string, id?: string) => kind === "owner" ? "You" : id ? store.names.get(id) ?? "Session" : "Dyna";
</script>

{#snippet inputAttachments(input: TranscriptInput)}
  <PostAttachments attachments={input.attachments ?? []} {newest} event={input.id} at={input.at} onvisual={visual => void openVisual(visual, input.room, input.id)} />
{/snippet}

{#snippet postItem(turn: TranscriptTurn, item: TranscriptItem)}
  {@const room = item.room}
  {@const seq = item.seq}
  <div class:agent-input={item.kind === "post"} class:message={item.kind !== "post"}>{#if item.kind === "post"}<Avatar kind="session" name={sender("session", session)} size={32} />{/if}<div class="agent-body">{#if item.kind === "post"}<div class="agent-sender"><a href={`/r/${encodeURIComponent(store.room)}/s/${encodeURIComponent(session)}`} onclick={navigate} style:--hue={hue(sender("session", session))}>{sender("session", session)}</a><time datetime={item.at}>{formatTime(item.at)}</time></div><div class="post-destination">{#if room && seq}<button onclick={() => onpostlocation(room, seq)}>{item.home?.kind === "feed" ? (item.room === store.room ? `Posted in #${roomName}` : "Posted in another room") : item.home?.kind === "dm" ? (item.home.session === session ? "Posted in this DM" : `Posted to ${sender("session", item.home.session)}`) : "Posted"}</button>{:else}Posted{/if}</div>{/if}<Markdown text={chunks[item.id + "/text"] ?? item.text} /><PostAttachments attachments={item.attachments ?? []} {newest} event={item.event ?? item.id} at={item.at} onvisual={visual => void openVisual(visual, item.room ?? store.room, item.event ?? item.id)} /></div></div>{#if chunkFailed[item.id + "/text"]}<button onclick={() => void fetchPart(turn.id, item.id, "text")}>Text did not load. Try again</button>{/if}
{/snippet}

<div class="thread-frame" bind:clientWidth={frameWidth}>
{#if frameWidth >= 880}<TimelineMinimap turns={minimapTurns} current={currentTurn} onselect={(id) => scroller.querySelector<HTMLElement>(`article[data-turn="${CSS.escape(id)}"]`)?.scrollIntoView({ block: "start", behavior: "smooth" })} />{/if}
<div class="thread" bind:this={scroller} onscroll={() => { atBottom = scroller.scrollHeight - scroller.scrollTop - scroller.clientHeight < 80; updatePosition(); if (loaded && before && !loading && scroller.scrollTop < 100) void load(true); }}>
  <div class="body">
    {#each turns as turn (turn.id)}
      {@const finalText = turn.ended ? (turn.items ?? []).findLast(item => item.kind === "text")?.id : undefined}
      <article data-turn={turn.id}>
        {#each turn.inputs ?? [] as input (input.id)}
          {#if input.from.kind === "owner"}
            <div class="owner-message" title={formatTime(input.at)}><Markdown text={input.type === "post.created" ? chunks["input:" + input.id + "/text"] ?? input.text : "Started work"} />{@render inputAttachments(input)}{#if chunkFailed["input:" + input.id + "/text"]}<button onclick={() => void fetchPart(turn.id, "input:" + input.id, "text")}>Text did not load. Try again</button>{/if}</div>
          {:else}
            <div class="agent-input"><Avatar kind={input.from.kind} name={sender(input.from.kind, input.from.session)} size={32} /><div class="agent-body"><div class="agent-sender">{#if input.from.session}<a href={`/r/${encodeURIComponent(store.room)}/s/${encodeURIComponent(input.from.session)}`} onclick={navigate} style:--hue={hue(sender(input.from.kind, input.from.session))}>{sender(input.from.kind, input.from.session)}</a>{:else}<strong>{sender(input.from.kind, input.from.session)}</strong>{/if}<time datetime={input.at}>{formatTime(input.at)}</time></div><Markdown text={input.type === "post.created" ? chunks["input:" + input.id + "/text"] ?? input.text : "Started work"} />{@render inputAttachments(input)}{#if chunkFailed["input:" + input.id + "/text"]}<button onclick={() => void fetchPart(turn.id, "input:" + input.id, "text")}>Text did not load. Try again</button>{/if}</div></div>
          {/if}
        {/each}
        <!-- Dyna: Posts and the final answer remain visible outside the work fold. -->
        {#if turn.ended && (turn.olderItems || (turn.items ?? []).some(item => item.kind === "tool" || item.kind === "thinking"))}
          <button class="work-header" aria-expanded={!!expandedTurns[turn.id]} onclick={() => { expandedTurns[turn.id] = !expandedTurns[turn.id]; if (expandedTurns[turn.id] && turn.olderItems) void readContinuation(turn, "older"); }}>Worked for {formatTimelineDuration(Date.parse(turn.ended) - Date.parse(turn.started))}<span class:open={expandedTurns[turn.id]}><Icon name="chevron" size={14} /></span></button>
        {/if}
        {#each deriveTimelineGroups(turn.items ?? []) as group (group.id)}
          {#if group.kind === "work"}
            {#if !turn.ended || expandedTurns[turn.id]}
            <TranscriptWorkRow items={group.items} ended={!!turn.ended} started={turn.started} {now} {chunks}
              onopen={() => { if (turn.olderItems) void readContinuation(turn, "older"); }}
              onload={async (item, part) => { await fetchPart(turn.id, item.id, part); const value = chunks[item.id + "/" + part]; if (value === undefined) throw new Error("Part did not load"); }} />
            {/if}
          {:else if group.kind === "posts"}
            {#each group.item.posts ?? [] as post (post.id)}
              {@render postItem(turn, post)}
            {/each}
          {:else if group.item.kind === "text" || group.item.kind === "post"}
            {#if group.item.kind === "post" || !turn.ended || group.item.id === finalText || expandedTurns[turn.id]}{@render postItem(turn, group.item)}{/if}
          {:else if group.item.kind === "too_large"}<p class="muted">This item is too large to show.</p>{/if}
        {/each}
        {#if turn.outcome === "failed"}<div class="failure"><Icon name="warning" size={16} /><div><strong>{turn.cause ? causes[turn.cause] : "The turn failed"}</strong></div></div>{/if}
        {#if !turn.ended && !(turn.items ?? []).some(item => item.kind === "tool")}<p class="muted"><span class="spinner" aria-hidden="true"></span> Working for {elapsed(turn.started, now)}</p>{/if}
      </article>
    {:else}{#if !loading && !failed}<p class="muted">The conversation will appear here.</p>{/if}{/each}
    {#each queuedPosts as post (post.id)}
      <div class="input"><div class="who"><Avatar kind={post.from.kind} name={sender(post.from.kind, post.from.session)} size={22} /><strong>{sender(post.from.kind, post.from.session)}</strong></div><Markdown text={post.body.text} /><PostAttachments attachments={post.body.attachments ?? []} {newest} event={post.id} at={post.at} onvisual={visual => void openVisual(visual, post.room ?? store.room, post.id)} /></div>
    {/each}
    {#each [...view.pendingPosts.values()] as post (post.id)}
      <div class="input" class:pending={!post.failed}><strong>You</strong><Markdown text={post.request.text} /><PostAttachments attachments={post.request.attachments ?? []} {newest} onvisual={() => {}} />{#if post.failed}<StatusPill tone="failed" label="Not sent" word /> <button onclick={() => void view.retry(post.id)}>Retry</button> <button onclick={() => view.discard(post.id)}>Discard</button>{/if}</div>
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
  article { margin: 0 0 24px; }
  .failure { display: flex; gap: 10px; color: var(--muted); margin: 12px 0; }
  .failure strong { color: var(--text); }
  .spinner { display: inline-block; width: 12px; height: 12px; border: 1.5px solid var(--muted); border-top-color: transparent; border-radius: 50%; animation: spin 1s linear infinite; }
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
  .post-destination button { all: unset; cursor: pointer; }
  .post-destination button:hover { text-decoration: underline; }
  .post-destination { color: var(--muted); font-size: 12px; margin-bottom: 6px; }
  .message :global(.markdown h2) { font-size: 20px; margin: 20px 0 8px; }
  .message :global(.markdown h3) { font-size: 18px; margin: 20px 0 8px; }
  .message :global(.markdown :is(h2, h3):first-child) { margin-top: 0; }
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
