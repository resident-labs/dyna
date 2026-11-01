<script lang="ts">
  import { APIError, call } from "../client.ts";
  import { causes, clause, commandLine, fromNow, requestLine } from "../feed.ts";
  import { hue } from "../identity.ts";
  import Avatar from "./Avatar.svelte";
  import StatusPill from "./StatusPill.svelte";
  import type { Request, RoomStore } from "../room.svelte.ts";
  import type { OpState, Principal } from "../contract.gen.ts";
  // feed places the card in the post column, where the request happened. gone marks a request that no longer waits
  // although its decision is unknown here, so it shows as decided without one. On the Requests page a waiting request
  // is folded to one row until it opens; current marks the row the keys act on. Approve is filled only on the open
  // card and the current row.
  let { request, store, now, admin, feed = false, gone = false, folded = false, current = false, onopen }: {
    request: Request; store: RoomStore; now: number; admin: boolean; feed?: boolean; gone?: boolean; folded?: boolean; current?: boolean; onopen?: () => void;
  } = $props();
  const name = (who: Principal) => who.kind === "owner" ? "you" : who.session ? store.names.get(who.session) ?? "A closed session" : "Dyna";
  const outcome: Record<OpState, string> = {
    pending: "", approved: "approved", running: "approved", denied: "denied", expired: "expired", withdrawn: "withdrawn", done: "approved", failed: "approved",
  };
  // The root session is the open session with no parent.
  let open = $derived(request.by.session ? store.sessions.get(request.by.session) : undefined);
  let root = $derived(open !== undefined && !open.parent);
  // The body's first line says what runs; the lines after it, such as the reason, are muted.
  let pending = $derived(request.state === "pending" && !gone);
  let [lead = "", ...rest] = $derived((request.card.body ?? "").split("\n"));
  // A host or repository target shows in mono: inside the first line when the line names it, else after the title.
  let named = $derived(request.card.target.kind !== "room" ? request.card.target.name : "");
  let line = $derived(requestLine(request.card));
  // The engine decides expiry; the owner sees it in the attention color for its last 5 minutes.
  let soon = $derived(Date.parse(request.expires) - now < 5 * 60_000);
  // One muted line for a decided request, such as "builder-feed · host access · denied by you · just now · “Use CI”".
  // A decision can be newer than the half-minute clock, so it never reads as in the future.
  let decided = $derived.by(() => {
    const decision = request.decision;
    return [
      outcome[request.state] + (decision && decision.by.kind !== "engine" ? ` by ${decision.by.kind === "owner" ? "you" : name(decision.by)}` : ""),
      decision && fromNow(decision.at, Math.max(now, Date.parse(decision.at))), request.state === "done" && "ran", decision?.reason && request.state !== "expired" && `“${decision.reason}”`,
      // The engine's error text is raw (r8); the Run failed pill and the exit code say what happened.
      request.state === "failed" && request.result?.exitCode && `exited with code ${request.result.exitCode}`,
    ].filter(part => typeof part === "string" && part !== "").join(" · ");
  });
  let exact = $derived(Boolean(request.card.command?.length || request.card.changes?.length || request.card.body));
  let details = $state(false);
  let noting = $state(false);
  let reason = $state("");
  let busy = $state(false);
  let failure = $state("");
  // A title is the phrase after the asker's name; a decided line keeps only what was asked for.
  let asked = $derived(request.card.title.replace(/^asks (for|to) /, ""));
  // The card becomes a muted line when the op.decided event arrives; the answer itself carries nothing.
  // The Requests page calls it for the A and D keys.
  export async function decide(approve: boolean) {
    if (busy) return;
    busy = true;
    failure = "";
    await call("op.decide", { id: request.id, approve, reason: reason.trim() || undefined }).catch((error: unknown) => {
      failure = error instanceof APIError && error.detail.code === "conflict" ? "This request is no longer waiting." : "Your decision was not saved. Try again.";
    });
    busy = false;
  }
</script>

{#snippet who()}<Avatar kind={request.by.kind} name={name(request.by)} {root} size={pending ? 24 : 20} />
  <strong class={request.by.kind} class:root style:--hue={hue(name(request.by))}>{name(request.by)}</strong>
  <span class="title">{#if pending}{request.card.title}{#if named && !line.named} on <code>{named}</code>{/if}{:else}{asked}{/if}</span>{/snippet}

{#snippet ask()}{#if line.named}{line.text}<code>{line.named}</code>{clause(line.after)}{:else}{clause(lead)}{/if}{/snippet}

{#snippet expiry()}{#if soon}<StatusPill tone="warning" label="Expires {fromNow(request.expires, now)}" />{:else}Expires {fromNow(request.expires, now)}{/if}{/snippet}

<!-- While it waits, a request reads as a sentence on a card, with the exact command that runs; an owner-class card has the attention edge.
     Folded on the Requests page, it is one row: the sentence, what it allows, and its own Approve and Deny. -->
{#if pending && folded}
  <article class="row" class:current>
    <button type="button" class="head" aria-expanded="false" onclick={onopen}><span class="top">{@render who()}</span>
      {#if lead}<span class="sub">{@render ask()}{#if request.class === "owner"} · {@render expiry()}{/if}</span>{/if}
      {#if !lead}<span class="sub">{admin ? "You can decide" : "Needs a device with full access"} · {@render expiry()}</span>{/if}</button>
    {#if admin}<div class="actions">
      <button type="button" class:approve={current} onclick={() => void decide(true)} disabled={busy}>Approve</button>
      <button type="button" onclick={() => void decide(false)} disabled={busy}>Deny</button>
    </div>{/if}
    {#if failure}<p class="why" role="alert">{failure}</p>{/if}
  </article>
{:else if pending}
  <article class="card" class:feed class:current class:yours={request.class === "owner"}>
    <p class="top">{@render who()}
      <span class="expires">{@render expiry()}</span></p>
    {#if lead}<p class="what">{line.text}{#if line.named}<code>{line.named}</code>{line.after}{/if}</p>{/if}
    {#if rest.length}<p class="why">{rest.join("\n")}</p>{/if}
    {#if request.card.command?.length}<pre class="exact">{commandLine(request.card.command)}</pre>{/if}
    {#each request.card.changes ?? [] as change (change.field)}<p class="what"><span class="field">{change.field}:</span> {change.old || "none"} → {change.new || "none"}</p>{/each}
    <!-- The page says who decides on every open card; the card body already says what the grant allows. -->
    {#if !feed && admin}<p class="why">You can decide.</p>{/if}
    {#if noting}<!-- svelte-ignore a11y_autofocus -->
      <input type="text" bind:value={reason} placeholder="Note to {name(request.by)}" aria-label="Note to the session" disabled={busy} autofocus />{/if}
    {#if admin}<div class="actions">
      <button type="button" class="approve" onclick={() => void decide(true)} disabled={busy}>Approve</button>
      <button type="button" onclick={() => void decide(false)} disabled={busy}>Deny</button>
      {#if !noting}<button type="button" class="ghost" onclick={() => { noting = true; }}>Add a note</button>{/if}
    </div>{:else}<p class="why">Approve from a device with full access.</p>{/if}
    {#if failure}<p class="why" role="alert">{failure}</p>{/if}
  </article>
{:else}
  <!-- Decided, it is one muted line; the exact text stays behind Details, so the owner can still check what ran or was refused. -->
  <article class="decided" class:feed>
    <p class="line">{@render who()}<span class="outcome">{#if request.state === "failed"}<StatusPill tone="failed" label="Run failed" />
      {#if request.result?.cause}{causes[request.result.cause]} · {/if}{/if}{gone ? "No longer waiting" : decided}</span>
      {#if exact}<button type="button" class="ghost" aria-expanded={details} onclick={() => { details = !details; }}>Details</button>{/if}</p>
    {#if details}<div class="exact">{#if request.card.command?.length}<p>{commandLine(request.card.command)}</p>{/if}
      {#if request.card.body}<p>{request.card.body}</p>{/if}
      {#each request.card.changes ?? [] as change (change.field)}<p>{change.field}: {change.old || "none"} → {change.new || "none"}</p>{/each}</div>{/if}
  </article>
{/if}

<style>
  .card { display: grid; gap: 6px; max-width: 620px; margin: 8px 16px; padding: 12px 16px; border: 1px solid var(--line); border-radius: 12px; background: var(--raised); font-size: 14px; }
  /* In the feed the card starts at the post text's left edge, as the request is part of the conversation. */
  .card.feed { margin-left: var(--gutter); }
  .yours { border-left: 2px solid var(--attention-fill); padding-left: 15px; }
  p { margin: 0; }
  .top, .line { display: flex; align-items: center; gap: 8px; min-width: 0; }
  strong { flex: none; font-weight: 600; color: oklch(var(--id-name) var(--hue)); }
  strong.owner { color: var(--focus); }
  strong.engine, strong.root { color: var(--text); }
  .title { min-width: 0; flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--muted); }
  /* Only the host is mono, as it is a name the owner may type. */
  code { font: 13px var(--mono); color: var(--text); }
  .expires { flex: none; color: var(--muted); font-size: 12.5px; font-variant-numeric: tabular-nums; }
  .what { overflow-wrap: anywhere; }
  .why { color: var(--muted); font-size: 12.5px; white-space: pre-wrap; overflow-wrap: anywhere; }
  .field { color: var(--muted); }
  .exact { margin: 2px 0 0; padding: 8px 12px; border-radius: 6px; background: var(--bg); font: 13px/1.5 var(--mono); font-variant-ligatures: none;
    max-height: 320px; overflow: auto; white-space: pre-wrap; overflow-wrap: anywhere; }
  .exact p { white-space: pre-wrap; }
  input { max-width: 480px; height: 30px; margin-top: 4px; padding: 0 10px; border: 1px solid var(--line); border-radius: 6px; background: var(--bg); color: var(--text); font: inherit; font-size: 13px; }
  input:focus-visible { border-color: var(--focus); outline: none; box-shadow: 0 0 0 1px var(--focus); }
  .actions { display: flex; gap: 8px; margin-top: 6px; }
  button { height: 30px; padding: 0 12px; border: 1px solid var(--line); border-radius: 6px; background: none; color: var(--text); font: inherit; font-size: 13px; font-weight: 500; cursor: pointer; }
  button:hover:not(:disabled) { background: var(--hover); }
  .approve { background: var(--focus); color: var(--bg); border-color: var(--focus); }
  .approve:hover:not(:disabled) { background: var(--focus); filter: brightness(1.08); }
  .ghost { border-color: transparent; color: var(--muted); }
  .ghost:hover:not(:disabled) { color: var(--text); }
  :disabled { opacity: 0.6; cursor: default; }
  :focus-visible { outline: 2px solid var(--focus); outline-offset: 2px; }
  /* A folded row on the Requests page: the sentence and one muted line, then outline Approve and Deny. */
  .row { display: flex; align-items: center; gap: 12px; padding: 8px 12px; border-radius: 8px; font-size: 14px; }
  .row.current { background: var(--hover); }
  .row .head { all: unset; flex: 1; min-width: 0; display: grid; gap: 2px; cursor: pointer; border-radius: 6px; }
  .row .head:focus-visible { outline: 2px solid var(--focus); outline-offset: 2px; }
  .sub { padding-left: 32px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--muted); font-size: 12.5px; font-variant-numeric: tabular-nums; }
  .sub code { font-size: 12px; }
  .row .actions { flex: none; margin: 0; }
  .row .why { flex-basis: 100%; }
  /* A decided request is one muted line, in the panel and in the feed's post column. */
  .decided { padding: 8px 16px; color: var(--muted); font-size: 13px; }
  .decided.feed { padding: 4px 16px 4px var(--gutter); }
  .decided strong { color: inherit; font-weight: 500; }
  .decided .title { flex: 0 1 auto; }
  .decided .title::before { content: "· "; }
  .decided .ghost { margin-left: auto; }
  .outcome { flex: none; display: inline-flex; align-items: center; gap: 6px; max-width: 60%; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-variant-numeric: tabular-nums; }
  .outcome::before { content: "·"; }
  .decided .ghost { height: 24px; padding: 0 6px; font-size: 12.5px; font-weight: 400; }
  .decided .exact { color: var(--text); margin-top: 4px; }
  /* On a narrow screen the expiry takes its own line under the name, so the title never truncates first. */
  @media (max-width: 480px) {
    .top { flex-wrap: wrap; row-gap: 2px; }
    .title { white-space: normal; }
    .expires { flex-basis: 100%; padding-left: 32px; }
    /* A decided line keeps the sentence on its first line and puts the outcome under it. */
    .decided .line { flex-wrap: wrap; row-gap: 2px; }
    .decided .title { flex: 1; white-space: nowrap; }
    .outcome { order: 1; flex-basis: 100%; max-width: none; padding-left: 28px; }
    .outcome::before { content: none; }
  }
  /* On a phone the card spans the feed, and Approve and Deny share its width at touch size. */
  @media (max-width: 640px) {
    .card.feed { margin-left: 16px; }
    .decided.feed { padding-left: 16px; }
  }
  /* On a phone a row is its text, then Approve and Deny across the full width at touch size. */
  @media (max-width: 640px) {
    .row { flex-wrap: wrap; }
    .sub { white-space: normal; }
    .row .actions { flex-basis: 100%; }
  }
  @media (max-width: 640px), (hover: none) {
    .actions { display: grid; grid-template-columns: 1fr 1fr; }
    .actions button { height: 40px; }
    .actions .ghost { grid-column: 1 / -1; }
  }
</style>
