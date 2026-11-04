<script lang="ts">
  import { onMount, type Snippet } from "svelte";
  import { APIError, call, SignedOut } from "../client.ts";
  import { approvalOn, autopilotConfirm, autopilotOn, autopilotRequest, rootApprovals, switchRequest } from "../autopilot.ts";
  import { confirm } from "../modal.ts";
  import type { RoomSetRequest, RoomSummary } from "../contract.gen.ts";
  // Settings > Approvals. One card per room with a switch for each safeguard that waits for the owner today. A switch on lets the room's
  // root decide that kind of request; the owner still can. The Autopilot button turns every switch on, or off when all are on.
  let { bar, signedout }: { bar: Snippet<[string, number | undefined, Snippet | undefined]>; signedout: () => void } = $props();
  let rooms: RoomSummary[] = $state.raw([]);
  let loaded = $state(false), failed = $state(false), busy = $state(false);
  let problem: { id: string; text: string } | undefined = $state();

  function reload() {
    return call("room.list", {}).then(known => { rooms = (known.rooms ?? []).filter(room => !room.deleting); loaded = true; failed = false; },
      (error: unknown) => { if (error instanceof SignedOut) signedout(); else if (!loaded) failed = true; });
  }
  onMount(() => { void reload(); window.addEventListener("focus", reload); return () => window.removeEventListener("focus", reload); });

  async function autopilot(room: RoomSummary) {
    const ask = autopilotConfirm(room);
    if (ask && !(await confirm(ask))) return;
    await change(room, autopilotRequest(room));
  }
  async function change(room: RoomSummary, request: RoomSetRequest) {
    busy = true; problem = undefined;
    await call("room.set", request).then(reload, (error: unknown) => {
      if (error instanceof SignedOut) signedout();
      else problem = { id: room.id, text: error instanceof APIError ? error.detail.message : "That did not work. Try again." };
    });
    busy = false;
  }
</script>

{@render bar("Approvals", loaded ? rooms.length : undefined, undefined)}
<div class="body" data-page-ready={loaded || failed}>
  {#if failed}<p class="load" role="alert">The rooms did not load. <button type="button" class="retry" onclick={() => { failed = false; void reload(); }}>Try again</button></p>
  {:else if !loaded}<p class="load">Loading rooms…</p>
  {:else}
    <p class="load">A switch lets the room's root decide that kind of request, and you still can.</p>
    {#each rooms as room (room.id)}
      <section aria-labelledby={`room-${room.id}`}>
        <header class="scope"><h2 id={`room-${room.id}`}>{room.name}</h2>
          <button type="button" class="btn ghost autopilot" aria-pressed={autopilotOn(room)} disabled={busy} onclick={() => void autopilot(room)}>Autopilot</button></header>
        <div class="list">
          {#each rootApprovals as { kind, label, note } (kind)}
            <label class="row"><span class="lines"><span class="title">{label}</span><span class="line">{note}</span></span>
              <span class="badge">off by default</span>
              <input type="checkbox" role="switch" checked={approvalOn(room, kind)} disabled={busy} aria-label={`${label} in ${room.name}`} onchange={event => void change(room, switchRequest(room, kind, event.currentTarget.checked))} /></label>
          {/each}
        </div>
        {#if problem?.id === room.id}<p class="problem" role="alert">{problem.text}</p>{/if}
      </section>
    {/each}
  {/if}
</div>

<style>
  .body { flex: 1; min-height: 0; display: grid; gap: 20px; align-content: start; width: min(1040px, 100%); padding: 20px 24px; overflow-y: auto; }
  .load { margin: 0; color: var(--muted); font-size: 14px; }
  .retry { padding: 0; border: 0; background: none; color: var(--text); font: inherit; text-decoration: underline; cursor: pointer; }
  section { display: grid; gap: 8px; min-width: 0; }
  .scope { display: flex; align-items: center; gap: 10px; padding: 0 2px; }
  h2 { margin: 0; font-size: 14px; }
  .autopilot { margin-left: auto; }
  .autopilot[aria-pressed="true"] { border-color: var(--focus); }
  .list { display: grid; padding: 6px; border: 1px solid var(--line); border-radius: 12px; background: var(--raised); }
  .row { display: flex; align-items: center; gap: 12px; min-height: 52px; padding: 6px 8px; border-radius: 6px; cursor: pointer; }
  .lines { flex: 1; min-width: 0; display: flex; flex-direction: column; line-height: 1.3; }
  .title { font-size: 14px; font-weight: 600; }
  .line, .badge { color: var(--muted); font-size: 12.5px; }
  input[role="switch"] { appearance: none; flex: none; width: 34px; height: 20px; margin: 0; border: 1px solid var(--firm); border-radius: 10px; background: none; cursor: pointer; position: relative; }
  input[role="switch"]::after { content: ""; position: absolute; top: 3px; left: 3px; width: 12px; height: 12px; border-radius: 50%; background: var(--muted); }
  input[role="switch"]:checked { border-color: var(--focus); background: var(--focus); }
  input[role="switch"]:checked::after { left: 17px; background: var(--bg); }
  input[role="switch"]:disabled { cursor: default; }
  .problem { margin: 0; color: var(--danger); font-size: 13px; font-weight: 500; overflow-wrap: anywhere; }
  @media (max-width: 640px) { .body { padding: 12px 16px; } .badge { display: none; } }
</style>
