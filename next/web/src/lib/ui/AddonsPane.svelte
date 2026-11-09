<script lang="ts">
  import { onMount, tick, type Snippet } from "svelte";
  import { APIError, call, SignedOut } from "../client.ts";
  import { addonAdded, addonChips, addonDetail, addonKey, addonKind, addonKinds, addonOrder, addonScope, addonSummary, addonWhere, draftReady, draftScope, emptyDraft,
    mcpRequest, packageRequest, runOnChoices, runOnHosts, skillRequest, type AddonDraft } from "../addons.ts";
  import { hostLabel } from "../hosts.ts";
  import { confirm } from "../modal.ts";
  import Icon from "./Icon.svelte";
  import ItemList from "./settings/ItemList.svelte";
  import ListDetail from "./settings/ListDetail.svelte";
  import type { Addon, AddonKind, RoomSummary } from "../contract.gen.ts";
  // Settings > Skills & tools (spec 5.13): every skill, Pi package and MCP server, grouped by where it applies, and the
  // open item's detail, the one place that explains its switches. Add opens an empty form in the detail's place.
  // addon.list is whole, so the list searches and filters here. A device without admin reads this page and changes
  // nothing, since the engine would refuse it.
  let { bar, admin, signedout, selected, onselect, onclose }: {
    bar: Snippet<[string, number | undefined, Snippet | undefined]>; admin: boolean; signedout: () => void;
    selected: string | undefined; onselect: (key: string, open: boolean) => void; onclose: () => void;
  } = $props();
  let items: Addon[] | undefined = $state.raw(), rooms: RoomSummary[] = $state.raw([]), hosts: string[] = $state.raw([]);
  let failed = $state(false), busy = $state(false), creating = $state(false);
  let draft: AddonDraft = $state(emptyDraft());
  // One failed change, in the engine's words, in the detail it belongs to.
  let problem = $state("");
  // Where the focus goes when Add opens the form, and once the added item opens: the first control, not a text field.
  let kinds: HTMLFieldSetElement | undefined = $state(), switches: HTMLDivElement | undefined = $state();
  let ordered = $derived(items && addonOrder(items, rooms));
  let item = $derived(items?.find(each => addonKey(each) === selected));
  let where = $derived(draftScope(draft.where, rooms));
  let counts = $derived(addonSummary(items ?? []));
  const words = (error: unknown) => error instanceof APIError ? error.detail.message : "That did not work. Try again.";
  // Whether a call failed for a sign-out, which the app takes over; every other failure is the page's to show.
  function gone(error: unknown) { if (error instanceof SignedOut) signedout(); return error instanceof SignedOut; }
  const fail = (error: unknown) => { if (!gone(error)) problem = words(error); };

  // Resolves once the list has loaded or failed. The hosts serve only a skill's run-on switches, so without them the page still works.
  function reload() {
    if (admin) void call("host.list", {}).then(list => { hosts = (list.hosts ?? []).map(host => host.name); }, () => {});
    return Promise.all([call("addon.list", {}), call("room.list", {})]).then(([list, known]) => { items = list.items ?? []; rooms = known.rooms ?? []; failed = false; },
      (error: unknown) => { if (!gone(error) && !items) failed = true; });
  }
  function refresh() { void reload(); }
  // Add is a step in history of its own, so Back leaves the empty form as the back arrow does.
  function popped() { if (creating && !history.state?.creating) { creating = false; problem = ""; } }
  onMount(() => {
    refresh();
    window.addEventListener("focus", refresh);
    window.addEventListener("popstate", popped);
    return () => { window.removeEventListener("focus", refresh); window.removeEventListener("popstate", popped); };
  });
  // The added item takes the focus once the path has opened it, which lands after onselect returns, and the add has
  // settled, as its switches are disabled until then.
  let added: string | undefined = $state();
  $effect(() => { if (added !== undefined && selected === added && switches && !busy) { switches.querySelector("input")?.focus(); added = undefined; } });
  // Another item, or none, starts without the last one's problem.
  $effect.pre(() => { void selected; problem = ""; });

  // Whether the engine took the change.
  async function change(run: () => Promise<unknown>) {
    busy = true; problem = "";
    const done = await run().then(async () => { await reload(); return true; }, (error: unknown) => { fail(error); return false; });
    busy = false;
    return done;
  }
  async function start() {
    problem = "";
    // An open item stays in the path under the form, so Back returns to it.
    if (!creating) { draft = emptyDraft(); history.pushState({ creating: true, overList: selected === undefined }, "", location.href); }
    creating = true;
    await tick();
    kinds?.querySelector("input")?.focus();
  }
  function close() {
    if (!creating) { onclose(); return; }
    if (history.state?.creating) history.back();
    else { creating = false; problem = ""; }
  }
  // An item picked while the form is up takes the form's history step, so none is left behind that Back would land on.
  function select(key: string, open: boolean) {
    if (creating && history.state?.creating) history.replaceState({ fromList: !!history.state.overList }, "", location.href);
    creating = false;
    onselect(key, open);
  }
  async function add() {
    if (busy || !where || !draftReady(draft, where)) return;
    const form = draft, scope = where;
    busy = true; problem = "";
    await (form.kind === "skill" ? call("skill.save", skillRequest(form, scope)) : form.kind === "package" ? call("package.add", packageRequest(form, scope)) : call("mcp.add", mcpRequest(form, scope)))
      .then(async ({ item: saved }) => {
        await reload();
        creating = false;
        // The item takes the history step Add made, so Back returns to the list when the form opened over it, and closing
        // it with an item under the form goes to the list in place, as Settings does for an item opened without the list.
        if (history.state?.creating) history.replaceState({ fromList: !!history.state.overList }, "", location.href);
        if (!saved) return;
        // The new item opens once it is listed; a reload that failed lists it from what the engine returned.
        if (!items?.some(each => addonKey(each) === addonKey(saved))) items = [...items ?? [], saved];
        added = addonKey(saved);
        onselect(added, false);
      }, fail);
    busy = false;
  }
  // The shared confirm runs the removal and keeps a refusal in the engine's words.
  async function remove(item: Addon) {
    const request = { ...item.room ? { room: item.room } : { global: true }, name: item.name };
    problem = "";
    const ok = await confirm({ title: `Remove ${item.name}?`, action: "Remove", danger: true, failed: words,
      run: () => (item.kind === "skill" ? call("skill.remove", request) : item.kind === "package" ? call("package.remove", request) : call("mcp.remove", request))
        .catch((error: unknown) => { gone(error); throw error; }) });
    if (!ok) return;
    onclose();
    await reload();
  }
  const sourceLabel: Record<AddonKind, string> = { skill: "Link", package: "Source", mcp: "URL or command" };
  const sourceHint: Record<AddonKind, string> = {
    skill: "A link to a SKILL.md or a GitHub folder. To save a folder from a box, use dyna skill save.",
    package: "npm:name, git:host/owner/repo or a repository URL. Dyna pins it to one version now.",
    mcp: "A server URL, or one executable with its arguments. No shell reads it.",
  };
  const fact = (count: number, one: string, many: string) => `${count.toLocaleString("en")} ${count === 1 ? one : many}`;
</script>

{#snippet toggle(label: string, checked: boolean, run: (on: boolean) => Promise<unknown>)}
  <input type="checkbox" role="switch" {checked} disabled={busy} aria-label={label}
    onchange={event => { const box = event.currentTarget; void change(() => run(box.checked)).then(done => { if (!done) box.checked = !box.checked; }); }} />
{/snippet}

{#snippet row(item: Addon, _compact: boolean)}{@const kind = addonKind(item.kind)}
  <span class="icon"><Icon name={kind.icon} size={20} /></span>
  <span class="lines"><span class="title"><span class="name" title={item.name}>{item.name}</span><span class="badge">{kind.label}</span>{#if item.rootOnly}<span class="badge">Root only</span>{/if}{#if item.disabled}<span class="badge">Off</span>{/if}</span>
    <span class="line">{addonDetail(item)}</span></span>
{/snippet}

{#snippet empty()}{#if admin}<button type="button" class="btn" onclick={() => void start()}>Add</button>{/if}{/snippet}

{#snippet list({ drill, select: pick }: { drill: boolean; select: (key: string, open: boolean) => void })}
  <ItemList items={ordered} {failed} retry={() => { failed = false; refresh(); }} key={addonKey} text={each => `${each.name} ${addonDetail(each)} ${addonWhere(each, rooms)}`}
    noun={["item", "items"]} label="Skills and tools" chips={addonChips} group={each => ({ id: addonScope(each), title: addonWhere(each, rooms) })} folded={["built-in"]}
    {row} {empty} selected={creating ? undefined : selected} {drill} onselect={pick} />
{/snippet}

{#snippet form()}
  <form class="form" aria-label="Add a skill or tool" onsubmit={event => { event.preventDefault(); void add(); }}>
    <fieldset class="kinds" bind:this={kinds}><legend>Kind</legend>
      {#each addonKinds as { kind, label } (kind)}<label class:on={draft.kind === kind}><input type="radio" name="addon-kind" value={kind} bind:group={draft.kind} disabled={busy} />{label}</label>{/each}
    </fieldset>
    <label class="field">Where<input list="addon-rooms" bind:value={draft.where} disabled={busy} autocomplete="off" spellcheck="false"
      placeholder={`All rooms, or search ${fact(rooms.length, "room", "rooms")}`} />
      <datalist id="addon-rooms"><option value="All rooms"></option>{#each rooms as room (room.id)}<option value={room.name}></option>{/each}</datalist>
      <span class="help">{draft.where.trim() && !where ? `No room named ${draft.where.trim()}.` : "A room unless every room needs it."}</span></label>
    {#if draft.kind === "mcp"}<label class="field">Name<input class="mono" bind:value={draft.name} disabled={busy} autocomplete="off" spellcheck="false" /></label>{/if}
    <label class="field">{sourceLabel[draft.kind]}<input class="mono" bind:value={draft.source} disabled={busy} autocomplete="off" spellcheck="false" />
      <span class="help">{sourceHint[draft.kind]}</span></label>
    {#if problem}<p class="problem" role="alert">{problem}</p>{/if}
    <div class="actions"><button type="submit" class="btn fill" disabled={busy || !draftReady(draft, where)}>{busy ? "Adding…" : "Add"}</button></div>
  </form>
{/snippet}

{#snippet detail()}
  {#if creating}{@render form()}
  {:else if item}{@const kind = addonKind(item.kind)}{@const source = item.kind === "mcp" ? addonDetail(item) : item.source}
    {@const choices = admin && !item.builtIn && item.kind === "skill" ? runOnChoices(hosts, item.hosts) : []}
    <dl class="facts"><dt>Kind</dt><dd>{kind.label}</dd><dt>Where</dt><dd>{item.builtIn ? "Every box" : addonWhere(item, rooms)}</dd>
      {#if item.description}<dt>What</dt><dd>{item.description}</dd>{/if}
      {#if source}<dt>{item.kind === "mcp" ? "URL or command" : "Source"}</dt><dd class="mono">{source}</dd>{/if}
      {#if !item.builtIn}<dt>Added</dt><dd>{addonAdded(item, Date.now())}</dd>{/if}
      {#if item.hosts?.length && !choices.length}<dt>Runs on</dt><dd>{item.hosts.map(hostLabel).join(", ")}</dd>{/if}</dl>
    {#if item.builtIn}<p class="small muted">Ships with Dyna. Every box has it, and it cannot be turned off.</p>
    {:else}
      <p class="small muted">Changes reach a session at its next box start.{#if !item.room}{" "}A room's own item with the same name wins.{/if}</p>
      {#if admin}{@const open = item}
        <div class="stack" bind:this={switches}>
          <label class="set"><span class="name">On</span>{@render toggle(`${item.name} on`, !item.disabled, on => call("addon.set_enabled", { kind: open.kind, name: open.name, room: open.room, enabled: on }))}<span class="help">Off keeps it saved but out of new boxes.</span></label>
          <label class="set"><span class="name">Root only</span>{@render toggle(`${item.name} root only`, !!item.rootOnly, on => call("addon.set_root_only", { kind: open.kind, name: open.name, room: open.room, rootOnly: on }))}<span class="help">Only the room's root session gets it.</span></label>
        </div>
        {#if choices.length}{@const current = item.hosts ?? []}
          <div class="section"><strong class="head">Runs on</strong><p class="small muted">Its commands run on these hosts. The tool it drives must already be installed there. With none on, they run in the box.</p>
            <div class="stack">{#each choices as host (host)}
              <label class="set"><span class="name">{hostLabel(host)}</span>{@render toggle(`Runs on ${hostLabel(host)}`, current.includes(host),
                on => call("addon.set_hosts", { name: open.name, room: open.room, hosts: runOnHosts(open.hosts, host, on) }))}</label>{/each}</div></div>
        {/if}
        {#if problem}<p class="problem" role="alert">{problem}</p>{/if}
        <div class="danger-zone"><span class="small muted">Sessions in {item.room ? addonWhere(item, rooms) : "every room"} stop getting it.</span>
          <button type="button" class="btn danger" aria-haspopup="dialog" disabled={busy} onclick={() => void remove(open)}>Remove</button></div>
      {:else}<p class="small muted">This device can read this page. Changing it needs Admin access.</p>{/if}
    {/if}
  {:else}<p class="muted">That item is gone, or the link is wrong.</p>
  {/if}
{/snippet}

{#snippet summary()}
  {#if items}<p class="muted"><span class="fact">{counts.builtIn.toLocaleString("en")} built in</span> <span class="fact">· {counts.global.toLocaleString("en")} for all rooms</span>
    <span class="fact">· {counts.inRooms.toLocaleString("en")} in {fact(counts.rooms, "room", "rooms")}</span></p><p class="small muted">Rooms with nothing added have no group.</p>{/if}
{/snippet}

{#snippet action()}{#if admin}<button type="button" class="btn add" onclick={() => void start()}>Add</button>{/if}{/snippet}
{#snippet header()}{@render bar("Skills & tools", items?.length, action)}{/snippet}

<ListDetail bar={header} empty={ordered?.length === 0} label="Skills & tools" title={creating ? "Add" : item?.name ?? "Not found"} open={creating || (!!items && selected !== undefined)} onselect={select} onclose={close} {list} {detail} {summary} />

<style>
  .add { margin-left: auto; }
  .icon { display: grid; flex: none; color: var(--muted); }
  .head { font-size: 14px; }
  .problem { margin: 0; color: var(--danger); font-size: 13px; font-weight: 500; overflow-wrap: anywhere; }
  .mono { font-family: var(--mono); font-size: 13px; }
  .form { display: grid; gap: 18px; min-width: 0; }
  .kinds { display: flex; flex-wrap: wrap; align-items: center; gap: 4px; min-width: 0; margin: 0; padding: 0; border: 0; }
  legend { padding: 0 0 6px; font-size: 14px; font-weight: 600; }
  .kinds label { display: inline-flex; align-items: center; height: 30px; padding: 0 10px; border: 1px solid var(--firm); border-radius: 6px; color: var(--muted); font-size: 13px; font-weight: 500; cursor: pointer; }
  .kinds label:hover { color: var(--text); }
  .kinds label.on { border-color: var(--focus); color: var(--text); }
  .kinds label:has(:focus-visible) { outline: 2px solid var(--focus); outline-offset: 2px; }
  .kinds input { position: absolute; opacity: 0; pointer-events: none; }
  .field { display: grid; gap: 6px; min-width: 0; font-size: 14px; font-weight: 600; }
  .help { color: var(--muted); font-size: 12.5px; font-weight: 400; line-height: 1.35; }
  .field input { box-sizing: border-box; width: 100%; height: 34px; padding: 0 10px; border: 1px solid var(--firm); border-radius: 6px; background: var(--bg); color: var(--text); font: inherit; font-size: 14px; font-weight: 400; }
  .field input.mono { font-family: var(--mono); font-size: 13px; }
  .field input:focus { outline: none; border-color: var(--focus); }
  @media (hover: none) { .kinds label { height: 40px; } .field input { height: 40px; } }
</style>
