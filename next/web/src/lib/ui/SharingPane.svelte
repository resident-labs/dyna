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
  function routeLabel(route: string) {
    const host = new URL(route).hostname;
    return host === "localhost" || host.endsWith(".localhost") || host === "127.0.0.1" || host === "[::1]" ? "This computer" : host.endsWith(".ts.net") ? "Tailscale HTTPS" : "Your HTTPS address";
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
{@render bar("Sharing & access", undefined, undefined)}
<div class="scroll">
  <section class="ways">
    <h2>Ways in</h2><p>Browsers try them in order and check that each one is this machine.</p>
    <div class="route-card">
    {#each localRoutes as route, index (route)}<div class="row"><span class="route-icon"><Icon name={routeLabel(route) === "This computer" ? "laptop" : "globe"} size={20} /></span><span class="number">{index + 1}</span><div><strong>{routeLabel(route)}</strong><p><code>{route}</code>{#if route === location.origin} · In use by this browser{/if}</p></div></div>{/each}
    <div class="row"><span class="route-icon"><Icon name="globe" size={20} /></span><span class="number">{status?.linked ? localRoutes.length + 1 : ""}</span><div class="detail"><strong>Resident Labs Connect</strong>
      {#if !status}<p>{problem ? "Connect status did not load." : "Loading Connect…"}</p>
      {:else if status.attempt.state === "starting" || status.attempt.state === "unlinking"}
        <p>{status.attempt.state === "starting" ? "Starting the link…" : "Unlinking this machine…"}</p>
      {:else if status.linked}
        {#if status.connectorMissing}<StatusPill tone="warning" label="Connector stopped" />{/if}
        <p>Linked to this machine.</p>{#if tunnel}<p><code>{tunnel}</code>{#if tunnel === location.origin} · In use by this browser{/if}</p>{/if}<p>Machine key <code>{status.fingerprint}</code></p>
        {#if confirm}<p>Unlink this machine? Connect sign-ins end. Paired devices stay.</p><div class="actions"><button disabled={busy} onclick={() => void act("connect.unlink")}>Unlink</button><button disabled={busy} onclick={() => { confirm = false; }}>Cancel</button></div>
        {:else}<button onclick={() => { confirm = true; }}>Unlink this machine…</button>{/if}
      {:else if status.attempt.state === "pending"}
        <p>Waiting for your approval.</p><p>Code <code>{status.code}</code></p><p>Key fingerprint <code>{status.fingerprint}</code></p>
        <p>Check the code and fingerprint on the approval page, then approve with your passkey.</p>
        {#if approvalURL(status.url)}<a href={approvalURL(status.url)} target="_blank" rel="noopener noreferrer">Open approval page</a>{/if}
        <button disabled={busy} onclick={() => void act("connect.cancel")}>Cancel</button>
      {:else}
        {#if status.attempt.state === "failed"}<p role="alert">Connect could not finish. Try again.</p>{/if}
        <p>Open Dyna from anywhere, signed in with your passkey. Dyna adds a private tunnel.</p>
        <button disabled={busy} onclick={() => void start()}>Link this machine</button>
      {/if}
      {#if problem}<p role="alert">Connect could not finish. Try again.</p>{/if}
    </div></div>
    </div>
  </section>
  <DevicesPane {devices} {failed} {reload} {signedout} {bar} />
</div>
<style>
  .scroll { overflow-y: auto; }
  .ways { display: grid; gap: 8px; max-width: 1040px; padding: 20px 24px; }
  h2 { margin: 0; font-size: 18px; } p { margin: 0; color: var(--muted); font-size: 13px; }
  .route-card { overflow: hidden; border: 1px solid var(--line); border-radius: 12px; background: var(--raised); }
  .row { display: flex; align-items: start; gap: 12px; padding: 14px 16px; border-bottom: 1px solid var(--line); }
  .row:last-child { border-bottom: 0; }
  .route-icon { display: grid; place-items: center; flex: none; width: 40px; height: 40px; border-radius: 10px; background: var(--hover); color: var(--muted); }
  .number { flex: none; color: var(--muted); font-variant-numeric: tabular-nums; }
  .detail { display: grid; gap: 8px; }
  code { font: 13px var(--mono); overflow-wrap: anywhere; }
  button, a { min-height: 32px; padding: 4px 10px; border: 1px solid var(--firm); border-radius: 6px; background: transparent; color: var(--text); font: inherit; cursor: pointer; }
  a { display: inline-flex; align-items: center; text-decoration: none; }
  .actions { display: flex; gap: 8px; }
  @media (hover: none) { button, a { min-height: 44px; } }
</style>
