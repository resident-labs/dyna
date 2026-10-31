<script lang="ts">
  import { onMount, type Snippet } from "svelte";
  import { call } from "../client.ts";
  import { allowedOrigin } from "../connect.ts";
  import type { ConnectStatusResponse, Device } from "../contract.gen.ts";
  import DevicesPane from "./DevicesPane.svelte";
  import Icon from "./Icon.svelte";
  import StatusPill from "./StatusPill.svelte";
  let { devices, failed, reload, signedout, bar }: { devices: readonly Device[] | undefined; failed: boolean; reload: () => void; signedout: () => void; bar: Snippet<[string, number | undefined, Snippet | undefined]> } = $props();
  let status: ConnectStatusResponse | undefined = $state();
  let confirm = $state(false), busy = $state(false), problem = $state(false);
  let timer: ReturnType<typeof setTimeout>;
  let routes = $derived(status?.routes ?? []);
  let tunnel = $derived(status?.tunnel);
  let localRoutes = $derived(routes.filter(route => route !== tunnel));
  let current = $derived(routes.find(route => route === location.origin));
  function routeLabel(route: string) {
    const host = new URL(route).hostname;
    return host === "localhost" || host.endsWith(".localhost") || host === "127.0.0.1" || host === "[::1]" ? "Local address" : host.endsWith(".ts.net") ? "Tailscale HTTPS" : "Your HTTPS address";
  }
  function approvalURL(value: string | undefined): string | undefined {
    if (!value) return;
    try { const url = new URL(value); return !url.username && !url.password && allowedOrigin(url.origin) ? url.href : undefined; }
    catch { return; }
  }
  let stopped = false;
  async function refresh() {
    try {
      status = await call("connect.status", {});
      problem = false;
      if (!stopped && (status.attempt.state === "pending" || status.attempt.state === "starting" || status.attempt.state === "unlinking")) timer = setTimeout(() => void refresh(), 3000);
    } catch { problem = true; }
  }
  onMount(() => { void refresh(); return () => { stopped = true; clearTimeout(timer); }; });
  async function start() {
    busy = true;
    try { await call("connect.start", { relay: "", name: "" }); await refresh(); }
    catch { problem = true; }
    busy = false;
  }
  async function act(method: "connect.unlink" | "connect.cancel") {
    busy = true;
    try { await call(method, {}); confirm = false; await refresh(); }
    catch { problem = true; }
    busy = false;
  }
</script>
{#snippet sectionBar(title: string, count?: number, action?: Snippet)}
  <header class="section-head"><strong>{title}</strong>{#if count !== undefined}<span>{count}</span>{/if}{#if title === "Ways in"}<span>Browsers try them in order and check that each one reaches the Dyna host.</span>{/if}{@render action?.()}</header>
{/snippet}
{@render bar("Sharing & access", undefined, undefined)}
<div class="scroll" data-page-ready={(!!status || problem) && (!!devices || failed)}>
  <section class="ways">
    {@render sectionBar("Ways in")}
    <div class="ways-body">
    {#if current || status?.connectorMissing}<p class="summary">{#if current}This browser uses {current === tunnel ? "the Connect tunnel" : routeLabel(current) === "Local address" ? "the local address" : routeLabel(current).replace(/^./, letter => letter.toLowerCase())}.{/if}{#if status?.connectorMissing}{#if current}{" "}{/if}The Connect connector is not running.{/if}</p>{/if}
    <div class="route-card">
    {#each localRoutes as route, index (route)}<div class="row"><span class="route-icon"><Icon name={routeLabel(route) === "Local address" ? "laptop" : "globe"} size={20} /></span><span class="number">{index + 1}</span><div><strong>{routeLabel(route)}</strong><p><code>{route}</code></p>{#if route === location.origin}<p>This browser uses it</p>{:else if routeLabel(route) !== "Local address"}<p>Set in the Dyna host’s configuration</p>{/if}</div></div>{/each}
    <div class="row"><span class="route-icon"><Icon name="globe" size={20} /></span><span class="number">{status?.linked ? localRoutes.length + 1 : ""}</span><div class="detail"><strong>Resident Labs Connect</strong>
      {#if !status}<p>{problem ? "Connect status did not load." : "Loading Connect…"}</p>
      {:else if status.attempt.state === "starting" || status.attempt.state === "unlinking"}
        <p>{status.attempt.state === "starting" ? "Turning on Connect…" : "Turning off Connect…"}</p>
      {:else if status.linked}
        <p>Connect is on. {#if status.connectorMissing}<StatusPill tone="warning" label="Connector stopped" />{/if}</p>
        {#if tunnel === location.origin}<p>This browser uses it</p>{/if}
        <dl class="key-values">{#if tunnel}<dt>Address</dt><dd><code>{tunnel}</code></dd>{/if}{#if status.relay}<dt>Sign-in service</dt><dd><code>{status.relay}</code></dd>{/if}<dt>Machine key</dt><dd><code>{status.fingerprint}</code></dd></dl>
        {#if status.connectorMissing}<p>The Connect connector is not running.</p>{/if}
        {#if confirm}<p>Turn off Connect? Connect sign-ins end. Paired devices stay.</p><div class="actions"><button disabled={busy} onclick={() => void act("connect.unlink")}>Turn off</button><button disabled={busy} onclick={() => { confirm = false; }}>Cancel</button></div>
        {:else}<button class="unlink" onclick={() => { confirm = true; }}>Turn off Connect…</button>{/if}
      {:else if status.attempt.state === "pending"}
        <p>Waiting for your approval.</p><p>Code <code>{status.code}</code></p><p>Key fingerprint <code>{status.fingerprint}</code></p>
        <p>Check the code and fingerprint on the approval page, then approve with your passkey.</p>
        {#if approvalURL(status.url)}<a href={approvalURL(status.url)} target="_blank" rel="noopener noreferrer">Open approval page</a>{/if}
        <button disabled={busy} onclick={() => void act("connect.cancel")}>Cancel</button>
      {:else}
        {#if status.attempt.state === "failed"}<p role="alert">Connect could not finish. Try again.</p>{/if}
        <p>Open Dyna from anywhere, signed in with your passkey. Dyna adds a private tunnel.</p>
        <button disabled={busy} onclick={() => void start()}>Turn on Connect</button>
      {/if}
      {#if problem}<p role="alert">Connect could not finish. Try again.</p>{/if}
    </div></div>
    </div>
    </div>
  </section>
  <DevicesPane {devices} {failed} {reload} {signedout} bar={sectionBar} />
</div>
<style>
  .scroll { overflow-y: auto; }
  .section-head { display: flex; align-items: center; gap: 8px; min-height: 52px; padding: 0 16px; border-bottom: 1px solid var(--line); }
  .section-head span { color: var(--muted); font-size: 13px; }
  .ways-body { display: grid; gap: 12px; width: min(1040px, 100%); padding: 20px 24px; }
  p { margin: 0; color: var(--muted); font-size: 13px; }
  .summary { line-height: 1.5; }
  .route-card { overflow: hidden; border: 1px solid var(--line); border-radius: 12px; background: var(--raised); }
  .row { display: flex; align-items: start; gap: 12px; padding: 14px 16px; border-bottom: 1px solid var(--line); }
  .row:last-child { border-bottom: 0; }
  .route-icon { display: grid; place-items: center; flex: none; width: 40px; height: 40px; border-radius: 10px; background: var(--hover); color: var(--muted); }
  .number { flex: none; color: var(--muted); font-variant-numeric: tabular-nums; }
  .detail { display: grid; gap: 8px; min-width: 0; }
  .key-values { display: grid; grid-template-columns: max-content minmax(0, 1fr); gap: 6px 16px; margin: 0; font-size: 13px; }
  dt { color: var(--muted); } dd { margin: 0; min-width: 0; }
  button.unlink { justify-self: start; min-height: 32px; padding: 0; border: 0; color: var(--danger); }
  code { font: 13px var(--mono); overflow-wrap: anywhere; }
  button, a { min-height: 32px; padding: 4px 10px; border: 1px solid var(--firm); border-radius: 6px; background: transparent; color: var(--text); font: inherit; cursor: pointer; }
  a { display: inline-flex; align-items: center; text-decoration: none; }
  .actions { display: flex; gap: 8px; }
  @media (max-width: 480px) { .key-values { grid-template-columns: minmax(0, 1fr); gap: 4px; } .key-values dd + dt { margin-top: 4px; } }
  @media (hover: none) { button, a { min-height: 44px; } }
  @media (max-width: 640px) { .ways-body { padding: 12px 16px; } .section-head { flex-wrap: wrap; padding: 12px 16px; } .section-head span:not(:first-child) { flex-basis: auto; } }
</style>
