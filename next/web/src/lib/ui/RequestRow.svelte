<script lang="ts">
  import { requestDecision } from "../request-decision.ts";
  import { closedSession, commandLine, fromNow } from "../feed.ts";
  import { hue } from "../identity.ts";
  import type { Request, RoomStore } from "../room.svelte.ts";
  import Avatar from "./Avatar.svelte";
  import StatusPill from "./StatusPill.svelte";

  // The discriminant leaves room for question and terminal rows when their M3 APIs exist.
  type Row = { kind: "approval"; request: Request };
  let { row, store, admin, now, current = false, grouped = false }: {
    row: Row; store: RoomStore; admin: boolean; now: number; current?: boolean; grouped?: boolean;
  } = $props();
  let request = $derived(row.request);
  let name = $derived(request.by.kind === "owner" ? "you" : request.by.session ? store.names.get(request.by.session) ?? closedSession : "Dyna");
  let soon = $derived(Date.parse(request.expires) - now < 300_000);
  let note = $state(false), reason = $state(""), busy = $state(false), failure = $state("");
  export async function decide(approve: boolean) {
    if (busy || !admin) return;
    if (!approve && !note) { note = true; return; }
    busy = true; failure = "";
    await requestDecision(admin, { id: request.id, approve, reason: reason.trim() || undefined })
      .catch(() => { failure = "Your decision was not saved. Try again."; });
    busy = false;
  }
</script>
<article class:current>
  {#if !grouped}<div class="head"><Avatar kind={request.by.kind} {name} size={24} /><strong style:--hue={hue(name)}>{name}</strong>
    <span class="time">{#if soon}<StatusPill tone="warning" word label={"Expires " + fromNow(request.expires, now)} />{:else}Expires {fromNow(request.expires, now)}{/if}</span></div>{/if}
  <p><span class="muted">{request.card.title}</span>{#if request.card.target.kind !== "room" && !request.card.body?.includes(request.card.target.name)}{" on "}<code>{request.card.target.name}</code>{/if}{#if request.card.body}{" · "}{request.card.body}{/if}</p>
  {#each request.card.changes ?? [] as change (change.field)}<p><span class="muted">{change.field}:</span> {change.old || "none"} → {change.new || "none"}</p>{/each}
  {#if request.card.command?.length}<pre>{commandLine(request.card.command)}</pre>{/if}
  {#if admin && note}<input aria-label="Note to the session" placeholder="Add a note" bind:value={reason} disabled={busy} />{/if}
  <div class="actions">{#if admin}<button class="approve" type="button" disabled={busy} onclick={() => void decide(true)}>Approve</button>
    <button type="button" disabled={busy} onclick={() => void decide(false)}>{note ? "Confirm denial" : "Deny"}</button>
    {#if !note}<button class="ghost" type="button" onclick={() => { note = true; }}>Add a note</button>{/if}
    {/if}{#if grouped}<span class="time">{#if soon}<StatusPill tone="warning" word label={"Expires " + fromNow(request.expires, now)} />{:else}Expires {fromNow(request.expires, now)}{/if}</span>{/if}
  </div>
  {#if failure}<p class="failure" role="alert">{failure}</p>{/if}
</article>
<style>
  article { padding: 14px 16px; border-bottom: 1px solid var(--line); font-size: 14px; }
  article.current { background: var(--selected); }
  .head, .actions { display: flex; align-items: center; gap: 8px; }
  strong { color: oklch(var(--id-name) var(--hue)); font-weight: 600; overflow-wrap: anywhere; }
  .time { margin-left: auto; color: var(--muted); font-size: 12.5px; white-space: nowrap; }
  p { margin: 8px 0; white-space: pre-wrap; overflow-wrap: anywhere; }
  .muted { color: var(--muted); }
  code { font: 13px var(--mono); overflow-wrap: anywhere; }
  pre { margin: 8px 0; padding: 10px; border-radius: 6px; background: var(--bg); font: 13px/1.5 var(--mono); white-space: pre-wrap; overflow-wrap: anywhere; }
  .actions { flex-wrap: wrap; margin-top: 10px; }
  button { min-height: 30px; padding: 0 12px; border: 1px solid var(--line); border-radius: 6px; background: none; color: var(--text); font: inherit; font-size: 13px; white-space: nowrap; cursor: pointer; }
  button:hover { background: var(--hover); }
  .approve { background: var(--focus); color: var(--bg); border-color: var(--focus); }
  .ghost { border-color: transparent; color: var(--muted); }
  input { box-sizing: border-box; width: 100%; padding: 8px; background: var(--bg); border: 1px solid var(--line); border-radius: 6px; color: var(--text); font: inherit; }
  :focus-visible { outline: 2px solid var(--focus); outline-offset: 2px; }
  .failure { color: var(--muted); }
  @media(max-width: 640px) { button { min-height: 40px; } }
</style>
