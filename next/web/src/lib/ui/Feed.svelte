<script lang="ts">
  import { onDestroy, onMount, tick, untrack, type Snippet } from "svelte";
  import { SvelteMap, SvelteSet } from "svelte/reactivity";
  import { APIError, call } from "../client.ts";
  import VirtualList from "./VirtualList.svelte";
  import PostRow from "./PostRow.svelte";
  import MediaViewer from "./MediaViewer.svelte";
  import { conversationMedia, type MediaItem } from "../media.ts";
  import BoxRestart from "./BoxRestart.svelte";
  import FailedTurnRow from "./FailedTurnRow.svelte";
  import WorkRow from "./WorkRow.svelte";
  import Icon from "./Icon.svelte";
  import RequestCard from "./RequestCard.svelte";
  import StatusPill from "./StatusPill.svelte";
  import { boxRow, closedSession, describe, formatTime, shortPaths, dayLabel, feedRows, timeSpan, type Quote, type Row } from "../feed.ts";
  import type { RoomStore } from "../room.svelte.ts";
  import type { Conversation, PostRequest, Principal } from "../contract.gen.ts";

  // unsent holds the app's failed sends of this conversation; see EventWindow.onunsent.
  let { store, home, navigate, admin, operate, intro, unsent, onunsent, onsettled, onforward, waiting, onclosed, onjobs, target, ontarget, element = $bindable() }: {
    store: RoomStore; home: Conversation; navigate: (event: MouseEvent & { currentTarget: HTMLAnchorElement }) => void; admin: boolean; operate: boolean; intro: Snippet<[boolean]>; unsent: PostRequest[];
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
  type Item = Row | { kind: "intro"; key: string } | { kind: "steps"; key: string; labels: string[] };
  // A pending post's mentions are names; its row shows them as the sessions the engine will resolve.
  const mentioned = (names: string[]) => names.flatMap(name => {
    const session = [...store.sessions.values()].find(open => open.name === name)?.session;
    return session ? [{ kind: "session" as const, session }] : [];
  });
  let pending = $derived([...view.pendingPosts.values()].map(({ id, request, at, failed }) => ({
    id, at, failed, text: request.text, attachments: request.attachments ?? [], to: mentioned(request.mentions ?? []),
  })));
  // The owner's read position when the conversation opened, as in Discord: the New line stays where it was while the
  // owner reads on, and moves only when the conversation opens again. It is taken once the room and the window load.
  let since: number | undefined = $state();
  $effect(() => { if (since === undefined && store.ready && view.ready) since = untrack(() => store.readAt(home)); });
  // The owner has read the conversation up to its newest event once the bottom of the window is on screen while the
  // page is visible. see() sends only a newer position, so scrolling and new events make one call per change. A new
  // event may push the bottom out of view before the browser reports it, so the position is taken from a fresh
  // observation of the layout that holds that event, not from the last report.
  // The observation resolves a frame later; by then the owner may have switched tab or conversation.
  let gone = false;
  onDestroy(() => { gone = true; });
  function markRead() {
    if (since === undefined || !view.atPresent || document.visibilityState !== "visible") return;
    const newest = view.newest;
    void list?.atBottom().then(shown => { if (shown && !gone && view.atPresent && document.visibilityState === "visible") store.see(home, newest); });
  }
  $effect(markRead);
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
  let rows = $derived(feedRows(view.ordered, home, pending, since, retainedRequests));
  // The post right under the New line; its bar stays inside the row, clear of the New label.
  let belowNew = $derived(rows.find((_, index) => rows[index - 1]?.kind === "new")?.key);
  let media = $derived(conversationMedia(rows.filter(row => row.kind === "post").map(row => ({ ...row, event: visualEvents.get(row.key) }))));
  let mediaIndex: number | undefined = $state();
  function openMedia(key: string, position: number) { const index = media.findIndex(item => item.key === key && item.position === position); if (index >= 0) mediaIndex = index; }
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
  let items: Item[] = $derived([
    ...view.ready && !view.hasOlder ? [{ kind: "intro" as const, key: "intro" }] : [],
    ...rows,
    ...steps.length ? [{ kind: "steps" as const, key: "steps", labels: steps }] : [],
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
  const author = (from: Principal) => from.kind === "owner" ? "You" : from.session ? name(from.session) : "Dyna";
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
  // Measured row heights keep the deferred correction small when new rows first enter the view.
  const estimate = (item: Item): number => {
    if (item.kind === "post") return 40 + 20 * Math.max(1, item.text.split("\n").length);
    if (item.kind === "day" || item.kind === "new" || item.kind === "notice" || item.kind === "restarted") return 28;
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

{#if mediaIndex !== undefined}<MediaViewer room={store.room} {loadVisual} items={media} initial={mediaIndex} {name} {root} onclose={() => mediaIndex = undefined} onjump={key => { const index = items.findIndex(item => item.key === key); if (index >= 0) void list?.reveal(index); }} />{/if}

{#if short && view.hasOlder}
  <button type="button" class="jump" onclick={older}>Show older posts</button>
{/if}
{#if items.length}
  <VirtualList bind:this={list} {items} key={item => item.key} label="Feed" {estimate} follow={view.atPresent} bind:element
    onstart={() => void view.loadOlder(() => list?.scrollSettled() ?? Promise.resolve())} onend={() => void view.loadNewer()} onbottom={shown => { if (shown) markRead(); }}>
    {#snippet row(item: Item)}
      {#if item.kind === "post"}<div class:ring={item.key === `p${ring}`}><PostRow room={store.room} {navigate} onmedia={position => openMedia(item.key, position)} row={item} belowNew={item.key === belowNew} {newest} visualEvent={visualEvents.get(item.key)} author={author(item.from)} to={home.kind === "dm" ? undefined : item.to.map(author).join(", ")} root={root(item.from.session)}
        onretry={id => void view.retry(id)} ondiscard={id => view.discard(id)} swipeDisabled={waiting?.at === item.at && waiting.author === author(item.from)} onforward={operate ? () => onforward({ author: author(item.from), at: item.at, text: item.text, attachments: item.attachments }) : undefined} /></div>
      {:else if item.kind === "notice"}<p class="divider"><span>{describe(item.event, name)}{#if item.resumedAt}{" "}for {Math.round((Date.parse(item.resumedAt) - Date.parse(item.event.at)) / 60_000) < 1 ? "under a minute" : `${Math.round((Date.parse(item.resumedAt) - Date.parse(item.event.at)) / 60_000)} min`}{/if} · <time datetime={item.event.at}>{formatTime(item.event.at)}</time></span></p>
      {:else if item.kind === "restarted"}<div class="restart-warning"><span class="restart-mark"><StatusPill tone="warning" label="Box replaced" dot triangle /></span><p>{name(item.session)}'s box was replaced. Unpushed work in the old volume is gone. <time datetime={item.at}>{formatTime(item.at)}</time></p></div>
      {:else if item.kind === "failed"}<FailedTurnRow row={item} author={name(item.session)} root={root(item.session)}
        onseen={item.label === "Turn failed" ? () => store.see(home, item.seq) : undefined}>
        {#if canRestart(item)}<BoxRestart session={item.session} {onclosed} />{/if}</FailedTurnRow>
      {:else if item.kind === "more-failures"}<div class="more-failures"><button type="button" aria-expanded={opened.has(item.key)} onclick={() => toggle(item.key)}>and {item.failures.length} more {item.failures.every(failure => failure.label === "Turn failed") ? `failed ${item.failures.length === 1 ? "turn" : "turns"}` : item.failures.length === 1 ? "failure" : "failures"} <time datetime={item.failures[0].at}>{item.failures.length > 1 ? timeSpan(item.failures[0].at, item.failures.at(-1)!.at) : formatTime(item.failures[0].at)}</time><span class="failure-chevron" class:open={opened.has(item.key)}><Icon name="chevron" size={14} /></span></button></div>
        {#if opened.has(item.key)}{#each item.failures as failed (failed.key)}<FailedTurnRow row={failed} author={name(failed.session)} root={root(failed.session)} onseen={failed.label === "Turn failed" ? () => store.see(home, failed.seq) : undefined}>{#if canRestart(failed)}<BoxRestart session={failed.session} {onclosed} />{/if}</FailedTurnRow>{/each}{/if}
      <!-- The store knows every request that waited when the room opened and every later one. A request the window
        shows as waiting that the store lacks was decided outside the window, so it gets no buttons. -->
      {:else if item.kind === "request"}{@const live = store.requests.get(item.request.id)}
        <RequestCard {admin} request={live ?? item.request} gone={!live && item.request.state === "pending"} {store} {now} feed />
      {:else if item.kind === "work"}<WorkRow row={item} {name} {opened} ontoggle={toggle} {transcripts} {transcriptOf} {onjobs} />
      {:else if item.kind === "new"}<p class="new"><span>New</span></p>
      {:else if item.kind === "day"}<p class="day"><span>{dayLabel(item.at, Date.now())}</span></p>
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
  .steps { margin: 4px 16px 4px var(--gutter); padding: 0 0 0 12px; border-left: 1px solid var(--line); list-style: none; color: var(--muted); font-size: 12.5px; }
  .steps li { padding: 1px 0; overflow-wrap: anywhere; }
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
