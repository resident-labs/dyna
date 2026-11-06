<script lang="ts">
  import { onMount, tick, type Snippet } from "svelte";
  import { APIError, call, SignedOut } from "../client.ts";
  import { confirm } from "../modal.ts";
  import { startLabel } from "../start.ts";
  import { boxWords } from "../feed.ts";
  import { diskLow, gpuLine, hostBars, hostChips, hostGroup, hostLine, hostMeters, hostName, hostOrder, hostState, hostText, largeShared, lastSeen, machines, roomSessions, sharedSize,
    watchHosts } from "../hosts.ts";
  import type { HostProblem, HostSummary, RoomSummary } from "../contract.gen.ts";
  import Avatar from "./Avatar.svelte";
  import Icon from "./Icon.svelte";
  import MeterTile from "./MeterTile.svelte";
  import StatusPill from "./StatusPill.svelte";
  import ItemList from "./settings/ItemList.svelte";
  import ListDetail from "./settings/ListDetail.svelte";
  import RoomsServed from "./settings/RoomsServed.svelte";
  // Settings > Hosts (spec 5.13): the machines Dyna places work on, added by the owner or brought by a pool, a row of
  // values each with Home's load at a glance, and the open host's detail, the one place that explains its switches,
  // rooms, problem and removal. Its status reuses Home's pieces: the merge of the engine's machine (machines), the
  // meters and their tiles, the last-seen words and the 10 s poll (watchHosts). Add a host opens a form in the detail's
  // place. host.list is whole, so the list searches and filters here. The pane is admin only (routes.ts), as every host
  // call is the owner's.
  let { bar, phone, navigate, signedout, selected, onselect, onclose }: {
    bar: Snippet<[string, number | undefined, Snippet | undefined]>; phone: boolean; navigate: (event: MouseEvent & { currentTarget: HTMLAnchorElement }) => void;
    signedout: () => void; selected: string | undefined; onselect: (name: string, open: boolean) => void; onclose: () => void;
  } = $props();
  let hosts: HostSummary[] | undefined = $state.raw(), rooms: RoomSummary[] = $state.raw([]);
  let failed = $state(false), busy = $state(false), now = $state(Date.now());
  // A failed change on the open host or the form, in the engine's words.
  let problem = $state("");
  // The host Add just added without swap, whose detail says so.
  let noSwap: string | undefined = $state();
  let items = $derived(hosts && hostOrder(hosts));
  let item = $derived(items?.find(each => each.name === selected));

  // Add: what the host is for, its name and its SSH destination, and the owner's word that only they administer it.
  let creating = $state(false), exec = $state(false), name = $state(""), ssh = $state(""), sole = $state(false);
  // Each form opened, or left, takes a new ticket, so the answer of an Add from a form since left touches nothing.
  let formTicket = 0;
  let ready = $derived(!!name.trim() && !!ssh.trim() && (exec || sole));
  // Inside the detail, to hand its panel the focus when what had it is gone.
  let anchor: HTMLElement | undefined = $state();
  const gone = (reason: unknown) => { if (reason instanceof SignedOut) signedout(); return reason instanceof SignedOut; };
  const words = (reason: unknown) => reason instanceof APIError ? reason.detail.message : "That did not work. Try again.";
  const fail = (reason: unknown) => { if (!gone(reason)) problem = words(reason); };
  const panel = async () => { await tick(); anchor?.closest<HTMLElement>("aside")?.focus(); };
  const roomName = (room: string) => rooms.find(each => each.id === room)?.name ?? "a closed room";

  // A change reads the list again before it settles. A beat that answers meanwhile may hold the list from before the
  // change, so it is dropped, and the next beat comes 10 s later.
  const apply = (list: HostSummary[]) => { hosts = list; now = Date.now(); failed = false; };
  function reload() {
    void call("room.list", {}).then(result => { rooms = result.rooms ?? []; }, () => {});
    return call("host.list", {}).then(list => apply(machines(list.hosts ?? [])), (reason: unknown) => { if (!gone(reason) && !hosts) failed = true; });
  }
  // Add is a step in history of its own, so Back leaves the empty form as the back arrow does.
  function popped() { if (creating && !history.state?.creating) { creating = false; formTicket++; problem = ""; } }
  onMount(() => {
    void call("room.list", {}).then(result => { rooms = result.rooms ?? []; }, () => {});
    window.addEventListener("popstate", popped);
    // A failed beat keeps the last list, and the next one tries again.
    const stop = watchHosts(list => { if (!busy) apply(list); }, () => { if (!hosts) failed = true; });
    return () => { stop(); window.removeEventListener("popstate", popped); };
  });
  // Another host, or none, starts without the last one's problem.
  $effect.pre(() => { void selected; problem = ""; });

  function start() {
    problem = "";
    // An open host stays in the path under the form, so Back returns to it.
    if (!creating) { formTicket++; exec = false; name = ""; ssh = ""; sole = false; history.pushState({ creating: true, overList: selected === undefined }, "", location.href); }
    creating = true;
    void panel();
  }
  function close() {
    if (!creating) { onclose(); return; }
    if (history.state?.creating) history.back();
    else { creating = false; formTicket++; problem = ""; }
  }
  // A host picked while the form is up takes the form's history step, so none is left behind that Back would land on.
  function select(key: string, open: boolean) {
    if (creating && history.state?.creating) history.replaceState({ fromList: !!history.state.overList }, "", location.href);
    if (creating) formTicket++;
    creating = false;
    onselect(key, open);
  }
  // Add waits for the engine's check of the host. The new host opens once the list holds it, in the form's history step,
  // unless the form was left meanwhile.
  async function add() {
    if (busy || !ready) return;
    const host = name.trim(), address = ssh.trim(), ticket = formTicket;
    busy = true; problem = "";
    await call("host.add", exec ? { name: host, ssh: address, ownerOnlyAdmin: false, agents: false, ci: false } : { name: host, ssh: address, ownerOnlyAdmin: true }).then(async result => {
      await reload();
      if (ticket !== formTicket) return;
      creating = false;
      if (history.state?.creating) history.replaceState({ fromList: !!history.state.overList }, "", location.href);
      noSwap = result.noSwap ? host : undefined;
      if (hosts?.some(each => each.name === host)) { onselect(host, false); void panel(); }
    }, (reason: unknown) => { if (ticket === formTicket) fail(reason); else gone(reason); });
    busy = false;
  }
  // Whether the engine took the change; the list is read again before it settles.
  async function change(run: () => Promise<unknown>) {
    busy = true; problem = "";
    const done = await run().then(async () => { await reload(); return true; }, (reason: unknown) => { fail(reason); return false; });
    busy = false;
    return done;
  }
  // Each switch saves at once and keeps the focus. A change while one saves is put back. On an exec-only host, turning
  // one on runs the full host check, so it asks first, and the owner's yes confirms that only they administer the machine.
  async function setSwitch(h: HostSummary, request: { agents: boolean } | { ci: boolean }, box: HTMLInputElement) {
    if (busy) { box.checked = !box.checked; return; }
    problem = "";
    if (h.execOnly && box.checked) {
      const ok = await confirm({ title: `Make ${h.name} a full host?`, message: "Dyna checks Podman, cgroups, FUSE and disk there first, and boxes or CI runners may then run on it.",
        note: "You confirm that only you administer this machine.", action: "Check and turn on", danger: false, opener: box, failed: words,
        run: () => call("host.set", { host: h.name, ...request, ownerOnlyAdmin: true }).catch((reason: unknown) => { gone(reason); throw reason; }) });
      if (ok) await reload();
      else box.checked = false;
      return;
    }
    if (!await change(() => call("host.set", { host: h.name, ...request }))) box.checked = !box.checked;
  }
  async function remove(h: HostSummary, opener: HTMLElement) {
    problem = "";
    const ok = await confirm({ title: `Remove ${h.name}?`, message: h.pool ? `Pool ${h.pool} takes the node back through its script.` : "Dyna drops its connection to the machine. You can add it again later.",
      action: "Remove", danger: true, opener, failed: words, run: () => call("host.remove", { host: h.name }).catch((reason: unknown) => { gone(reason); throw reason; }) });
    if (!ok) return;
    onclose();
    await reload();
  }
  const problems: Record<HostProblem, string> = {
    create_failed: "Box creation failed three times in a row here, so no new session box is made here. Boxes already here keep running. Clear it once the host is fixed.",
    share_unavailable: "The shared folder cannot be mounted here, so no session box starts here; CI runners still may. Fix FUSE on the host, then clear it.",
  };
  const plural = (n: number, one: string, many: string) => `${n.toLocaleString("en")} ${n === 1 ? one : many}`;
</script>

{#snippet row(h: HostSummary, compact: boolean)}{@const state = hostState(h)}{@const bars = compact ? [] : hostBars(h)}
  <span class="icon"><Icon name="monitor" size={20} /></span>
  <span class="lines"><span class="title"><span class="name" title={hostName(h)}>{hostName(h)}</span>{#if h.execOnly}<span class="badge">Exec only</span>{/if}{#if state}<StatusPill tone={state.tone} label={state.label} />{/if}</span>
    <span class="line">{hostLine(h, now)}</span></span>
  {#if bars.length}<span class="bars" title={bars.map(each => `${each.label} ${each.percent}%`).join(" · ")}>
    {#each bars as each (each.label)}<span class="meter"><span class="bl">{each.label}</span><span class="usebar" class:busy={each.busy}><i style:width="{each.percent}%"></i></span></span>{/each}</span>{/if}
{/snippet}

{#snippet empty()}<button type="button" class="btn" onclick={start}>Add a host</button>{/snippet}

{#snippet list({ drill, select: pick }: { drill: boolean; select: (key: string, open: boolean) => void })}
  <ItemList {items} {failed} retry={() => { failed = false; void reload(); }} key={each => each.name} text={hostText} noun={["host", "hosts"]} label="Hosts"
    chips={hostChips} group={hostGroup} {row} {empty} selected={creating ? undefined : selected} {drill} onselect={pick} />
{/snippet}

{#snippet form()}
  <form class="form" aria-label="Add a host" onsubmit={event => { event.preventDefault(); void add(); }}>
    <fieldset class="section"><legend class="head">For</legend>
      <div class="kinds">
        <label class="kind"><input type="radio" name="host-kind" value={false} bind:group={exec} disabled={busy} /><span class="name">Boxes and CI runners</span>
          <span class="help">Dyna checks Podman, cgroups, FUSE and disk before the host is ready.</span></label>
        <label class="kind"><input type="radio" name="host-kind" value={true} bind:group={exec} disabled={busy} /><span class="name">Exec only</span>
          <span class="help">Nothing is placed here; only host exec uses it. Dyna checks SSH and bash.</span></label></div></fieldset>
    <label class="field">Name<input class="input mono" bind:value={name} placeholder="studio" disabled={busy} autocomplete="off" spellcheck="false" /></label>
    <label class="field">SSH<input class="input mono" bind:value={ssh} placeholder="user@192.168.1.20" disabled={busy} autocomplete="off" spellcheck="false" /></label>
    {#if !exec}<label class="check"><input type="checkbox" bind:checked={sole} disabled={busy} />Only I administer this machine</label>{/if}
    {#if problem}<p class="problem" role="alert">{problem}</p>{/if}
    <div class="actions"><button type="submit" class="btn fill" disabled={busy || !ready}>{busy ? "Checking the host…" : "Add"}</button></div>
  </form>
{/snippet}

{#snippet host(h: HostSummary)}{@const meters = hostMeters(h)}{@const seen = h.reachable ? undefined : lastSeen(h, now)}{@const groups = roomSessions(h, roomName)}
  {#if h.execOnly}<p class="small muted">Exec only: nothing is placed here, and only host exec uses it. Its check needs SSH and bash; turning Agents or CI on runs the full host check first.</p>{/if}
  {#if meters.length && (h.reachable || seen)}<div class="tiles" class:dimmed={!h.reachable}>{#each meters as meter (meter.label)}<MeterTile {meter} />{/each}</div>{/if}
  <dl class="facts">{#if h.execOnly || !h.reachable}<dt>State</dt><dd>{#if h.reachable}Online{:else}<StatusPill tone="error" label="Unreachable" />{" · "}{seen ?? "Not seen yet"}{/if}</dd>{/if}
    {#if h.health === "degraded"}<dt>Scheduling</dt><dd><StatusPill tone="warning" label="Degraded" /> · No new boxes or runners until a good probe</dd>{/if}
    {#if !h.execOnly}<dt>Workspaces</dt><dd>{h.boxes.toLocaleString("en")} running</dd>{/if}
    {#each h.gpus ?? [] as gpu (gpu.index)}<dt>GPU {gpu.index}</dt><dd>{gpuLine(gpu)}</dd>{/each}
    {#if h.runners}<dt>CI runners</dt><dd>{h.runners.toLocaleString("en")}</dd>{/if}
    {#if h.pool}<dt>Pool</dt><dd><a class="link" href={`/settings/pools/${encodeURIComponent(h.pool)}`} onclick={navigate}>{h.pool}</a></dd>{/if}</dl>
  {#if noSwap === h.name}<p class="warn">This host has no swap, so memory pressure can kill box processes.</p>{/if}
  {#if diskLow(h)}<p class="warn">Low on disk. New boxes and wakes wait here once free space reaches its floor.</p>{/if}
  {#each largeShared(h) as folder (folder.room)}<p class="small muted">Shared folder of {roomName(folder.room)}: {sharedSize(folder.bytes)}</p>{/each}
  {#if h.problem}<div class="section"><p class="warn">{problems[h.problem]}</p>
    <div class="actions"><button type="button" class="btn" disabled={busy} onclick={() => void change(() => call("host.set", { host: h.name, clearCreateProblem: true }))}>Clear</button></div></div>{/if}
  <div class="stack">
    <label class="set"><span class="name">Agents</span><input type="checkbox" role="switch" checked={!!h.agents} aria-label="Agents" onchange={event => void setSwitch(h, { agents: event.currentTarget.checked }, event.currentTarget)} />
      <span class="help">New session boxes may land here. Off: boxes already here stay until they stop.</span></label>
    <label class="set"><span class="name">CI</span><input type="checkbox" role="switch" checked={!!h.ci} aria-label="CI" onchange={event => void setSwitch(h, { ci: event.currentTarget.checked }, event.currentTarget)} />
      <span class="help">The CI pool may start runners here. Off: running jobs finish.</span></label></div>
  {#if !h.execOnly}<RoomsServed label="Rooms it serves" {rooms} chosen={h.rooms ?? []} {busy} set={ids => change(() => call("host.set_limits", { host: h.name, rooms: ids }))} />{/if}
  {#if groups.length}<div class="section"><strong class="head">Sessions</strong>
    <div class="sessions">{#each groups as group (group.room)}
      <p class="room"><Avatar kind="room" name={group.name} size={16} />{group.name}</p>
      {#each group.sessions as session (session.id)}<a class="session" class:quiet={session.boxState === "stopped"} href={`/r/${encodeURIComponent(group.room)}/s/${encodeURIComponent(session.id)}`} onclick={navigate}>
        <span class="sname">{session.name}</span>{#if session.boxState !== "ready"}<span class="word">{session.start ? startLabel(session.start, h.name, now) : boxWords[session.boxState]}</span>{/if}</a>{/each}{/each}</div></div>{/if}
  {#if problem}<p class="problem" role="alert">{problem}</p>{/if}
  <div class="danger-zone"><span class="small muted">{h.pool ? "Its pool's script takes it back. " : ""}Refused while {h.execOnly ? "a Run or a room's rule" : "a box, a CI runner, a Run or a room's rule"} uses it.</span>
    <button type="button" class="btn danger" aria-haspopup="dialog" disabled={busy} onclick={event => void remove(h, event.currentTarget)}>Remove</button></div>
{/snippet}

{#snippet detail()}
  <div class="detail" bind:this={anchor}>
    {#if creating}{@render form()}
    {:else if item}{#key item.name}{@render host(item)}{/key}
    {:else}<p class="muted">That host is gone, or the link is wrong.</p>{/if}
  </div>
{/snippet}

{#snippet summary()}
  {#if items}<p class="muted"><span class="fact">{plural(items.length, "host", "hosts")}</span> <span class="fact">· {items.filter(h => !h.reachable).length.toLocaleString("en")} unreachable</span>
    <span class="fact">· {items.filter(diskLow).length.toLocaleString("en")} low on disk</span></p>{/if}
{/snippet}

{#snippet action()}<button type="button" class="btn add" onclick={start}>{#if phone}<Icon name="plus" size={16} />Add{:else}Add a host{/if}</button>{/snippet}
{#snippet header()}{@render bar("Hosts", items?.length, action)}{/snippet}

<ListDetail bar={header} label="Hosts" title={creating ? "Add a host" : item ? hostName(item) : "Not found"} open={creating || (!!items && selected !== undefined)} onselect={select} onclose={close} {list} {detail} {summary} />

<style>
  .add { margin-left: auto; gap: 4px; }
  .icon { display: grid; flex: none; color: var(--muted); }
  /* A row's load: a short bar each for CPU, memory and disk, warm as Home's tiles are. */
  .bars { flex: none; display: flex; align-items: center; gap: 12px; }
  .meter { display: flex; align-items: center; gap: 5px; }
  .bl { color: var(--muted); font-size: 12px; }
  .usebar { display: block; width: 40px; height: 5px; border-radius: 3px; background: var(--track); overflow: hidden; }
  .usebar i { display: block; height: 100%; border-radius: inherit; background: var(--muted); }
  .usebar.busy i { background: var(--attention-fill); }
  .detail { display: grid; gap: 18px; min-width: 0; overflow-wrap: anywhere; }
  .head { padding: 0; font-size: 14px; font-weight: 600; }
  .problem { color: var(--danger); font-size: 13px; font-weight: 500; }
  .problem::first-letter { text-transform: uppercase; }
  .warn { color: var(--attention); font-size: 13px; font-weight: 500; }
  .mono { font-family: var(--mono); font-size: 13px; }
  /* Home's tiles, three to a row; a narrow detail makes them smaller, as a narrow Home card does. */
  .tiles { container-type: inline-size; display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 14px; }
  .dimmed { opacity: 0.4; }
  .form { display: grid; gap: 18px; min-width: 0; }
  fieldset { min-width: 0; margin: 0; padding: 0; border: 0; }
  legend { padding: 0 0 8px; }
  .kinds { display: grid; border: 1px solid var(--line); border-radius: 12px; background: var(--raised); }
  .kind { display: grid; grid-template-columns: auto minmax(0, 1fr); gap: 2px 10px; align-items: center; min-height: 44px; padding: 8px 12px; cursor: pointer; }
  .kind + .kind { border-top: 1px solid var(--line); }
  .kind input, .check input { width: 16px; height: 16px; margin: 0; accent-color: var(--focus); cursor: inherit; }
  .kind .name { font-size: 14px; font-weight: 600; }
  .kind .help { grid-column: 2; }
  .kind:has(input:disabled), .check:has(input:disabled) { cursor: default; }
  .field { display: grid; gap: 6px; min-width: 0; font-size: 14px; font-weight: 600; }
  .help { color: var(--muted); font-size: 12.5px; font-weight: 400; line-height: 1.35; }
  .input { box-sizing: border-box; width: 100%; min-height: 34px; padding: 0 10px; border: 1px solid var(--firm); border-radius: 6px; background: var(--bg); color: var(--text); font-weight: 400; }
  .input:focus { outline: none; border-color: var(--focus); }
  .check { display: flex; align-items: center; gap: 8px; min-height: 30px; font-size: 14px; cursor: pointer; }
  .sessions { display: grid; }
  .room { display: flex; align-items: center; gap: 6px; margin: 8px 0 2px; color: var(--muted); font-size: 12.5px; font-weight: 600; }
  .room:first-child { margin-top: 0; }
  .session { display: flex; align-items: center; gap: 10px; min-height: 32px; margin: 0 -8px; padding: 4px 8px; border-radius: 6px; color: var(--text); font-size: 13px; font-weight: 500; text-decoration: none; }
  .session:hover { background: var(--hover); }
  .session:focus-visible { outline: 2px solid var(--focus); outline-offset: -2px; }
  .quiet { color: var(--muted); font-weight: 400; }
  .link { color: inherit; }
  .sname { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .word { flex: none; color: var(--muted); font-size: 12.5px; }
  @media (hover: none) { .input { min-height: 40px; } .check { min-height: 40px; } .session { min-height: 44px; } }
</style>
