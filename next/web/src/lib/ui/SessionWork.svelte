<script lang="ts">
  import { untrack } from "svelte";
  import { readWork, type WorkTurn } from "../session-work.ts";
  import { causes, dayLabel, describe, formatTime } from "../feed.ts";
  import type { RoomStore } from "../room.svelte.ts";
  import VirtualList from "./VirtualList.svelte";
  import FailedTurnRow from "./FailedTurnRow.svelte";
  import StatusPill from "./StatusPill.svelte";

  let { store, session, onfeed }: { store: RoomStore; session: string; onfeed: (seq: number) => void } = $props();
  let turns: WorkTurn[] = $state([]);
  let more = $state(false), loading = $state(false), failed = $state(false);
  let list: VirtualList<WorkTurn> | undefined = $state();
  let failures = $derived(turns.filter(turn => turn.ended?.body.outcome === "failed"));
  let failureIndex = 0;
  const name = (id: string) => store.names.get(id) ?? "Closed session";
  async function load(older = false) {
    if (loading) return;
    loading = true;
    const before = older ? turns.at(-1)?.started.body.turn : undefined;
    const page = await readWork(session, before).catch(() => undefined);
    if (page) { turns = older ? [...turns, ...page.turns] : page.turns; more = page.more; failed = false; }
    else failed = true;
    loading = false;
  }
  $effect(() => {
    void store.turns.get(session);
    void store.failed.get(session);
    untrack(() => { void load(); });
  });
  async function nextFailure() {
    const turn = failures[failureIndex++ % failures.length];
    const index = turns.indexOf(turn);
    if (index >= 0) await list?.reveal(index);
  }
</script>

<div class="work-head">
  <strong>Work</strong>
  {#if failures.length}<button type="button" class="failed-link" onclick={nextFailure}><StatusPill tone="failed" label={`Failed ${failures.length}`} /></button>{/if}
  <button type="button" class="latest" onclick={() => { void load(); void list?.reveal(0); }}>Jump to latest</button>
</div>
{#if turns.length}
  <VirtualList bind:this={list} items={turns} key={turn => turn.started.body.turn} label="Session work" role="group" align="top">
    {#snippet sticky(turn: WorkTurn)}<div class="day">{dayLabel(turn.started.at, Date.now())}</div>{/snippet}
    {#snippet row(turn: WorkTurn)}
      {@const previous = turns[turns.indexOf(turn) - 1]}
      {#if !previous || new Date(previous.started.at).toDateString() !== new Date(turn.started.at).toDateString()}
        <div class="day">{dayLabel(turn.started.at, Date.now())}</div>
      {/if}
      <article class="turn">
        <div class="cause"><time datetime={turn.started.at}>{formatTime(turn.started.at)}</time>
          {#each turn.inputs as input (input.seq)}
            <span>from <strong>{input.from.kind === "owner" ? "You" : input.from.session ? name(input.from.session) : "Dyna"}</strong>:
              {input.type === "post.created" ? input.body.text : describe(input, name)}</span>
          {:else}<span>Started work</span>{/each}
        </div>
        {#each turn.posts.filter(post => post.body.home.kind === "feed") as post (post.seq)}
          <div class="post"><time datetime={post.at}>{formatTime(post.at)}</time><div><div class="recipient">{post.body.mentions?.length === 1 && turn.inputs.some(input => input.from.session && input.from.session === post.body.mentions?.[0].session) ? "Reply" : `to ${post.body.mentions?.length ? post.body.mentions.map(person => person.kind === "owner" ? "You" : person.session ? name(person.session) : "Dyna").join(", ") : "the room"}`} <button type="button" onclick={() => onfeed(post.seq)}>Open in the feed</button></div><p>{post.body.text}</p></div></div>
        {/each}
        {#if turn.posts.some(post => post.body.home.kind === "dm") || turn.ended?.body.replies?.some(reply => reply.reply.home?.kind === "dm")}<p class="answer">Answered you in Messages</p>{/if}
        {#if !turn.ended && store.activity.get(session)}<p class="answer">{store.activity.get(session)}</p>{/if}
        {#if turn.ended?.body.outcome === "failed"}
          {@const end = turn.ended}
          <FailedTurnRow row={{ kind: "failed", key: end.id, seq: end.seq, session, at: end.at, label: "Turn failed", cause: end.body.cause ? causes[end.body.cause] : "" }} author={name(session)} root={!store.sessions.get(session)?.parent} onseen={() => store.see({ kind: "dm", session }, end.seq)}>
            {#if end.body.usage.model}<details><summary>Details</summary><p>Model: {end.body.usage.model}</p></details>{/if}
          </FailedTurnRow>
        {/if}
      </article>
    {/snippet}
  </VirtualList>
{:else if !loading && !failed}<p class="hint">This session has not worked yet.</p>{/if}
{#if failed}<p class="hint" role="alert">Work did not load. <button type="button" onclick={() => void load()}>Try again</button></p>{/if}
{#if more}<button type="button" class="older" disabled={loading} onclick={() => void load(true)}>Show older</button>{/if}

<style>
  .work-head { display: flex; align-items: center; gap: 12px; padding: 8px 16px; }
  .work-head strong { font-size: 14px; }
  button { border: 0; background: none; color: var(--muted); cursor: pointer; font: inherit; }
  button:hover { color: var(--text); }
  button:focus-visible { outline: 2px solid var(--focus); outline-offset: 2px; }
  .latest { margin-left: auto; }
  .day { padding: 8px 16px; background: var(--bg); color: var(--text); font-size: 12.5px; font-weight: 600; }
  .turn { padding: 6px 16px; border-top: 1px solid var(--line); }
  .cause, .post { display: grid; grid-template-columns: 56px minmax(0,1fr); gap: 8px; }
  .cause { color: var(--muted); font-size: 12.5px; overflow-wrap: anywhere; }
  .cause strong { color: var(--text); }
  .post { margin-top: 4px; font-size: 14px; }
  .post time, .cause time { color: var(--muted); font-size: 11px; white-space: nowrap; font-variant-numeric: tabular-nums; }
  .post p { margin: 2px 0; white-space: pre-wrap; overflow-wrap: anywhere; }
  .recipient, .answer { color: var(--muted); font-size: 12.5px; }
  .answer { margin: 4px 0 4px 64px; }
  .post button { margin-left: 8px; padding: 0; font-size: 12.5px; }
  .hint { padding: 8px 16px; color: var(--muted); }
  .older { padding: 8px 16px; align-self: center; }
  @media (hover: none) { button { min-height: 40px; } }
</style>
