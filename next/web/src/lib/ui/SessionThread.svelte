<script lang="ts">
  import { onDestroy, onMount, tick, untrack } from "svelte";
  import { call } from "../client.ts";
  import { causes, formatTime } from "../feed.ts";
  import { hue } from "../identity.ts";
  import { watchTranscripts } from "../stream.ts";
  import type { PostRequest, TranscriptItem, TranscriptPart, TranscriptReadResponse, TranscriptTurn } from "../contract.gen.ts";
  import type { RoomStore } from "../room.svelte.ts";
  import Icon, { type IconName } from "./Icon.svelte";
  import { activeVerb, elapsed, stepTime, titleOf, workingGroup, workSummary, type StepKind } from "../steps.ts";
  import Avatar from "./Avatar.svelte";
  import StatusPill from "./StatusPill.svelte";
  import Markdown from "./Markdown.svelte";

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
  let before: string | undefined = $state();
  let total = $state(0);
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
  let atBottom = $state(true);
  let loaded = $state(false);
  let destroyed = false;
  let lastSeq = $derived(Math.max(0, ...turns.flatMap(turn => [...(turn.inputs ?? []).map(input => input.seq), ...(turn.items ?? []).flatMap(item => [item.seq ?? 0, ...(item.posts ?? []).map(post => post.seq ?? 0)])])));
  let queuedPosts = $derived(view.posts().filter(post => post.seq > lastSeq));

  async function load(older = false) {
    if (loading) { if (!older) pending = true; return; }
    loading = true;
    try {
      const [page, work] = await Promise.all([
        call("transcript.read", { session, before: older ? before : undefined }),
        older ? Promise.resolve(undefined) : call("session.work", { session, limit: 1 }),
      ]);
      if (work) total = work.total;
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
        turns = [...retained, ...incoming.reverse()];
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
  async function moreItems(turn: TranscriptTurn) {
    const page = await call("transcript.read", { session, turn: turn.id, afterItem: turn.after, itemIndex: turn.nextItem, tail: turn.tail, postIndex: turn.nextPost }).catch(() => undefined);
    const next = page?.turns?.[0];
    if (!next) { failed = true; return; }
    turns = turns.map(item => item.id === turn.id ? { ...item, inputs: [...item.inputs ?? [], ...next.inputs ?? []], items: [...item.items ?? [], ...next.items ?? []], more: next.more, after: next.after, nextItem: next.nextItem, tail: next.tail, nextPost: next.nextPost } : item);
  }
  type Group = { kind: "work"; id: string; items: TranscriptItem[] } | { kind: "item" | "posts"; id: string; item: TranscriptItem };
  function groups(items: TranscriptItem[]): Group[] {
    const result: Group[] = [];
    for (const item of items) {
      const last = result.at(-1);
      if (item.kind === "tool" && last?.kind === "work") last.items.push(item);
      else if (item.kind === "tool") result.push({ kind: "work", id: item.id, items: [item] });
      else result.push({ kind: "item", id: item.id, item });
      if (item.kind === "tool" && ((item.posts?.length ?? 0) > 0 || item.postsOmitted)) result.push({ kind: "posts", id: "posts:" + item.id, item });
    }
    return result;
  }
  let chunks: Record<string, string> = $state({});
  let chunkFailed: Record<string, boolean> = $state({});
  async function fetchPart(turn: string, item: string, part: TranscriptPart) {
    const key = item + "/" + part;
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
  }
  const icons: Record<StepKind, IconName> = { read: "file", edit: "edit", write: "write", search: "search", list: "list", run: "terminal", fetch: "globe", tool: "tool" };
  const kinds = (items: TranscriptItem[]) => [...new Set(items.map(item => titleOf(item).kind))];
  const sender = (kind: string, id?: string) => kind === "owner" ? "You" : id ? store.names.get(id) ?? "Session" : "Dyna";
  const duration = (ms?: number) => ms === undefined ? "" : ms < 60000 ? `${Math.floor(ms / 1000)} s` : `${Math.floor(ms / 60000)} m ${Math.floor(ms / 1000) % 60} s`;
</script>

{#snippet postItem(turn: TranscriptTurn, item: TranscriptItem)}
  <div class="message"><Markdown text={chunks[item.id + "/text"] ?? item.text} /></div>{#if item.more && chunks[item.id + "/text"] === undefined}<button onclick={() => void fetchPart(turn.id, item.id, "text")}>Show all</button>{/if}
{/snippet}

<div class="thread" bind:this={scroller} onscroll={() => { atBottom = scroller.scrollHeight - scroller.scrollTop - scroller.clientHeight < 80; }}>
  <div class="body">
    {#if before}<button class="older" disabled={loading} onclick={() => void load(true)}>Load earlier turns</button>{/if}
    {#each turns as turn, index (turn.id)}
      <article>
        <div class="rule"><span><strong>Turn {Math.max(1, total - turns.length + index + 1)}</strong>{#if turn.durationMs !== undefined}{" · "}{duration(turn.durationMs)}{:else if !turn.ended}{" · "}{elapsed(turn.started, now)}{/if}{#if !turn.ended}{" · running"}{:else if turn.outcome === "failed"}{" · "}<StatusPill tone="failed" label="failed" word />{/if}</span><i></i></div>
        {#each turn.inputs ?? [] as input (input.id)}
          <div class="input"><div class="who"><Avatar kind={input.from.kind} name={sender(input.from.kind, input.from.session)} size={22} />{#if input.from.kind === "owner"}<strong>You</strong>{:else}<strong class:identity={input.from.kind === "session"} style:--hue={hue(sender(input.from.kind, input.from.session))}>{sender(input.from.kind, input.from.session)}</strong>{/if}{#if input.home}<span class="muted">{input.home.kind === "feed" ? `in ${roomName}` : input.home.session === session ? "in your DM" : "in a DM"}</span>{/if}<time datetime={input.at}>{formatTime(input.at)}</time></div><Markdown text={input.type === "post.created" ? chunks["input:" + input.id + "/text"] ?? input.text : "Started work"} />{#if input.more && input.type === "post.created" && chunks["input:" + input.id + "/text"] === undefined}<button onclick={() => void fetchPart(turn.id, "input:" + input.id, "text")}>Show all</button>{/if}</div>
        {/each}
        {#each groups(turn.items ?? []) as group (group.id)}
          {#if group.kind === "work"}
            <details open={turn.outcome === "failed" || workingGroup(group.items, !!turn.ended)}>
              <summary><span class="chevron"><Icon name="chevron" size={14} /></span>{#if !workingGroup(group.items, !!turn.ended)}{#each kinds(group.items) as kind}<Icon name={icons[kind]} size={14} />{/each}<span>{workSummary(group.items)}</span><span class="time">{stepTime({ started: group.items[0].at, ended: group.items.at(-1)?.ended })}</span>{:else}<span>Working for {elapsed(turn.started, now)}</span>{/if}</summary>
              <div class="steps">{#each group.items as item (item.id)}
                {@const title = titleOf(item)}
                <details class="step"><summary>{#if !item.ended && !turn.ended}<span class="spinner" role="img" aria-label="Working"></span>{:else}<Icon name={icons[title.kind]} size={14} />{/if} <span>{!item.ended && !turn.ended ? activeVerb(title.kind, title.verb) : title.verb}</span><code>{title.target}</code><span class="time">{!item.ended && !turn.ended ? elapsed(item.at, now) : stepTime({ started: item.at, ended: item.ended })}</span>{#if item.failed}<StatusPill tone="failed" label="Failed" word />{/if}</summary>
                  {#if item.arguments}<pre>{chunks[item.id + "/arguments"] ?? item.arguments}</pre>{/if}
                  {#if item.output}<pre>{chunks[item.id + "/output"] ?? item.output}</pre>{/if}
                  {#if item.more && chunks[item.id + "/arguments"] === undefined}<button onclick={() => void fetchPart(turn.id, item.id, "arguments")}>Show all arguments</button>{/if}
                  {#if item.more && chunks[item.id + "/output"] === undefined}<button onclick={() => void fetchPart(turn.id, item.id, "output")}>Show all output</button>{/if}
                  {#if chunkFailed[item.id + "/output"]}<button onclick={() => void fetchPart(turn.id, item.id, "output")}>Output did not load. Try again</button>{/if}
                </details>
              {/each}</div>
            </details>
          {:else if group.kind === "posts"}
            {#each group.item.posts ?? [] as post (post.id)}
              {@render postItem(turn, post)}
            {/each}
            {#if group.item.postsOmitted && group.item.postsNext}<button class="muted" onclick={() => onpostlocation(group.item.postsNext!.room, group.item.postsNext!.seq)}>+{group.item.postsOmitted} more posts</button>{/if}
          {:else if group.item.kind === "thinking"}
            <details><summary><span class="chevron"><Icon name="chevron" size={14} /></span> Thought</summary><div class="thought"><Markdown text={chunks[group.item.id + "/text"] ?? group.item.text} /></div>{#if group.item.more && chunks[group.item.id + "/text"] === undefined}<button onclick={() => void fetchPart(turn.id, group.item.id, "text")}>Show all</button>{/if}</details>
          {:else if group.item.kind === "text" || group.item.kind === "post"}
            {@render postItem(turn, group.item)}
          {:else if group.item.kind === "too_large"}<p class="muted">This item is too large to show.</p>{/if}
        {/each}
        {#if turn.outcome === "failed"}<div class="failure"><Icon name="warning" size={16} /><div><strong>{turn.cause ? causes[turn.cause] : "The turn failed"}</strong></div></div>{/if}
        {#if turn.more}<button onclick={() => void moreItems(turn)}>Show more of this turn</button>{/if}
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

<style>
  .thread { height: 100%; min-height: 0; overflow-y: auto; }
  .body { max-width: 920px; margin: auto; padding: 16px 24px; }
  article { margin: 0 0 24px; }
  .rule { display: flex; gap: 12px; align-items: center; margin: 16px 0; color: var(--muted); font-size: 12.5px; }
  .rule strong { color: var(--text); font-weight: 600; }
  .failure { display: flex; gap: 10px; color: var(--muted); margin: 12px 0; }
  .failure strong { color: var(--text); }
  .spinner { display: inline-block; width: 12px; height: 12px; border: 1.5px solid var(--muted); border-top-color: transparent; border-radius: 50%; animation: spin 1s linear infinite; }
  @keyframes spin { to { transform: rotate(360deg); } }
  @media (prefers-reduced-motion: reduce) { .spinner { animation: none; } }
  .rule i { flex: 1; height: 1px; background: var(--line); }
  .input { background: var(--raised); padding: 12px 16px; border-radius: 14px; margin: 12px 0; }
  p { white-space: pre-wrap; overflow-wrap: anywhere; line-height: 1.45; }
  .who { display: flex; align-items: center; gap: 8px; }
  .who strong, .who .muted { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .who .identity { flex-shrink: 0; max-width: 60%; color: oklch(var(--id-name) var(--hue)); }
  .who time { flex: none; margin-left: auto; color: var(--muted); font-size: 11.5px; font-variant-numeric: tabular-nums; white-space: nowrap; }
  .input :global(.markdown) { margin: 4px 0 0; }
  .pending { opacity: 0.5; }
  .message { margin: 12px 0; }
  .muted, details { color: var(--muted); }
  details { margin: 8px 0; font-size: 13px; }
  summary { cursor: pointer; display: flex; align-items: center; gap: 8px; min-height: 28px; list-style: none; border-radius: 6px; }
  summary:hover { background: var(--hover); color: var(--text); }
  summary::-webkit-details-marker { display: none; }
  .chevron { display: inline-flex; flex: none; transition: transform 150ms; }
  details:not([open]) > summary .chevron { transform: rotate(-90deg); }
  details > .thought { margin-left: 20px; }
  .steps { border-left: 1px solid var(--line); margin-left: 7px; padding-left: 12px; }
  .time { margin-left: auto; white-space: nowrap; font-size: 11px; font-variant-numeric: tabular-nums; }
  code { min-width: 0; overflow-wrap: anywhere; font: 12.5px var(--mono); }
  pre { margin: 4px 0 8px 20px; padding: 8px 12px; background: var(--sidebar); border-radius: 6px; overflow-wrap: anywhere; white-space: pre-wrap; font: 12.5px/1.45 var(--mono); }
  button { padding: 0; border: 0; background: none; color: var(--muted); font: inherit; cursor: pointer; }
  button:hover { color: var(--text); }
  button:focus-visible, summary:focus-visible { outline: 2px solid var(--focus); }
  .older { display: block; margin: auto; padding: 8px; }
  .latest { position: absolute; right: 24px; bottom: 16px; background: var(--sidebar); padding: 8px 12px; border-radius: 6px; }
  @media (max-width: 600px) { .body { padding: 12px 16px; } }
  @media (pointer: coarse) { summary, button { min-height: 40px; } }
</style>
