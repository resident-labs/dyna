<script lang="ts">
  import { APIError, call } from "../client.ts";
  import { causes, commandLine, fromNow } from "../feed.ts";
  import { hue } from "../identity.ts";
  import Avatar from "./Avatar.svelte";
  import StatusPill from "./StatusPill.svelte";
  import type { Request, RoomStore } from "../room.svelte.ts";
  import type { OpState, Principal } from "../contract.gen.ts";
  let { request, store, now }: { request: Request; store: RoomStore; now: number } = $props();
  const name = (who: Principal) => who.kind === "owner" ? "You" : who.session ? store.names.get(who.session) ?? "A closed session" : "Dyna";
  const outcome: Record<OpState, string> = {
    pending: "", approved: "Approved", denied: "Denied", expired: "Expired", withdrawn: "Withdrawn", done: "Approved", failed: "Approved",
  };
  // The root session is the open session with no parent.
  let open = $derived(request.by.session ? store.sessions.get(request.by.session) : undefined);
  let root = $derived(open !== undefined && !open.parent);
  // An agent-class request may also be decided by the requester's parent session.
  let parent = $derived(open?.parent && store.names.get(open.parent));
  // One muted line for a decided request, such as "Denied by you · just now · “Use CI”". The engine decides expiry.
  // A decision can be newer than the panel's half-minute clock, so it never reads as in the future.
  let decided = $derived.by(() => {
    const decision = request.decision;
    return [
      outcome[request.state] + (decision && decision.by.kind !== "engine" ? ` by ${decision.by.kind === "owner" ? "you" : name(decision.by)}` : ""),
      decision && fromNow(decision.at, Math.max(now, Date.parse(decision.at))), request.state === "done" && "ran", decision?.reason && request.state !== "expired" && `“${decision.reason}”`,
      // The engine's error text is raw (r8); the Run failed pill and the exit code say what happened.
      request.state === "failed" && request.result?.exitCode && `exited with code ${request.result.exitCode}`,
    ].filter(part => typeof part === "string" && part !== "").join(" · ");
  });
  let reason = $state("");
  let busy = $state(false);
  let failure = $state("");
  // The card moves to Decided when the op.decided event arrives; the answer itself carries nothing.
  async function decide(approve: boolean) {
    busy = true;
    failure = "";
    await call("op.decide", { id: request.id, approve, reason: reason.trim() || undefined }).catch((error: unknown) => {
      failure = error instanceof APIError && error.detail.code === "conflict" ? "This request is no longer waiting." : "Your decision was not saved. Try again.";
    });
    busy = false;
  }
</script>

<!-- While it waits, a request is a card with the exact text that runs; an owner-class card has the attention edge. Decided, it is a quiet row. -->
<article class="request" class:decided={request.state !== "pending"} class:card={request.state === "pending"} class:yours={request.state === "pending" && request.class === "owner"}>
  <p class="title"><Avatar kind={request.by.kind} name={name(request.by)} {root} size={24} />
    <span class="who {request.by.kind}" class:root style:--hue={hue(name(request.by))}>{name(request.by)}</span><span class="asks">requests</span>{request.card.title}
    {#if request.card.target.kind !== "room"}<span class="asks">on {request.card.target.kind} {request.card.target.name}</span>{/if}</p>
  {#snippet exact()}<div class="exact">
    {#if request.card.command?.length}<p>{commandLine(request.card.command)}</p>{/if}
    {#if request.card.body}<p>{request.card.body}</p>{/if}
    {#each request.card.changes ?? [] as change (change.field)}<p><span class="field">{change.field}:</span> {change.old || "none"} → {change.new || "none"}</p>{/each}
  </div>{/snippet}
  <!-- A decided row keeps the exact text behind a fold, so the owner can still check what ran or was refused. -->
  {#if request.card.body || request.card.command?.length || request.card.changes?.length}
    {#if request.state === "pending"}{@render exact()}{:else}<details><summary>Details</summary>{@render exact()}</details>{/if}{/if}
  {#if request.state === "pending"}
    <p class="meta">{request.class === "owner" ? "Nothing runs until you approve." : parent ? `${parent} or you can decide.` : ""} Expires {fromNow(request.expires, now)}.</p>
    <input type="text" bind:value={reason} placeholder="Note (optional)" aria-label="Note to the session (optional)" disabled={busy} />
    <div class="actions">
      <button type="button" class="approve" onclick={() => void decide(true)} disabled={busy}>Approve</button>
      <button type="button" onclick={() => void decide(false)} disabled={busy}>Deny</button>
    </div>
    {#if failure}<p class="meta" role="alert">{failure}</p>{/if}
  {:else}
    <p class="meta">{#if request.state === "failed"}<StatusPill tone="failed">Run failed</StatusPill>
      {#if request.result?.cause}<span class="cause">{causes[request.result.cause]}</span>{/if}{/if}<span>{decided}</span></p>
  {/if}
</article>

<style>
  .request { display: grid; gap: 8px; padding: 12px 16px; border-bottom: 1px solid var(--line); font-size: 14px; }
  .card { max-width: 760px; margin: 8px 16px; padding: 14px 16px; border: 1px solid var(--line); border-radius: 12px; background: var(--raised); }
  .yours { border-left: 3px solid var(--attention); padding-left: 14px; }
  .decided { gap: 4px; color: var(--muted); }
  .title { margin: 0; display: flex; flex-wrap: wrap; align-items: center; column-gap: 6px; font-size: 16px; line-height: 1.25; font-weight: 600; overflow-wrap: anywhere; }
  .who { color: oklch(var(--id-name) var(--hue)); }
  .who.owner { color: var(--focus); }
  .who.engine, .who.root { color: var(--text); }
  .asks { color: var(--muted); font-weight: 400; }
  .decided .title { font-size: 14px; font-weight: 400; }
  .decided .who { color: inherit; }
  .exact { padding: 8px 12px; border-radius: 6px; background: var(--bg); font: 13px/1.5 var(--mono); font-variant-ligatures: none; max-height: 320px; overflow: auto; }
  .exact p { margin: 0; white-space: pre-wrap; overflow-wrap: anywhere; }
  details { font-size: 12.5px; }
  summary { width: fit-content; padding: 2px 4px; margin-left: -4px; border-radius: 6px; cursor: pointer; }
  summary:hover { background: var(--hover); color: var(--text); }
  details .exact { margin-top: 4px; color: var(--text); }
  .meta { margin: 0; display: flex; flex-wrap: wrap; align-items: center; gap: 6px; color: var(--muted); font-size: 12.5px; font-variant-numeric: tabular-nums; }
  .cause { color: var(--text); }
  .field { color: var(--muted); }
  .actions { display: flex; gap: 8px; }
  input { max-width: 480px; padding: 6px 10px; border: 1px solid var(--line); border-radius: 6px; background: var(--bg); color: var(--text); font: inherit; font-size: 13px; }
  button { padding: 6px 14px; border: 1px solid var(--line); border-radius: 6px; background: none; color: var(--text); font: inherit; font-size: 13px; font-weight: 500; cursor: pointer; }
  button:hover:not(:disabled) { background: var(--hover); }
  .approve { background: var(--focus); color: var(--bg); border-color: var(--focus); }
  .approve:hover:not(:disabled) { background: var(--focus); filter: brightness(1.08); }
  :disabled { opacity: 0.6; cursor: default; }
  :focus-visible { outline: 2px solid var(--focus); outline-offset: 2px; }
</style>
