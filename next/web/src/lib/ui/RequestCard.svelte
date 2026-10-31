<script lang="ts">
  import { APIError, call } from "../client.ts";
  import { fromNow } from "../feed.ts";
  import Avatar from "./Avatar.svelte";
  import StatusPill from "./StatusPill.svelte";
  import type { Request, RoomStore } from "../room.svelte.ts";
  import type { OpState, Principal } from "../contract.gen.ts";
  let { request, store, now }: { request: Request; store: RoomStore; now: number } = $props();
  const name = (who: Principal) => who.kind === "owner" ? "You" : who.session ? store.names.get(who.session) ?? "A closed session" : "Dyna";
  const outcome: Record<OpState, string> = {
    pending: "", approved: "Approved", denied: "Denied", expired: "Expired", withdrawn: "Withdrawn", done: "Approved and ran", failed: "Approved",
  };
  // The root session is the open session with no parent.
  let root = $derived.by(() => { const open = request.by.session ? store.sessions.get(request.by.session) : undefined; return open !== undefined && !open.parent; });
  let target = $derived(request.card.target.kind === "room" ? "this room" : `${request.card.target.kind} ${request.card.target.name}`);
  let reason = $state("");
  let busy = $state(false);
  let failure = $state("");
  // The card moves to Decided when the op.decided event arrives; the answer itself carries nothing.
  async function decide(approve: boolean) {
    busy = true;
    failure = "";
    await call("op.decide", { id: request.id, approve, reason: approve ? undefined : reason.trim() || undefined }).catch((error: unknown) => {
      failure = error instanceof APIError && error.detail.code === "conflict" ? "This request is no longer waiting." : "Your decision was not saved. Try again.";
    });
    busy = false;
  }
</script>

<article class="request" class:decided={request.state !== "pending"}>
  <strong>{request.card.title}</strong>
  <p class="meta"><Avatar kind={request.by.kind} name={name(request.by)} {root} size={20} /><span class="who">{name(request.by)}</span>
    <span>· {target}</span>{#if request.state === "pending"}<span>· expires {fromNow(request.expires, now)}</span>{/if}</p>
  {#if request.card.body}<p class="body">{request.card.body}</p>{/if}
  {#if request.card.command?.length}<pre>{request.card.command.join(" ")}</pre>{/if}
  {#if request.card.changes?.length}<ul class="changes">{#each request.card.changes as change (change.field)}
    <li><span class="field">{change.field}:</span> {change.old || "none"} → {change.new || "none"}</li>{/each}</ul>{/if}
  {#if request.state === "pending"}
    <div class="actions">
      <input type="text" bind:value={reason} placeholder="Reason for Deny (optional)" aria-label="Reason for Deny (optional)" disabled={busy} />
      <button type="button" onclick={() => void decide(false)} disabled={busy}>Deny</button>
      <button type="button" class="approve" onclick={() => void decide(true)} disabled={busy}>Approve</button>
    </div>
    {#if failure}<p class="meta" role="alert">{failure}</p>{/if}
  {:else}
    <p class="meta">{#if request.state === "failed"}<StatusPill tone="failed">Run failed</StatusPill>{/if}
      {outcome[request.state]}{#if request.decision && request.decision.by.kind !== "engine"} by {name(request.decision.by)}{/if}
      {#if request.decision}· {fromNow(request.decision.at, now)}{/if}{#if request.decision?.reason && request.state !== "expired"} · “{request.decision.reason}”{/if}
      {#if request.result?.error}· {request.result.error}{/if}</p>
  {/if}
</article>

<style>
  .request { display: grid; gap: 4px; padding: 12px 16px; border-bottom: 1px solid var(--line); font-size: 14px; }
  .decided { color: var(--muted); }
  .meta { margin: 0; display: flex; flex-wrap: wrap; align-items: center; gap: 6px; color: var(--muted); font-size: 12px; font-variant-numeric: tabular-nums; }
  .who { color: var(--text); }
  .decided .who { color: inherit; }
  .body { margin: 0; white-space: pre-wrap; overflow-wrap: anywhere; }
  pre { margin: 0; padding: 8px 10px; border-radius: 8px; background: var(--sidebar); font: 13px ui-monospace, monospace; white-space: pre-wrap; overflow-wrap: anywhere; }
  .changes { margin: 0; padding: 0; list-style: none; font: 13px ui-monospace, monospace; }
  .field { color: var(--muted); }
  .actions { display: flex; gap: 8px; margin-top: 4px; }
  input { flex: 1; min-width: 0; max-width: 320px; padding: 5px 8px; border: 1px solid var(--line); border-radius: 6px; background: var(--bg); color: var(--text); font: inherit; font-size: 13px; }
  button { padding: 5px 12px; border: 1px solid var(--line); border-radius: 6px; background: var(--raised); color: var(--text); font: inherit; font-size: 13px; cursor: pointer; }
  button:hover:not(:disabled) { background: var(--hover); }
  .approve { background: var(--text); color: var(--bg); border-color: var(--text); }
  .approve:hover:not(:disabled) { background: var(--text); opacity: 0.88; }
  :disabled { opacity: 0.6; cursor: default; }
  :focus-visible { outline-offset: 2px; }
</style>
