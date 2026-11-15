<script lang="ts">
  import { onMount, type Snippet } from "svelte";
  import { APIError, call, SignedOut } from "../client.ts";
  import { approvalChips, approvalOn, approvalsLine, autopilotConfirm, autopilotOn, autopilotRequest, rootApprovals, switchRequest } from "../autopilot.ts";
  import { confirm } from "../modal.ts";
  import type { Column } from "../settings-list.ts";
  import type { RoomSetRequest, RoomSummary } from "../contract.gen.ts";
  import { writeGroups, writeLabel, writeOn, writeRequest, writesRepos } from "../githubwrites.ts";
  import Avatar from "./Avatar.svelte";
  import StatusPill from "./StatusPill.svelte";
  import ItemList from "./settings/ItemList.svelte";
  import ListDetail from "./settings/ListDetail.svelte";
  // Settings > Approvals (spec 5.13): every room with its three root-approves switches as values, and the open room's
  // detail, the one place that says what they do. A switch on lets the room's root decide that kind of request; the
  // owner still can. Autopilot turns all three on, or off when all are on. Keys 1 to 3 flip the switches of the row the
  // list is on. room.list is whole, so the list searches and filters here.
  let { bar, signedout, selected, onselect, onclose }: {
    bar: Snippet<[string, number | undefined, Snippet | undefined]>; signedout: () => void;
    selected: string | undefined; onselect: (room: string, open: boolean) => void; onclose: () => void;
  } = $props();
  let rooms: RoomSummary[] | undefined = $state.raw();
  let failed = $state(false), busy = $state(false);
  let problem: { id: string; text: string } | undefined = $state();
  // What the last key did, for a screen reader: the list keeps the focus, so the switch's own change is not read.
  let said = $state("");
  let room = $derived(rooms?.find(each => each.id === selected));

  function reload() {
    return call("room.list", {}).then(known => { rooms = (known.rooms ?? []).filter(each => !each.deleting); failed = false; },
      (error: unknown) => { if (error instanceof SignedOut) signedout(); else if (!rooms) failed = true; });
  }
  onMount(() => { void reload(); window.addEventListener("focus", reload); return () => window.removeEventListener("focus", reload); });

  async function autopilot(room: RoomSummary) {
    const ask = autopilotConfirm(room);
    if (ask && !(await confirm(ask))) return;
    await change(room, autopilotRequest(room));
  }
  // Whether the engine took the change.
  async function change(room: RoomSummary, request: RoomSetRequest) {
    busy = true; problem = undefined;
    const done = await call("room.set", request).then(async () => { await reload(); return true; }, (error: unknown) => {
      if (error instanceof SignedOut) signedout();
      else problem = { id: room.id, text: error instanceof APIError ? error.detail.message : "That did not work. Try again." };
      return false;
    });
    busy = false;
    return done;
  }
  function key(pressed: string, room: RoomSummary) {
    const item = rootApprovals[Number(pressed) - 1];
    if (!/^[1-3]$/.test(pressed)) return false;
    if (busy) return true;
    const on = !approvalOn(room, item.kind);
    said = `${item.short} ${on ? "on" : "off"} in ${room.name}`;
    void change(room, switchRequest(room, item.kind, on));
    return true;
  }
  const columns: Column<RoomSummary>[] = [
    { id: "name", label: "Room", width: "minmax(0, 1fr)", compare: (a, b) => a.name.localeCompare(b.name) },
    ...rootApprovals.map(({ kind, short }) => ({ id: kind, label: short, width: "64px" })),
  ];
  const count = (test: (room: RoomSummary) => boolean) => rooms?.filter(test).length ?? 0;
</script>

{#snippet toggle(room: RoomSummary, kind: (typeof rootApprovals)[number]["kind"], label: string, inList: boolean)}
  <input type="checkbox" role="switch" checked={approvalOn(room, kind)} disabled={busy} aria-label={label} tabindex={inList ? -1 : undefined}
    onchange={event => { const box = event.currentTarget; void change(room, switchRequest(room, kind, box.checked)).then(done => { if (!done) box.checked = !box.checked; }); }} />
{/snippet}

{#snippet row(room: RoomSummary, compact: boolean)}
  {#if compact}<Avatar kind="room" name={room.name} size={24} /><span class="lines"><span class="title"><span class="name">{room.name}</span>{#if room.waiting?.length}<StatusPill tone="warning" label={`${room.waiting.length} waiting`} />{/if}</span><span class="line">{approvalsLine(room)}</span></span>
  {:else}<span class="title"><Avatar kind="room" name={room.name} size={24} /><span class="name">{room.name}</span>{#if autopilotOn(room)}<span class="badge">Autopilot</span>{/if}{#if room.waiting?.length}<StatusPill tone="warning" label={`${room.waiting.length} waiting`} />{/if}</span>
    {#each rootApprovals as { kind, short } (kind)}<span class="cell">{@render toggle(room, kind, `${short} in ${room.name}`, true)}</span>{/each}{/if}
{/snippet}

{#snippet tools()}
  {#if problem && problem.id !== selected}<p class="problem" role="alert">{rooms?.find(each => each.id === problem!.id)?.name}: {problem.text}</p>{/if}
  <p class="said" role="status">{said}</p>
{/snippet}

{#snippet list({ drill, select }: { drill: boolean; select: (key: string, open: boolean) => void })}
  <ItemList items={rooms} {failed} retry={() => { failed = false; void reload(); }} key={each => each.id} text={each => each.name} noun={["room", "rooms"]} label="Rooms"
    chips={approvalChips} {columns} sort={{ column: "name", dir: 1 }} {row} {tools} {selected} {drill} onselect={select} onkey={key} />
{/snippet}

{#snippet detail()}
  {#if room}
    <p class="small muted">Each switch is off until you turn it on. With one on, this room's root decides that kind of request, and you still can.</p>
    <div class="stack">{#each rootApprovals as { kind, label, note }, index (kind)}
      <label class="set"><span class="name">{label} <kbd title={`Press ${index + 1} on the room's row in the list`}>{index + 1}</kbd></span>{@render toggle(room, kind, label, false)}<span class="help">{note}</span></label>{/each}</div>
    <div class="section"><div class="actions"><button type="button" class="btn" aria-pressed={autopilotOn(room)} disabled={busy} onclick={() => void autopilot(room!)}>Autopilot</button></div>
      <p class="small muted">Turns all three on, or all off when all are on. Turning on asks first.</p></div>
    <div class="section"><h3 class="small">GitHub writes</h3>
      <p class="small muted">Off until you turn a group on. Lets this room's sessions write to GitHub without a host grant, on {writesRepos(room)}; merging, deleting and everything else stay refused.</p>
      <div class="stack">{#each writeGroups as { group, label, note } (group)}
        <label class="set"><span class="name">{label}</span><input type="checkbox" role="switch" checked={writeOn(room, group)} disabled={busy} aria-label={writeLabel(group, room)}
          onchange={event => { const box = event.currentTarget; void change(room!, writeRequest(room!, group, box.checked)).then(done => { if (!done) box.checked = !box.checked; }); }} /><span class="help">{note}</span></label>{/each}</div></div>
    <div class="section"><h3 class="small">Git signing</h3>
      <p class="small muted">Off until you turn it on. This room's boxes then sign their commits and tags as you. Add the key from <code>dyna signing key</code> on GitHub once, as a signing key. Boxes pick up a change at their next start.</p>
      <div class="stack"><label class="set"><span class="name">Sign commits and tags</span><input type="checkbox" role="switch" checked={!!room.gitSigning} disabled={busy} aria-label={`Git signing in ${room.name}`}
        onchange={event => { const box = event.currentTarget; void change(room!, { room: room!.id, gitSigning: box.checked }).then(done => { if (!done) box.checked = !box.checked; }); }} /><span class="help">Commits show as Verified on GitHub.</span></label></div></div>
    {#if problem?.id === room.id}<p class="problem" role="alert">{problem.text}</p>{/if}
  {:else}<p class="muted">That room is gone, or the link is wrong.</p>
  {/if}
{/snippet}

{#snippet summary()}
  {#if rooms}<p class="muted"><span class="fact">{rooms.length.toLocaleString("en")} {rooms.length === 1 ? "room" : "rooms"}</span> <span class="fact">· {count(autopilotOn)} on Autopilot</span>
    <span class="fact">· {count(approvalChips[2].test!)} with some switches on</span> <span class="fact">· {count(approvalChips[3].test!)} all off</span></p>{/if}
{/snippet}

{#snippet header()}{@render bar("Approvals", rooms?.length, undefined)}{/snippet}

<ListDetail bar={header} empty={rooms?.length === 0} label="Approvals" title={room?.name ?? "Not found"} open={!!rooms && selected !== undefined} {onselect} {onclose} {list} {detail} {summary} />

<style>
  .problem { flex-basis: 100%; margin: 0; color: var(--danger); font-size: 13px; font-weight: 500; overflow-wrap: anywhere; }
  .said { position: absolute; width: 1px; height: 1px; overflow: hidden; clip-path: inset(50%); white-space: nowrap; }
  .btn[aria-pressed="true"] { border-color: var(--focus); }
  /* The switch's key in the list, drawn as a key so it does not read as a count; a phone has no keys to press. */
  kbd { margin-left: 4px; padding: 0 5px; border: 1px solid var(--line); border-radius: 4px; background: var(--selected); color: var(--muted); font: 11px/16px var(--mono); vertical-align: 1px; }
  @media (pointer: coarse) { kbd { display: none; } }
</style>
