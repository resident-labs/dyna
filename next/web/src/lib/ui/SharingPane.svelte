<script lang="ts">
  import { onMount, tick, type Snippet } from "svelte";
  import { call, SignedOut } from "../client.ts";
  import { allowedOrigin } from "../connect.ts";
  import { ago, expiry, isPhone, scopes } from "../devices.ts";
  import { confirm } from "../modal.ts";
  import { connectPill, sharingChips, sharingGroup, sharingItems, sharingLine, sharingText, type ConnectItem, type DeviceItem, type SharingItem, type WayItem } from "../sharing.ts";
  import type { ConnectStatusResponse, Device, Scope } from "../contract.gen.ts";
  import Icon from "./Icon.svelte";
  import PairCard from "./PairCard.svelte";
  import StatusPill from "./StatusPill.svelte";
  import ItemList from "./settings/ItemList.svelte";
  import ListDetail from "./settings/ListDetail.svelte";
  // Settings > Sharing & access (spec 5.13): the ways into the engine, then the paired browsers, and the open item's
  // detail, the one place that explains it: a way's order and Connect, a device's access, host and revoke. Pair a device
  // opens the pair flow in the detail's place. Settings reads device.list, as its refusal hides the admin panes; this
  // page reads connect.status. Both lists are whole, so the list searches and filters here.
  let { bar, phone, devices, failed, reload, signedout, selected, onselect, onclose }: {
    bar: Snippet<[string, number | undefined, Snippet | undefined]>; phone: boolean; devices: readonly Device[] | undefined; failed: boolean;
    reload: () => Promise<void>; signedout: () => void; selected: string | undefined; onselect: (id: string, open: boolean) => void; onclose: () => void;
  } = $props();
  let status: ConnectStatusResponse | undefined = $state.raw(), statusFailed = $state(false);
  // Times on rows and in the detail read from when the lists last changed.
  let items = $derived(devices && (status || statusFailed) ? sharingItems(status, devices, location.origin, Date.now()) : undefined);
  let item = $derived(items?.find(each => each.key === selected));
  let now = $derived(items ? Date.now() : 0);
  let busy = $state(false);
  // A failed change on the open item, in the page's words.
  let problem = $state("");
  let pairing = $state(false), pair: PairCard | undefined = $state();
  // Inside the detail, to hand its panel the focus when what had it is gone.
  let anchor: HTMLElement | undefined = $state();
  const gone = (reason: unknown) => { if (reason instanceof SignedOut) signedout(); return reason instanceof SignedOut; };
  const panel = async () => { await tick(); anchor?.closest<HTMLElement>("aside")?.focus(); };

  // Connect's status reads again every 3 seconds while a step is under way.
  let timer: ReturnType<typeof setTimeout> | undefined, stopped = false;
  // A failed read during a step keeps the last status and tries again on the same beat.
  const moving = (s: ConnectStatusResponse | undefined) => s?.attempt.state === "pending" || s?.attempt.state === "starting" || s?.attempt.state === "unlinking";
  function refresh() {
    clearTimeout(timer);
    return call("connect.status", {}).then(result => { status = result; statusFailed = false; }, (reason: unknown) => { if (!gone(reason)) statusFailed = true; })
      .then(() => { if (!stopped && moving(status)) timer = setTimeout(() => void refresh(), 3000); });
  }
  // Pair a device is a step in history of its own, so Back leaves it as the back arrow does.
  function popped() { if (pairing && !history.state?.creating) pairing = false; }
  onMount(() => {
    void refresh();
    window.addEventListener("popstate", popped);
    return () => { stopped = true; clearTimeout(timer); window.removeEventListener("popstate", popped); };
  });
  // Another item, or none, starts without the last one's problem.
  $effect.pre(() => { void selected; problem = ""; });

  function start() {
    problem = "";
    if (!pairing) history.pushState({ creating: true, overList: selected === undefined }, "", location.href);
    pairing = true;
    void panel();
  }
  // Leaving the pair flow ends its code: the flow ends it, then calls back.
  function leave() {
    if (history.state?.creating) history.back();
    else pairing = false;
  }
  function close() {
    if (pairing) pair?.close();
    else onclose();
  }
  // A row picked while pairing takes the flow's history step, so none is left behind that Back would land on.
  function select(id: string, open: boolean) {
    if (pairing && history.state?.creating) history.replaceState({ fromList: !!history.state.overList }, "", location.href);
    pairing = false;
    onselect(id, open);
  }
  // The list already holds the new device, as the flow found it there.
  function paired(id: string) {
    pairing = false;
    if (history.state?.creating) history.replaceState({ fromList: !!history.state.overList }, "", location.href);
    onselect(id, false);
    void panel();
  }

  // Connect: each step reads the status again, which says where Connect stands.
  async function connect(run: () => Promise<unknown>) {
    busy = true; problem = "";
    await run().then(refresh, (reason: unknown) => { if (!gone(reason)) problem = "Connect could not finish. Try again."; });
    busy = false;
  }
  // The shared confirm runs the unlink and keeps a refusal in its own words.
  async function unlink(opener: HTMLElement) {
    problem = "";
    if (await confirm({ title: "Turn off Connect?", message: "Connect sign-ins end. Paired devices stay.", action: "Turn off", danger: true, opener,
      run: () => call("connect.unlink", {}), failed: "Connect did not turn off. Try again." })) await refresh();
  }
  function approvalURL(value: string | undefined) {
    if (!value || !URL.canParse(value)) return undefined;
    const url = new URL(value);
    return !url.username && !url.password && allowedOrigin(url.origin) ? url.href : undefined;
  }

  // Each box saves at once. The last checked one cannot be unchecked; Admin off on this browser asks first, as this
  // browser then loses the page. A refusal puts the box back.
  async function setScope(device: Device, scope: Scope, box: HTMLInputElement) {
    // The boxes stay enabled, so the keys keep their place; a change while one saves is put back.
    if (busy) { box.checked = !box.checked; return; }
    const next = scopes.map(each => each.scope).filter(each => each === scope ? box.checked : !!device.scopes?.includes(each));
    const run = () => call("device.set_scopes", { id: device.id, scopes: next }).catch((reason: unknown) => { gone(reason); throw reason; });
    problem = "";
    if (device.current && scope === "admin" && !box.checked) {
      if (await confirm({ title: "Remove Admin from this browser?", message: "This browser can no longer change settings or pair devices.", action: "Remove Admin", danger: true, opener: box,
        run, failed: "The access did not change. Try again." })) await reload();
      else box.checked = true;
      return;
    }
    busy = true;
    await run().then(reload, () => { problem = "The access did not change. Try again."; box.checked = !box.checked; });
    busy = false;
  }

  // Host: Enter and leaving the field both save, so the blur after an Enter finds nothing new. A refused host keeps
  // the owner's text in the field; Esc first puts back the saved one.
  let saved = $derived(item?.kind === "device" ? item.device.host ?? "" : undefined);
  let hostDraft = $state(""), hostSaving = $state(false);
  $effect.pre(() => { hostDraft = saved ?? ""; });
  async function saveHost(device: Device) {
    const host = hostDraft.trim();
    if (hostSaving) return;
    if (host === (device.host ?? "")) { hostDraft = host; return; }
    hostSaving = true; problem = "";
    await call("device.set_host", { id: device.id, host }).then(reload, (reason: unknown) => { if (!gone(reason)) problem = "The host did not change. Try again."; });
    hostSaving = false;
  }
  function escape(event: KeyboardEvent) {
    if (event.key !== "Escape" || event.isComposing || event.defaultPrevented) return;
    if ((event.target as Element).closest("input.host") && saved !== undefined && hostDraft !== saved) { event.preventDefault(); hostDraft = saved; problem = ""; }
  }

  async function revoke(device: Device, opener: HTMLElement) {
    problem = "";
    const ok = await confirm({ title: `Revoke ${device.name}?`, message: device.current ? "You will be signed out, and this browser must pair again." : "It must pair again to open Dyna.",
      action: "Revoke", danger: true, opener, run: () => call("device.revoke", { id: device.id }).catch((reason: unknown) => { gone(reason); throw reason; }), failed: "The device was not revoked. Try again." });
    if (!ok) return;
    if (device.current) { signedout(); return; }
    onclose();
    await reload();
  }
  const plural = (n: number, one: string, many: string) => `${n.toLocaleString("en")} ${n === 1 ? one : many}`;
</script>

{#snippet row(each: SharingItem)}
  {#if each.kind === "device"}{@const d = each.device}
    <Icon name={isPhone(d.name) ? "phone" : "laptop"} size={20} />
    <span class="lines"><span class="title"><span class="name" title={d.name}>{d.name}</span>{#if d.current}<span class="badge">This device</span>{/if}{#if each.warning}<StatusPill tone="warning" label={each.warning} />{/if}</span>
      <span class="line">{sharingLine(each, now)}</span></span>
  {:else}{@const pill = each.kind === "connect" ? connectPill(each.status) : undefined}
    <Icon name={each.kind === "way" && each.local ? "laptop" : "globe"} size={20} />
    <span class="lines"><span class="title"><span class="name">{each.name}</span>{#if each.current}<span class="badge">This browser</span>{/if}
      {#if pill?.tone}<StatusPill tone={pill.tone} label={pill.label} />{:else if pill}<span class="badge">{pill.label}</span>{/if}</span>
      <span class="line" class:mono={each.kind === "way" || !!each.status?.linked}>{sharingLine(each, now)}</span></span>
  {/if}
{/snippet}

{#snippet list({ drill, select: pick }: { drill: boolean; select: (key: string, open: boolean) => void })}
  <ItemList {items} {failed} retry={() => void reload()} key={each => each.key} noun={["item", "items"]} label="Sharing & access" text={sharingText}
    chips={sharingChips} group={sharingGroup} {row} selected={pairing ? undefined : selected} {drill} onselect={pick} />
{/snippet}

<!-- What every way shares: its place in the order, its address, who sets it, and whether this browser came through it. -->
{#snippet wayFacts(each: WayItem | ConnectItem, address: string | undefined, setBy: string)}
  <p class="small muted">Browsers try the ways in from the top and use the first that reaches the Dyna host.</p>
  <dl class="facts">{#if address}<dt>Address</dt><dd class="mono">{address}</dd>{/if}<dt>Set by</dt><dd>{setBy}</dd>{#if each.current}<dt>Used by</dt><dd>This browser</dd>{/if}</dl>
{/snippet}

{#snippet connectDetail(each: ConnectItem)}{@const s = each.status}
  {@render wayFacts(each, s?.tunnel, "Connect")}
  <div class="section"><strong class="head">Connect</strong>
    <p class="small muted lead">Opens Dyna from anywhere, signed in with your passkey, through a private tunnel.</p>
    {#if !s}<p class="problem" role="alert">Connect status did not load. <button type="button" class="retry" onclick={() => void refresh()}>Try again</button></p>
    {:else if s.attempt.state === "starting" || s.attempt.state === "unlinking"}<p>{s.attempt.state === "starting" ? "Turning on Connect…" : "Turning off Connect…"}</p>
    {:else if s.linked}
      <dl class="facts">{#if s.relay}<dt>Sign-in service</dt><dd class="mono">{s.relay}</dd>{/if}<dt>Machine key</dt><dd class="mono">{s.fingerprint}</dd></dl>
      {#if s.connectorMissing}<p class="warn">The Connect connector is not running.</p>{/if}
    {:else if s.attempt.state === "pending"}{@const link = approvalURL(s.url)}
      <p>Waiting for your approval. Check the code and fingerprint on the approval page, then approve with your passkey.</p>
      <dl class="facts"><dt>Code</dt><dd class="mono">{s.code}</dd><dt>Key fingerprint</dt><dd class="mono">{s.fingerprint}</dd></dl>
      <div class="actions">{#if link}<a class="btn" href={link} target="_blank" rel="noopener noreferrer">Open approval page<Icon name="external" size={14} /></a>{/if}
        <button type="button" class="btn" disabled={busy} onclick={() => void connect(() => call("connect.cancel", {}))}>Cancel</button></div>
    {:else}
      {#if s.attempt.state === "failed" && !problem}<p class="problem" role="alert">Connect could not finish. Try again.</p>{/if}
      <div class="actions"><button type="button" class="btn" disabled={busy} onclick={() => void connect(() => call("connect.start", { relay: "", name: "" }))}>Turn on Connect</button></div>
    {/if}
    {#if problem}<p class="problem" role="alert">{problem}</p>{/if}
  </div>
  {#if s?.linked && s.attempt.state !== "unlinking"}<div class="danger-zone"><span class="small muted">Connect sign-ins end. Paired devices stay.</span>
    <button type="button" class="btn danger" aria-haspopup="dialog" disabled={busy} onclick={event => void unlink(event.currentTarget)}>Turn off Connect…</button></div>{/if}
{/snippet}

{#snippet deviceDetail(each: DeviceItem)}{@const d = each.device}
  <dl class="facts"><dt>Paired</dt><dd>{ago(d.pairedAt, now)}</dd><dt>Last seen</dt><dd>{d.current ? "Active now" : ago(d.lastSeenAt, now)}</dd>
    <dt>Expires</dt><dd>{expiry(d.expiresAt, now)}, unless used</dd></dl>
  <fieldset class="section"><legend class="head">Access</legend>
    <div class="scopes">{#each scopes as { scope, label, about } (scope)}{@const on = !!d.scopes?.includes(scope)}
      <label class="scope"><input type="checkbox" checked={on} disabled={on && d.scopes?.length === 1} onchange={event => void setScope(d, scope, event.currentTarget)} />
        <span class="name">{label}</span><span class="about">{about}</span></label>{/each}</div></fieldset>
  <label class="field">Host<input class="input host mono" placeholder="none" autocomplete="off" spellcheck="false" bind:value={hostDraft} readonly={hostSaving}
    onblur={() => void saveHost(d)} onkeydown={event => { if (event.key === "Enter" && !event.isComposing) { event.preventDefault(); void saveHost(d); } }} />
    <span class="help">Your posts from this device read as “the owner, on &lt;host&gt;”. Empty clears it.</span></label>
  {#if problem}<p class="problem" role="alert">{problem}</p>{/if}
  <div class="danger-zone"><span class="small muted">{d.current ? "You will be signed out here." : "It must pair again to open Dyna."}</span>
    <button type="button" class="btn danger" aria-haspopup="dialog" onclick={event => void revoke(d, event.currentTarget)}>Revoke</button></div>
{/snippet}

{#snippet detail()}
  <div class="detail" bind:this={anchor}>
    {#if pairing && devices}<PairCard bind:this={pair} {devices} reload={() => void reload()} onclose={leave} onpaired={paired} />
    {:else if item?.kind === "device"}{#key item.key}{@render deviceDetail(item)}{/key}
    {:else if item?.kind === "connect"}{@render connectDetail(item)}
    {:else if item}{@render wayFacts(item, item.address, item.local ? "The Dyna host itself" : "The Dyna host’s configuration")}
    {:else}<p class="muted">That item is gone, or the link is wrong.</p>{/if}
  </div>
{/snippet}

{#snippet summary()}
  {#if items && devices}<p class="muted"><span class="fact">{plural(items.length - devices.length, "way in", "ways in")}</span> <span class="fact">· {plural(devices.length, "device", "devices")}</span></p>{/if}
{/snippet}

{#snippet action()}<button type="button" class="btn add" disabled={!devices} onclick={start}>{#if phone}<Icon name="plus" size={16} />Pair{:else}Pair a device{/if}</button>{/snippet}
{#snippet header()}{@render bar("Sharing & access", items?.length, action)}{/snippet}

<!-- svelte-ignore a11y_no_static_element_interactions: Esc is caught on its way down, before ListDetail closes the detail with it. -->
<div class="keys" onkeydowncapture={escape}>
<ListDetail bar={header} empty={items?.length === 0} label="Sharing & access" title={pairing ? "Pair a device" : item?.kind === "device" ? item.device.name : item?.name ?? "Not found"}
  open={pairing || (!!items && selected !== undefined)} onselect={select} onclose={close} {list} {detail} {summary} />
</div>

<style>
  .keys { display: contents; }
  .add { margin-left: auto; gap: 4px; }
  .mono { font-family: var(--mono); font-size: 12.5px; }
  .detail { display: grid; gap: 18px; min-width: 0; overflow-wrap: anywhere; }
  /* Padding, as the detail sets every paragraph's margin. */
  .lead { padding-bottom: 6px; }
  .head { padding: 0; font-size: 14px; font-weight: 600; }
  fieldset { min-width: 0; margin: 0; padding: 0; border: 0; }
  .detail p { font-size: 13px; }
  .problem { color: var(--danger); font-weight: 500; }
  .warn { color: var(--attention); font-weight: 500; }
  .facts .mono { font-size: 12.5px; }
  /* The scopes in one card, a checkbox, the name and its meaning to a row, as the mock draws them. */
  .scopes { display: grid; border: 1px solid var(--line); border-radius: 12px; background: var(--raised); }
  .scope { display: grid; grid-template-columns: auto minmax(0, 1fr); gap: 2px 10px; align-items: center; min-height: 44px; padding: 8px 12px; cursor: pointer; }
  .scope + .scope { border-top: 1px solid var(--line); }
  .scope:has(input:disabled) { cursor: default; }
  .scope input { width: 16px; height: 16px; margin: 0; accent-color: var(--focus); cursor: inherit; }
  .scope .name { font-size: 14px; font-weight: 600; }
  .scope .about { grid-column: 2; color: var(--muted); font-size: 12.5px; line-height: 1.35; }
  .field { display: grid; gap: 6px; min-width: 0; font-size: 14px; font-weight: 600; }
  .help { color: var(--muted); font-size: 12.5px; font-weight: 400; line-height: 1.35; }
  .input { box-sizing: border-box; width: 100%; min-height: 34px; padding: 0 10px; border: 1px solid var(--firm); border-radius: 6px; background: var(--bg); color: var(--text); font-weight: 400; }
  .input:focus { outline: none; border-color: var(--focus); }
  a.btn { gap: 6px; text-decoration: none; }
  @media (hover: none) { .input { min-height: 40px; } }
</style>
