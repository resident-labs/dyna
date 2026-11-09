<script lang="ts">
  import { onDestroy, onMount, tick, untrack, type Snippet } from "svelte";
  import { SvelteMap, SvelteSet } from "svelte/reactivity";
  import type { PostReceipt } from '../contract.gen.ts';
  import { APIError, call } from "../client.ts";
  import VirtualList from "./VirtualList.svelte";
  import PostRow from "./PostRow.svelte";
  import MediaViewer from "./MediaViewer.svelte";
  import { conversationMedia, type MediaItem, type MediaRow } from "../media.ts";
  import BoxRestart from "./BoxRestart.svelte";
  import EngineRestartNotice from "./EngineRestartNotice.svelte";
  import FailedTurnRow from "./FailedTurnRow.svelte";
  import WorkRow from "./WorkRow.svelte";
  import Icon from "./Icon.svelte";
  import RequestCard from "./RequestCard.svelte";
  import StatusPill from "./StatusPill.svelte";
  import { boxRow, closedSession, describe, formatTime, shortPaths, dayLabel, feedRows, visibleAttachments, timeSpan, type Quote, type Row } from "../feed.ts";
  import { runCounts, runWords } from "../workflow.ts";
  import { activityLabel } from "../steps.ts";
  import type { RoomStore } from "../room.svelte.ts";
  import { sourceLabel } from "../source.ts";
  import type { FeedDepthFilter } from "../feed-depth.ts";
  import type { Conversation, PostRequest, Principal, RoomID } from "../contract.gen.ts";

  // unsent holds the app's failed sends of this conversation; see EventWindow.onunsent.
  let { store, hasRoom, home, navigate, admin, operate, intro, unsent, onunsent, onsettled, onforward, waiting, onclosed, onjobs, target, ontarget, depth, element = $bindable() }: {
    depth?: FeedDepthFilter;
    store: RoomStore; hasRoom: (room: RoomID) => boolean; home: Conversation; navigate: (event: MouseEvent & { currentTarget: HTMLAnchorElement }) => void; admin: boolean; operate: boolean; intro: Snippet<[boolean]>; unsent: PostRequest[];
    onunsent: (request: PostRequest) => void; onsettled: (request: PostRequest) => void; onforward: (quote: Quote) => void; waiting?: Quote; onclosed: () => void; onjobs: (session: string) => void; target?: number; ontarget: () => void; element?: HTMLDivElement;
  } = $props();

  // The view is keyed by its conversation, so its window lives as long as the view.
  // svelte-ignore state_referenced_locally
  const view = store.window(home);
  view.onunsent = request => onunsent(request);
  view.onsettled = request => onsettled(request);
  // A send that fails after the owner left and came back arrives here later, so each new one shows at once.
  $effect(() => {
    const shown = new Set(untrack(() => [...view.pendingPosts.values()].map(post => post.request)));
    for (const request of unsent) if (!shown.has(request)) view.unsent(request);
  });
  onDestroy(() => store.leave(view));
  type Item = Row | { kind: "intro"; key: string } | { kind: "steps"; key: string; labels: string[] } | { kind: "working"; key: string; lines: string[] };
  // A pending post's mentions are names; its row shows them as the sessions the engine will resolve.
  const mentioned = (names: string[]) => names.flatMap(name => {
    const session = [...store.sessions.values()].find(open => open.name === name)?.session;
    return session ? [{ kind: "session" as const, session }] : [];
  });
  let pending = $derived([...view.pendingPosts.values()].map(({ id, request, at, failed }) => ({
    id, at, failed, text: request.text, data: request.data, attachments: request.attachments ?? [], to: mentioned(request.mentions ?? []),
  })));
  // The owner's read position when the conversation opened, as in Discord: the New line marks what arrived while the
  // owner was away. It is taken once the room and the window load. Sending, Escape, or watching an event arrive at the
  // bottom of the conversation clears it, so the line never marks what the owner just saw.
  let since: number | undefined = $state();
  $effect(() => { if (since === undefined && store.ready && view.ready) since = untrack(() => store.readAt(home)); });
  // The owner has read the conversation up to its newest event once the bottom of the window is on screen while the
  // page is visible. see() sends only a newer position, so scrolling and new events make one call per change. A new
  // event may push the bottom out of view before the browser reports it, so the position is taken from a fresh
  // observation of the layout that holds that event, not from the last report.
  // The observation resolves a frame later; by then the owner may have switched tab or conversation.
  let gone = false;
  onDestroy(() => { gone = true; });
  const clearNew = () => { since = view.newest; };
  let watched: number | undefined;
  function markRead() {
    if (since === undefined || !view.atPresent || document.visibilityState !== "visible") return;
    const newest = view.newest;
    void list?.atBottom().then(shown => {
      if (!shown || gone || !view.atPresent || document.visibilityState !== "visible") return;
      store.see(home, newest);
      if (watched !== undefined && newest > watched) since = newest;
      watched = newest;
    });
  }
  // view.newest is a plain read, so the effect also follows the events themselves: an event that arrives while the owner
  // watches the end is read, and never sits under a New line.
  $effect(() => { void view.ordered.length; markRead(); });
  $effect(() => {
    document.addEventListener("visibilitychange", markRead);
    return () => document.removeEventListener("visibilitychange", markRead);
  });
  // The intro opens the conversation once the window reaches its first event; it also stands in for an empty feed.
  // Requests stay in the room feed and the Requests panel: a DM reads only its posts and the turns that answered in it (spec 5.13).
  // A DM ends with the running turn's finished steps; the live line has the current one, and they fold into its work row
  // when the turn ends. A failed box keeps its turn open but runs nothing, so no live line follows: every step is
  // finished, and the failure ends the DM, as a DM's read leaves box events out.
  const retainedRequests = new SvelteSet<string>();
  $effect(() => { for (const request of store.requests.values()) retainedRequests.add(request.id); });
  let rows = $derived(feedRows(view.ordered, home, pending, since, retainedRequests, depth));
  const receipts = new SvelteMap<number, PostReceipt[]>();
  // Read snapshots, not synthetic delivery events. Poll only unresolved posts and
  // stop when this feed leaves; the countdown itself runs locally.
  $effect(() => {
    if (home.kind !== 'feed') return;
    const posts = rows.flatMap(row => row.kind === 'post' && !row.pending ? [{ seq: Number(row.key.slice(1)), at: Date.parse(row.at) }] : []);
    let stopped = false, timer: ReturnType<typeof setTimeout>;
    async function refresh() {
      // A taken post turns read once the model sees it, which is soon; an old one that never did stays as it is.
      const recent = Date.now() - 15 * 60_000;
      const pending = posts.filter(({ seq, at }) => !receipts.has(seq) || receipts.get(seq)!.some(r => r.state === 'pending' || (r.state === 'taken' && !r.closed && at > recent))).map(({ seq }) => seq);
      for (let i = 0; i < pending.length && !stopped; i += 100) {
        const page = await call('events.list', { room: store.room, afterSeq: 0, limit: 100, receiptSeqs: pending.slice(i, i + 100) }).catch(() => undefined);
        if (stopped || !page) break;
        for (const seq of pending.slice(i, i + 100)) receipts.set(seq, page.receipts?.[seq] ?? []);
      }
      if (!stopped) timer = setTimeout(() => void refresh(), 3000);
    }
    untrack(() => { void refresh(); });
    return () => { stopped = true; clearTimeout(timer); };
  });
  // The post right under the New line; its bar stays inside the row, clear of the New label.
  // The last post of a run takes half the gap to the next run, so a hover or tint sits evenly on the words.
  let lastPosts = $derived(new Set(rows.flatMap((row, index) => {
    const next = rows[index + 1];
    return row.kind === "post" && (next?.kind !== "post" || next.first) ? [row.key] : [];
  })));
  let belowNew = $derived(rows.find((_, index) => rows[index - 1]?.kind === "new")?.key);
  // A step's image joins the media when it opens, as a feed loads a row's steps only when that row opens.
  let stepMedia: MediaRow[] = $state([]);
  let media = $derived(conversationMedia([...rows.filter(row => row.kind === "post").map(row => ({ ...row, event: visualEvents.get(row.key) })), ...stepMedia].sort((a, b) => Date.parse(a.at) - Date.parse(b.at))));
  let mediaIndex: number | undefined = $state();
  function openMedia(key: string, position: number) { const index = media.findIndex(item => item.key === key && item.position === position); if (index >= 0) mediaIndex = index; }
  function openStep(row: MediaRow) { if (!stepMedia.some(entry => entry.key === row.key)) stepMedia = [...stepMedia, row]; openMedia(row.key, 0); }
  let newestBoxRows = $derived.by(() => {
    const latest = new Map<string, string>();
    for (const row of rows) {
      const failures = row.kind === "failed" ? [row] : row.kind === "more-failures" ? row.failures : [];
      for (const failed of failures) if (failed.label !== "Turn failed") latest.set(failed.session, failed.key);
    }
    return latest;
  });
  function canRestart(row: Extract<Row, { kind: "failed" }>): boolean {
    const current = store.boxFailures.get(row.session);
    return operate && !!current && (row.label === "Failed" || row.label === "Could not start") &&
      (row.key === "box-failed" || (newestBoxRows.get(row.session) === row.key && row.at === current.at));
  }
  let visualPosts = $derived(view.posts().flatMap(event => (event.body.attachments ?? []).flatMap(file => file.visual ? [{ event, visual: file.visual }] : [])));
  let newest = $derived(new Map(visualPosts.map(({ visual }) => [visual.id, visual])));
  let visualEvents = $derived(new Map(visualPosts.map(({ event }) => [`p${event.seq}`, event.id])));
  async function loadVisual(item: MediaItem) {
    return visualPosts.filter(entry => entry.visual.id === item.file.visual?.id).reverse();
  }
  let failure = $derived(home.kind === "dm" && home.session && view.atPresent ? store.boxFailures.get(home.session) : undefined);
  let steps = $derived(home.kind === "dm" && home.session && view.atPresent ? (store.steps.get(home.session) ?? []).slice(0, failure ? undefined : -1) : []);
  // In the feed, the sessions that took the owner's last post and still run a turn say so under it, until they answer.
  let lastOwnerPost = $derived(rows.findLast(row => row.kind === "post" && !row.pending && row.from.kind === "owner"));
  let working = $derived(home.kind !== "feed" || !view.atPresent || !lastOwnerPost || rows.at(-1) !== lastOwnerPost ? []
    : (receipts.get(Number(lastOwnerPost.key.slice(1))) ?? []).filter(r => (r.state === "taken" || r.state === "read") && store.turns.has(r.session)).slice(0, 3)
      .map(r => { const label = store.activity.get(r.session); return label ? `${r.name} is working · ${activityLabel(label)}` : `${r.name} is working`; }));
  let items: Item[] = $derived([
    ...view.ready && !view.hasOlder ? [{ kind: "intro" as const, key: "intro" }] : [],
    ...rows,
    ...steps.length ? [{ kind: "steps" as const, key: "steps", labels: steps }] : [],
    ...working.length ? [{ kind: "working" as const, key: "working", lines: working }] : [],
    ...failure && home.kind === "dm" && home.session ? [{ kind: "failed" as const, key: "box-failed", seq: 0, session: home.session, at: failure.at ?? "", ...boxRow(failure.box, failure.cause) }] : [],
  ]);
  import { TurnTranscripts } from "../transcripts.svelte.ts";

  // Opened work rows, steps and transcripts stay while the list recycles their elements.
  const opened = new SvelteSet<string>();
  const transcripts = new SvelteMap<string, TurnTranscripts>();
  function transcriptOf(session: string): TurnTranscripts {
    let transcript = transcripts.get(session);
    if (!transcript) { transcript = new TurnTranscripts(session); transcripts.set(session, transcript); }
    return transcript;
  }
  // svelte-ignore state_referenced_locally
  const name = (session: string) => store.names.get(session) ?? closedSession;
  const author = (from: Principal) => from.kind === "owner" ? "you" : from.session ? name(from.session) : "Dyna";
  // The root session is the open session with no parent.
  const root = (session?: string) => { const open = session ? store.sessions.get(session) : undefined; return open !== undefined && !open.parent; };
  // Request expiry times refresh each half minute; this reads nothing from the engine.
  let now = $state(Date.now());
  onMount(() => {
    const timer = setInterval(() => { now = Date.now(); }, 30_000);
    return () => clearInterval(timer);
  });
  const toggle = (key: string) => { list?.hold(); if (!opened.delete(key)) opened.add(key); };
  let list: VirtualList<Item> | undefined = $state();
  export function jumpMedia(key: string) {
    const index = items.findIndex(item => item.key === key);
    if (index >= 0) void list?.reveal(index);
  }
  // Measured row heights keep the deferred correction small when new rows first enter the view.
  // Rows above the reader are measured as they arrive, so a close estimate keeps the view from jumping. A reading-width
  // line holds about 95 characters. Visual cards and images are the tall rows.
  const postHeight = (item: Extract<Item, { kind: "post" }>): number => {
    const lines = item.text.split("\n").reduce((sum, line) => sum + Math.max(1, Math.ceil(line.length / 95)), 0);
    const media = item.attachments.reduce((sum, file) => sum + (file.visual ? 340 : file.media.startsWith("image/") ? 240 : 44), 0);
    return (item.first ? 48 : 24) + 25 * lines + media;
  };
  const estimate = (item: Item): number => {
    if (item.kind === "post") return postHeight(item);
    if (item.kind === "day" || item.kind === "new" || item.kind === "notice" || item.kind === "restarted" || item.kind === "run") return 28;
    return 59;
  };
  // True once the conversation has loaded with no event at all; a session that never existed reads so.
  export const empty = () => view.ready && !view.ordered.length;
  let ring: number | undefined = $state();
  $effect(() => {
    const seq = target;
    if (seq === undefined || !view.ready) return;
    let cancelled = false;
    void (async () => {
      await view.seek(seq);
      if (cancelled) return;
      await tick();
      const index = items.findIndex(item => item.key === `p${seq}`);
      if (index >= 0) {
        await list?.reveal(index);
        ring = seq;
        setTimeout(() => { if (ring === seq) ring = undefined; }, 1000);
      }
      ontarget();
    })();
    return () => { cancelled = true; };
  });
  // The owner's own post and a jump always land on the newest row, even from inside the present window.
  // It never rejects: a failed send stays in the feed as a row with Retry.
  export async function send(request: PostRequest): Promise<void> {
    clearNew();
    const sending = view.post(request);
    void list?.toEnd();
    await sending;
    await list?.toEnd();
  }
  // Rows that do not fill the view cannot be scrolled up, so older pages load until they do.
  // fill() stops before it would leave the present; rows that are still short then get an
  // explicit control, so older history stays reachable.
  const filled = async () => { await tick(); return list?.full() ?? false; };
  let short = $state(false);
  async function grow() {
    await view.fill(filled);
    short = !(await filled());
  }
  // Pages that fill adds go above the view, and their measured height pushes the newest row down, so the open lands on
  // it again. Only the open: this effect also reruns after each later load, and a reader paging up stays where they are.
  let landed = false;
  $effect(() => {
    if (view.ready) void grow().then(() => {
      if (!landed && view.atPresent) { landed = true; void list?.toEnd(); }
    });
  });
  async function older() {
    await view.loadOlder();
    await grow();
  }
  async function jump() {
    await view.jumpToPresent();
    await list?.toEnd();
    await grow();
  }
</script>

<svelte:window onkeydown={event => { if (event.key === "Escape" && !event.defaultPrevented) clearNew(); }} />

{#if mediaIndex !== undefined}<MediaViewer room={store.room} {loadVisual} items={media} initial={mediaIndex} {name} {root} onclose={() => mediaIndex = undefined} onjump={jumpMedia} />{/if}

{#if short && view.hasOlder}
  <button type="button" class="jump" onclick={older}>Show older posts</button>
{/if}
{#if items.length}
  <VirtualList bind:this={list} {items} key={item => item.key} label="Feed" {estimate} follow={view.atPresent} bind:element
    onstart={() => void view.loadOlder(() => list?.scrollSettled() ?? Promise.resolve())} onend={() => void view.loadNewer()} onbottom={shown => { if (shown) markRead(); }}>
    {#snippet row(item: Item)}
      {#if item.kind === "post"}<div class:ring={item.key === `p${ring}`}><PostRow receipts={receipts.get(Number(item.key.slice(1))) ?? []} linked={!item.source || hasRoom(item.source.room)} last={lastPosts.has(item.key)} room={store.room} {navigate} onmedia={position => openMedia(item.key, position)} row={item} belowNew={item.key === belowNew} {newest} visualEvent={visualEvents.get(item.key)} author={item.source ? sourceLabel(item.source) : author(item.from)} to={home.kind === "dm" ? undefined : item.target ?? item.to.map(author).join(", ")} root={root(item.from.session)}
        onretry={id => void view.retry(id)} ondiscard={id => view.discard(id)} swipeDisabled={waiting?.at === item.at && waiting.author === author(item.from)} onforward={operate ? () => onforward({ author: item.source ? sourceLabel(item.source) : author(item.from), at: item.at, text: item.text, data: item.data, attachments: visibleAttachments(item.attachments, item.dataAttachment).map(({ file }) => file) }) : undefined} /></div>
      {:else if item.kind === "notice" && item.event.type === "hook.digest"}<details class="digest"><summary class="divider"><span>{describe(item.event, name)} · <time datetime={item.event.at}>{formatTime(item.event.at)}</time></span></summary>{#each item.event.body.events ?? [] as text}<pre>{text}</pre>{/each}{#if item.event.body.more}<pre>and {item.event.body.more} more</pre>{/if}</details>
      {:else if item.kind === "notice"}<p class="divider"><span>{#if item.event.type === "engine.restarted"}<EngineRestartNotice event={item.event.body} />{:else}{describe(item.event, name)}{/if}{#if item.resumedAt}{" "}for {Math.round((Date.parse(item.resumedAt) - Date.parse(item.event.at)) / 60_000) < 1 ? "under a minute" : `${Math.round((Date.parse(item.resumedAt) - Date.parse(item.event.at)) / 60_000)} min`}{/if} · <time datetime={item.event.at}>{formatTime(item.event.at)}</time></span></p>
      {:else if item.kind === "restarted"}<div class="restart-warning"><span class="restart-mark"><StatusPill tone="warning" label="Box replaced" dot triangle /></span><p>{name(item.session)}'s box was replaced. Unpushed work in the old volume is gone. <time datetime={item.at}>{formatTime(item.at)}</time></p></div>
      {:else if item.kind === "failed"}<FailedTurnRow row={item} author={name(item.session)} root={root(item.session)}
        onseen={item.label === "Turn failed" ? () => store.see(home, item.seq) : undefined}>
        {#if canRestart(item)}<BoxRestart session={item.session} name={name(item.session)} {onclosed} />{/if}</FailedTurnRow>
      {:else if item.kind === "more-failures"}<div class="more-failures"><button type="button" aria-expanded={opened.has(item.key)} onclick={() => toggle(item.key)}>and {item.failures.length} more {item.failures.every(failure => failure.label === "Turn failed") ? `failed ${item.failures.length === 1 ? "turn" : "turns"}` : item.failures.length === 1 ? "failure" : "failures"} <time datetime={item.failures[0].at}>{item.failures.length > 1 ? timeSpan(item.failures[0].at, item.failures.at(-1)!.at) : formatTime(item.failures[0].at)}</time><span class="failure-chevron" class:open={opened.has(item.key)}><Icon name="chevron" size={14} /></span></button></div>
        {#if opened.has(item.key)}{#each item.failures as failed (failed.key)}<FailedTurnRow row={failed} author={name(failed.session)} root={root(failed.session)} onseen={failed.label === "Turn failed" ? () => store.see(home, failed.seq) : undefined}>{#if canRestart(failed)}<BoxRestart session={failed.session} name={name(failed.session)} {onclosed} />{/if}</FailedTurnRow>{/each}{/if}
      <!-- The store knows every request that waited when the room opened and every later one. A request the window
        shows as waiting that the store lacks was decided outside the window, so it gets no buttons. -->
      {:else if item.kind === "request"}{@const live = store.requests.get(item.request.id)}
        <RequestCard {admin} request={live ?? item.request} gone={!live && item.request.state === "pending"} {store} {now} feed />
      {:else if item.kind === "run"}{@const word = runWords[item.run.state]}
        <a class="run" href={`/r/${encodeURIComponent(store.room)}/s/${encodeURIComponent(item.run.session)}`} onclick={navigate}><Icon name="tool" size={14} /><span class="run-line"><strong>{item.run.name}</strong>{" · "}{#if word.tone}<StatusPill tone={word.tone} label={word.word} word />{:else}{word.word}{/if}{#each runCounts({ failed: item.run.failed, steps: item.run.steps ?? store.workflows.get(item.run.session)?.steps }) as part (part)}{` · ${part}`}{/each} <time datetime={item.at}>{formatTime(item.at)}</time></span></a>
      {:else if item.kind === "work"}<WorkRow row={item} {name} {opened} ontoggle={toggle} {transcripts} {transcriptOf} {onjobs} prs={store.prs} onmedia={openStep} />
      {:else if item.kind === "new"}<p class="new"><span>New</span></p>
      {:else if item.kind === "day"}<p class="day"><span>{dayLabel(item.at, Date.now())}</span></p>
      {:else if item.kind === "working"}{#each item.lines as line (line)}<p class="working"><span class="pulse" aria-hidden="true"></span><span>{line}</span></p>{/each}
      {:else if item.kind === "steps"}<ul class="steps" aria-label="Finished steps">
        {#if item.labels.length > 8}<li>{item.labels.length - 8} earlier</li>{/if}
        {#each item.labels.slice(-8) as label, i (i)}<li>{shortPaths(label)}</li>{/each}</ul>
      {:else}{@render intro(rows.some(row => row.kind === "post"))}{/if}
    {/snippet}
  </VirtualList>
{/if}
{#if view.error !== undefined}
  <p class="hint" role="alert">{view.error instanceof APIError && view.error.detail.code === "forbidden"
    ? "Your sign-in ended. Run dyna web to sign in again." : view.ready ? "Some posts did not load. Scroll or jump to try again." : "Posts did not load."}
    {#if !view.ready}<button type="button" class="jump" onclick={() => void view.open()}>Try again</button>{/if}</p>
{/if}
{#if !view.atPresent}
  <button type="button" class="btn present" onclick={jump}>New posts below. Jump to present</button>
{/if}

<style>
  .ring { animation: highlight 1s ease-out; }
  @keyframes highlight { from { box-shadow: inset 0 0 0 2px var(--focus); } to { box-shadow: inset 0 0 0 2px transparent; } }
  @media (prefers-reduced-motion: reduce) { .ring { animation: none; box-shadow: inset 0 0 0 2px var(--focus); } }
  /* The running turn's steps sit where its work line will fold, in the work list's form. */
  .working { display: flex; align-items: center; gap: 8px; margin: 0; padding: 2px 16px 2px var(--gutter); color: var(--muted); font-size: 12.5px; line-height: 20px; }
  .working span:last-child { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .pulse { flex: none; width: 6px; height: 6px; border-radius: 50%; background: var(--success); animation: pulse 1.6s ease-in-out infinite; }
  @keyframes pulse { 50% { opacity: .35; } }
  @media (prefers-reduced-motion: reduce) { .pulse { animation: none; } }
  .steps { margin: 4px 16px 4px var(--gutter); padding: 0 0 0 12px; border-left: 1px solid var(--line); list-style: none; color: var(--muted); font-size: 12.5px; }
  .steps li { padding: 1px 0; overflow-wrap: anywhere; }
  /* A run's line sits where a work row does, past PostRow's avatar. */
  .run { display: grid; grid-template-columns: 14px minmax(0, 1fr); align-items: start; gap: 10px; padding: 7px 16px 7px 72px; color: var(--muted); font-size: 13px; font-variant-numeric: tabular-nums; text-decoration: none; }
  .run :global(svg) { margin-top: 4px; }
  .run-line { min-width: 0; padding-top: 3px; overflow-wrap: anywhere; }
  .run strong { color: var(--text); font-weight: 500; }
  .run time { margin-left: 8px; opacity: .8; white-space: nowrap; }
  .run:hover strong { text-decoration: underline; }
  .run:focus-visible { outline: 2px solid var(--focus); outline-offset: -2px; }
  @media (max-width: 640px) { .run { padding-left: 64px; } }
  @media (hover: none) { .run { min-height: 40px; align-items: center; } }
  /* A centred muted date on a hairline, as in Discord. */
  .day { margin: 16px 16px 4px; display: flex; align-items: center; gap: 8px; color: var(--muted); font-size: 12.5px; font-weight: 600; line-height: 1.25; font-variant-numeric: tabular-nums; }
  .day::before, .day::after { content: ""; flex: 1; border-top: 1px solid var(--line); }
  .more-failures { padding: 4px 16px 4px var(--gutter); color: var(--muted); font-size: 12.5px; }
  .more-failures button { all: unset; display: flex; align-items: center; gap: 8px; width: 100%; cursor: pointer; }
  .more-failures time { opacity: .8; }
  .failure-chevron { display: inline-flex; transform: rotate(-90deg); }
  .failure-chevron.open { transform: none; }
  .restart-warning { display: grid; grid-template-columns: 32px minmax(0, 1fr); gap: 12px; padding: 6px 16px; color: var(--muted); font-size: 14px; }
  .restart-mark { display: flex; justify-content: center; padding-top: 2px; }
  .restart-warning p { margin: 0; overflow-wrap: anywhere; }
  .restart-warning time { white-space: nowrap; font-size: 12.5px; }
  .more-failures button:focus-visible { outline: 2px solid var(--focus); }
  .divider { margin: 12px 16px; display: flex; align-items: center; gap: 8px; color: var(--muted); text-align: center; font-size: 12.5px; }
  .digest summary { cursor: pointer; list-style: none; }
  .digest summary::-webkit-details-marker { display: none; }
  .digest summary span::after { content: " ▸"; }
  .digest[open] summary span::after { content: " ▾"; }
  .digest pre { margin: 8px 16px; white-space: pre-wrap; overflow-wrap: anywhere; font-size: 12px; }
  .divider::before, .divider::after { content: ""; flex: 1; min-width: 12px; border-top: 1px solid var(--line); }
  /* The first unread row, as in Discord and v1: a danger hairline with a small label at its right end. */
  .new { margin: 8px 16px 4px; display: flex; align-items: center; gap: 8px; color: var(--danger); font-size: 11px; font-weight: 600; line-height: 1.25; }
  .new::before { content: ""; flex: 1; border-top: 1px solid var(--danger); }
  .new span { padding: 1px 6px; border-radius: 6px; background: var(--danger); color: var(--bg); }
  .hint { margin: 0; padding: 6px 16px; color: var(--muted); font-size: 12px; }
  .jump { all: unset; cursor: pointer; padding: 6px 16px; color: var(--muted); font-size: 12px; }
  .jump:hover { color: var(--text); }
  /* Jump to present is a control: the shared button on the raised surface, centred under the feed. */
  .present { align-self: center; margin: 4px 16px 8px; border-radius: 99px; background: var(--raised); }
  .present:hover { background: var(--hover); }
  .jump:focus-visible { outline: 2px solid var(--focus); outline-offset: -2px; }
</style>
