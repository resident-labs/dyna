<script lang="ts">
  import { onDestroy, onMount, tick, untrack } from "svelte";
  import { call } from "../client.ts";
  import { causes, formatTime } from "../feed.ts";
  import { hue } from "../identity.ts";
  import { watchTranscripts } from "../stream.ts";
  import type { PostRequest, TranscriptItem, TranscriptPart, TranscriptReadResponse, TranscriptTurn } from "../contract.gen.ts";
  import type { RoomStore } from "../room.svelte.ts";
  import Icon from "./Icon.svelte";
  import { elapsed } from "../steps.ts";
  import Avatar from "./Avatar.svelte";
  import StatusPill from "./StatusPill.svelte";
  import Markdown from "./Markdown.svelte";
  import TranscriptWorkRow from "./TranscriptWorkRow.svelte";
  import TimelineMinimap from "./TimelineMinimap.svelte";
  import { deriveTimelineGroups, formatTimelineDuration, mergeTimelineInputs, mergeTimelineItems } from "../timeline.ts";

  let { session, roomName, store, unsent, onunsent, onsettled, onpostlocation, clock = Date.now }: { session: string; roomName: string; store: RoomStore; unsent: PostRequest[]; onunsent: (request: PostRequest) => void; onsettled: (request: PostRequest) => void; onpostlocation: (room: string, seq: number) => void; clock?: () => number } = $props();
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
  function updatePosition() {
    const bounds = scroller.getBoundingClientRect();
    const visible = [...scroller.querySelectorAll<HTMLElement>("article[data-turn]")].filter(el => { const rect = el.getBoundingClientRect(); return rect.bottom > bounds.top && rect.top < bounds.bottom; });
    visibleTurns = visible.map(el => el.dataset.turn!);
    currentTurn = visibleTurns[0] ?? null;
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
    return () => { destroyed = true; stop(); document.removeEventListener("visibilitychange", markRead); };
  });
  const paging = new Set<string>();
  async function readContinuation(turn: TranscriptTurn, kind: "items" | "posts" | "older") {
    const key = turn.id + "/" + kind;
    if (paging.has(key)) return;
    paging.add(key);
    let cursor = kind === "items" ? turn.nextMandatory ?? 0 : kind === "posts" ? turn.nextMandatoryPost ?? 0 : turn.nextItem ?? 0;
    try {
      for (;;) {
        const request = kind === "items" ? { mandatoryItems: true, mandatoryIndex: cursor } : kind === "posts" ? { mandatoryPosts: true, mandatoryPost: cursor } : { olderItems: true, itemIndex: cursor };
        const page = await call("transcript.read", { session, turn: turn.id, ...request });
        const next = page.turns?.[0];
        if (!next) throw new Error("Missing transcript page");
        turns = turns.map(item => item.id === turn.id ? { ...item, items: mergeTimelineItems(item.items ?? [], next.items ?? []), ...(kind === "older" ? { olderItems: next.olderItems, nextItem: next.nextItem } : kind === "items" ? { mandatoryItems: next.mandatoryItems, nextMandatory: next.nextMandatory } : { mandatoryPosts: next.mandatoryPosts, nextMandatoryPost: next.nextMandatoryPost }) } : item);
        const more = kind === "items" ? next.mandatoryItems : kind === "posts" ? next.mandatoryPosts : next.olderItems;
        if (!more) break;
        const nextCursor = kind === "items" ? next.nextMandatory ?? 0 : kind === "posts" ? next.nextMandatoryPost ?? 0 : next.nextItem ?? 0;
        if (nextCursor === cursor) throw new Error("Transcript cursor did not advance");
        cursor = nextCursor;
      }
    } catch { failed = true; }
    finally { paging.delete(key); }
  }
  async function readInputs(turn: TranscriptTurn) {
    const key = turn.id + "/inputs";
    if (paging.has(key)) return;
    paging.add(key);
    let after = turn.after;
    try {
      while (after) {
        const page = await call("transcript.read", { session, turn: turn.id, afterItem: after });
        const next = page.turns?.[0];
        if (!next || next.after === after) throw new Error("Transcript input cursor did not advance");
        turns = turns.map(item => item.id === turn.id ? { ...item, ...next, after: next.after, inputs: mergeTimelineInputs(item.inputs ?? [], next.inputs ?? []), items: mergeTimelineItems(item.items ?? [], next.items ?? []),
          ...(paging.has(turn.id + "/items") ? { mandatoryItems: item.mandatoryItems, nextMandatory: item.nextMandatory } : {}),
          ...(paging.has(turn.id + "/posts") ? { mandatoryPosts: item.mandatoryPosts, nextMandatoryPost: item.nextMandatoryPost } : {}),
          ...(paging.has(turn.id + "/older") ? { olderItems: item.olderItems, nextItem: item.nextItem } : {}) } : item);
        after = next.after;
      }
    } catch { failed = true; }
    finally { paging.delete(key); }
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
    for (const turn of turns) for (const item of turn.items ?? []) {
      const messages = item.kind === "text" || item.kind === "post" ? [item] : item.posts ?? [];
      for (const message of messages) {
        const key = message.id + "/text";
        if (message.more && chunks[key] === undefined && !chunkFailed[key]) void fetchPart(turn.id, message.id, "text");
      }
    }
  });
  const sender = (kind: string, id?: string) => kind === "owner" ? "You" : id ? store.names.get(id) ?? "Session" : "Dyna";
</script>

{#snippet postItem(turn: TranscriptTurn, item: TranscriptItem)}
  <div class="message"><Markdown text={chunks[item.id + "/text"] ?? item.text} /></div>{#if chunkFailed[item.id + "/text"]}<button onclick={() => void fetchPart(turn.id, item.id, "text")}>Text did not load. Try again</button>{/if}
{/snippet}

<div class="thread-frame" bind:clientWidth={frameWidth}>
<TimelineMinimap turns={minimapTurns} current={currentTurn} persistent={true} onselect={(id) => scroller.querySelector<HTMLElement>(`article[data-turn="${CSS.escape(id)}"]`)?.scrollIntoView({ block: "start", behavior: "smooth" })} />
<div class="thread" bind:this={scroller} onscroll={() => { atBottom = scroller.scrollHeight - scroller.scrollTop - scroller.clientHeight < 80; updatePosition(); if (loaded && before && !loading && scroller.scrollTop < 100) void load(true); }}>
  <div class="body">
    {#each turns as turn (turn.id)}
      {@const finalText = turn.ended ? (turn.items ?? []).findLast(item => item.kind === "text")?.id : undefined}
      <article data-turn={turn.id}>
        {#each turn.inputs ?? [] as input (input.id)}
          <div class="input"><div class="who"><Avatar kind={input.from.kind} name={sender(input.from.kind, input.from.session)} size={22} />{#if input.from.kind === "owner"}<strong>You</strong>{:else}<strong class:identity={input.from.kind === "session"} style:--hue={hue(sender(input.from.kind, input.from.session))}>{sender(input.from.kind, input.from.session)}</strong>{/if}{#if input.home}<span class="muted">{input.home.kind === "feed" ? `in ${roomName}` : input.home.session === session ? "in your DM" : "in a DM"}</span>{/if}<time datetime={input.at}>{formatTime(input.at)}</time></div><Markdown text={input.type === "post.created" ? chunks["input:" + input.id + "/text"] ?? input.text : "Started work"} />{#if input.more && input.type === "post.created" && chunks["input:" + input.id + "/text"] === undefined}<button onclick={() => void fetchPart(turn.id, "input:" + input.id, "text")}>Show all</button>{/if}</div>
        {/each}
        <!-- Dyna: Posts and the final answer remain visible outside the work fold. -->
        {#if turn.ended && (turn.olderItems || (turn.items ?? []).some(item => item.kind === "tool" || item.kind === "thinking"))}
          <button class="work-header" aria-expanded={!!expandedTurns[turn.id]} onclick={() => { expandedTurns[turn.id] = !expandedTurns[turn.id]; if (expandedTurns[turn.id] && turn.olderItems) void readContinuation(turn, "older"); }}>Worked for {formatTimelineDuration(Date.parse(turn.ended) - Date.parse(turn.started))}<span class:open={expandedTurns[turn.id]}><Icon name="chevron" size={14} /></span></button>
        {/if}
        {#each deriveTimelineGroups(turn.items ?? []) as group (group.id)}
          {#if group.kind === "work"}
            {#if !turn.ended || expandedTurns[turn.id]}
            <TranscriptWorkRow items={group.items} ended={!!turn.ended} started={turn.started} {now}
              onopen={() => { if (turn.olderItems) void readContinuation(turn, "older"); }}
              onload={async (item, part) => { await fetchPart(turn.id, item.id, part); const value = chunks[item.id + "/" + part]; if (value === undefined) throw new Error("Part did not load"); return value; }} />
            {/if}
          {:else if group.kind === "posts"}
            {#each group.item.posts ?? [] as post (post.id)}
              {@render postItem(turn, post)}
            {/each}
            {#if group.item.postsOmitted && group.item.postsNext}<button class="muted" onclick={() => onpostlocation(group.item.postsNext!.room, group.item.postsNext!.seq)}>+{group.item.postsOmitted} more posts</button>{/if}
          {:else if group.item.kind === "text" || group.item.kind === "post"}
            {#if group.item.kind === "post" || !turn.ended || group.item.id === finalText || expandedTurns[turn.id]}{@render postItem(turn, group.item)}{/if}
          {:else if group.item.kind === "too_large"}<p class="muted">This item is too large to show.</p>{/if}
        {/each}
        {#if turn.outcome === "failed"}<div class="failure"><Icon name="warning" size={16} /><div><strong>{turn.cause ? causes[turn.cause] : "The turn failed"}</strong></div></div>{/if}
        {#if !turn.ended && !(turn.items ?? []).some(item => item.kind === "tool")}<p class="muted"><span class="spinner" aria-hidden="true"></span> Working for {elapsed(turn.started, now)}</p>{/if}
      </article>
    {:else}{#if !loading && !failed}<p class="muted">The conversation will appear here.</p>{/if}{/each}
    {#each queuedPosts as post (post.id)}
      <div class="input"><div class="who"><Avatar kind={post.from.kind} name={sender(post.from.kind, post.from.session)} size={22} /><strong>{sender(post.from.kind, post.from.session)}</strong></div><Markdown text={post.body.text} /></div>
    {/each}
    {#each [...view.pendingPosts.values()] as post (post.id)}
      <div class="input" class:pending={!post.failed}><strong>You</strong><Markdown text={post.request.text} />{#if post.failed}<StatusPill tone="failed" label="Not sent" word /> <button onclick={() => void view.retry(post.id)}>Retry</button> <button onclick={() => view.discard(post.id)}>Discard</button>{/if}</div>
    {/each}
    {#if failed}<p class="muted" role="alert">The conversation did not load. <button onclick={() => void load()}>Try again</button></p>{/if}
  </div>
  {#if !atBottom}<button class="latest" onclick={() => scroller.scrollTo({ top: scroller.scrollHeight, behavior: "smooth" })}>Jump to latest</button>{/if}
</div>
</div>

<style>
  .thread-frame { position: relative; height: 100%; min-height: 0; }
  .thread { height: 100%; min-height: 0; overflow-y: auto; }
  .body { max-width: 784px; margin: auto; padding: 16px 24px; }
  article { margin: 0 0 24px; }
  .failure { display: flex; gap: 10px; color: var(--muted); margin: 12px 0; }
  .failure strong { color: var(--text); }
  .spinner { display: inline-block; width: 12px; height: 12px; border: 1.5px solid var(--muted); border-top-color: transparent; border-radius: 50%; animation: spin 1s linear infinite; }
  @keyframes spin { to { transform: rotate(360deg); } }
  @media (prefers-reduced-motion: reduce) { .spinner { animation: none; } }
  .input { background: var(--raised); padding: 12px 16px; border-radius: 14px; margin: 12px 0; }
  p { white-space: pre-wrap; overflow-wrap: anywhere; line-height: 1.45; }
  .who { display: flex; align-items: center; gap: 8px; }
  .who strong, .who .muted { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .who .identity { flex-shrink: 0; max-width: 60%; color: oklch(var(--id-name) var(--hue)); }
  .who time { flex: none; margin-left: auto; color: var(--muted); font-size: 11.5px; font-variant-numeric: tabular-nums; white-space: nowrap; }
  .input :global(.markdown) { margin: 4px 0 0; }
  .pending { opacity: 0.5; }
  .message { margin: 12px 0; }
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
  .latest { position: absolute; right: 24px; bottom: 16px; background: var(--sidebar); padding: 8px 12px; border-radius: 6px; }
  @media (max-width: 600px) { .body { padding: 12px 16px; } }
  @media (pointer: coarse) { button { min-height: 40px; } }
</style>
