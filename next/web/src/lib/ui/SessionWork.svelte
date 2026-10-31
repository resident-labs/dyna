<script lang="ts">
  import { tick, untrack } from "svelte";
  import { readWork, type WorkTurn } from "../session-work.ts";
  import { causes, dayLabel, describe, formatTime } from "../feed.ts";
  import { hue } from "../identity.ts";
  import { isLiveTurn } from "../work-steps.ts";
  import { SvelteSet } from "svelte/reactivity";
  import { TurnTranscripts } from "../transcripts.svelte.ts";
  import SessionTurnSteps from "./SessionTurnSteps.svelte";
  import type { Principal } from "../contract.gen.ts";
  import type { RoomStore } from "../room.svelte.ts";
  import Avatar from "./Avatar.svelte";
  import VirtualList from "./VirtualList.svelte";
  import FailedTurnRow from "./FailedTurnRow.svelte";
  import StatusPill from "./StatusPill.svelte";

  let { store, session, onfeed, ontotal }: { store: RoomStore; session: string; onfeed: (seq: number) => void; ontotal: (total: number) => void } = $props();
  let turns: WorkTurn[] = $state([]);
  let more = $state(false), loading = $state(false), failed = $state(false);
  let refreshPending = false;
  let active: Promise<void> | undefined;
  let list: VirtualList<WorkTurn> | undefined = $state();
  let failures = $derived(turns.filter(turn => turn.ended?.body.outcome === "failed"));
  let failureIndex = 0;
  const opened = new SvelteSet<string>();
  // The keyed view belongs to one session; transcript state survives virtual row recycling.
  const transcripts = untrack(() => new TurnTranscripts(session));
  function toggle(key: string) { list?.hold(); if (!opened.delete(key)) opened.add(key); }
  const name = (id: string) => store.names.get(id) ?? "Closed session";
  const author = (person: Principal) => person.kind === "owner" ? "You" : person.session ? name(person.session) : "Dyna";
  const day = (value: string) => new Date(value).toDateString();
  function dayCounts(turn: WorkTurn): string {
    const key = day(turn.started.at);
    if (more && turns.length && day(turns[turns.length - 1].started.at) === key) return "";
    const group = turns.filter(item => day(item.started.at) === key);
    const posts = group.reduce((sum, item) => sum + item.posts.length, 0);
    return `${group.length} ${group.length === 1 ? "turn" : "turns"} · ${posts} ${posts === 1 ? "post" : "posts"}`;
  }
  async function load(older = false) {
    if (loading) {
      if (!older) refreshPending = true;
      await active;
      return;
    }
    loading = true;
    let finish: () => void = () => {};
    active = new Promise(resolve => { finish = resolve; });
    const before = older ? turns.at(-1)?.started.body.turn : undefined;
    const page = await readWork(session, before).catch(() => undefined);
    if (page) {
      if (older) { turns = [...turns, ...page.turns]; more = page.more; }
      else {
        const ids = new Set(page.turns.map(turn => turn.started.body.turn));
        const overlaps = turns.some(turn => ids.has(turn.started.body.turn));
        const retained = turns.length && !overlaps ? [] : turns.filter(turn => !ids.has(turn.started.body.turn));
        turns = [...page.turns, ...retained];
        if (!retained.length) more = page.more;
      }
      ontotal(page.total);
      failed = false;
    } else failed = true;
    loading = false;
    if (refreshPending) { refreshPending = false; await load(); }
    finish();
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
  async function latest() {
    await load();
    await tick();
    await list?.reveal(0);
  }
</script>

{#snippet identity(person: Principal)}
  {@const label = author(person)}
  <span class="identity"><Avatar kind={person.kind} name={label} root={!!person.session && !!store.sessions.get(person.session) && !store.sessions.get(person.session)?.parent} size={22} /><strong class={person.kind} style:--hue={hue(label)}>{label}</strong></span>
{/snippet}
{#snippet dayHeader(turn: WorkTurn)}
  <div class="day"><strong>{dayLabel(turn.started.at, Date.now())}</strong>{#if dayCounts(turn)}<span>{dayCounts(turn)}</span>{/if}<span class="rule"></span></div>
{/snippet}

<div class="work-head">
  {#if failures.length}<button type="button" class="failed-link" onclick={nextFailure}><StatusPill tone="failed" label={`Failed ${failures.length}`} /></button>{/if}
  <button type="button" class="latest" onclick={() => void latest()}>Jump to latest</button>
</div>
{#if turns.length}
  <VirtualList bind:this={list} items={turns} key={turn => turn.started.body.turn} label="Session work" role="group" align="top">
    {#snippet sticky(turn: WorkTurn)}{@render dayHeader(turn)}{/snippet}
    {#snippet row(turn: WorkTurn)}
      {@const previous = turns[turns.indexOf(turn) - 1]}
      {#if !previous || day(previous.started.at) !== day(turn.started.at)}{@render dayHeader(turn)}{/if}
      <article class="turn">
        <div class="cause"><time datetime={turn.started.at}>{formatTime(turn.started.at)}</time><div class="cause-line">
          {#each turn.inputs as input (input.seq)}
            <span>from {@render identity(input.from)}: {input.type === "post.created" ? input.body.text : describe(input, name)}</span>
          {:else}<span>Started work</span>{/each}
          {#if turn.ended?.body.outcome === "failed"}<StatusPill tone="failed" label="Failed" />{/if}
        </div></div>
        {#each turn.posts.filter(post => post.body.home.kind === "feed") as post (post.seq)}
          <div class="post">
            <div class="recipient">
              {#if post.body.mentions?.length === 1 && turn.inputs.some(input => input.from.session && input.from.session === post.body.mentions?.[0].session)}
                <span>Reply</span>
              {:else if post.body.mentions?.length}
                <span>to {#each post.body.mentions as person, i}{#if i}, {/if}{@render identity(person)}{/each}</span>
              {:else}<span>to the room</span>{/if}
              <button type="button" onclick={() => onfeed(post.seq)}>Open in the feed</button>
              <time datetime={post.at}>{formatTime(post.at)}</time>
            </div>
            <p>{post.body.text}</p>
          </div>
        {/each}
        <SessionTurnSteps {turn} {transcripts} {opened} ontoggle={toggle} live={isLiveTurn(turn.started.body.turn, turns[0].started.body.turn, store.turns.get(session))} activity={store.activity.get(session)} answered={turn.posts.some(post => post.body.home.kind === "dm") || !!turn.ended?.body.replies?.some(reply => reply.reply.home?.kind === "dm")} />
        {#if turn.ended?.body.outcome === "failed"}
          {@const end = turn.ended}
          <div class="failure"><FailedTurnRow compact row={{ kind: "failed", key: end.id, seq: end.seq, session, at: end.at, label: "Turn failed", cause: end.body.cause ? causes[end.body.cause] : "The turn failed" }} author={name(session)} root={!store.sessions.get(session)?.parent} onseen={() => store.see({ kind: "dm", session }, end.seq)}>
            <details><summary>Details</summary><p>The session stopped before it finished this turn.</p></details>
          </FailedTurnRow></div>
        {/if}
      </article>
      {#if more && turn === turns[turns.length - 1]}<button type="button" class="older" disabled={loading} onclick={() => void load(true)}>Show older</button>{/if}
    {/snippet}
  </VirtualList>
{:else if !loading && !failed}<p class="hint">This session has not worked yet.</p>{/if}
{#if failed}<p class="hint" role="alert">Work did not load. <button type="button" onclick={() => void load()}>Try again</button></p>{/if}

<style>
  .work-head { display: flex; align-items: center; gap: 12px; padding: 4px 16px; }
  button { border: 0; background: none; color: var(--muted); cursor: pointer; font: inherit; }
  button:hover { color: var(--text); }
  button:focus-visible, summary:focus-visible { outline: 2px solid var(--focus); outline-offset: 2px; }
  .latest { margin-left: auto; }
  .day { display: flex; align-items: center; gap: 10px; padding: 10px 16px; background: var(--bg); color: var(--muted); font-size: 12.5px; }
  .day strong { color: var(--text); }
  .rule { height: 1px; background: var(--line); flex: 1; }
  .turn { padding: 12px 16px 16px; }
  .cause { display: grid; grid-template-columns: minmax(64px, max-content) minmax(0,1fr); gap: 8px; color: var(--muted); font-size: 13px; }
  .cause-line { display: flex; align-items: center; flex-wrap: wrap; gap: 7px; overflow-wrap: anywhere; }
  .identity { display: inline-flex; align-items: center; gap: 5px; vertical-align: middle; }
  .identity strong { color: oklch(var(--id-name) var(--hue)); }
  .identity strong.owner, .identity strong.engine { color: var(--text); }
  time { color: var(--muted); font-size: 12.5px; white-space: nowrap; font-variant-numeric: tabular-nums; }
  .post { margin: 12px 0 0 64px; }
  .recipient { display: flex; align-items: center; gap: 8px; color: var(--muted); font-size: 13px; }
  .recipient time { margin-left: auto; }
  .post p { margin: 5px 0 0; font-size: 15px; line-height: 1.5; white-space: pre-wrap; overflow-wrap: anywhere; }
  .post button { padding: 0; font-size: 12px; }
  .failure { margin: 12px 0 0 64px; }
  .hint { padding: 8px 16px; color: var(--muted); }
  .older { display: block; margin: 0 auto; padding: 8px 16px; }
  @media (max-width: 600px) {
    .post, .failure { margin-left: 0; }
  }
  @media (pointer: coarse) { button, summary { min-height: 40px; } }
</style>
