<script lang="ts">
  import { onMount, tick, untrack } from "svelte";
  import { allowedOrigin, hello, getOriginPin, getPin, putPin, fingerprint, type Hello, type Pin } from "../connect.ts";
  import { beginConnect, checkPin, finishConnect, type Landing, type ConnectProblem } from "../connect-flow.ts";
  import PairScreen from "./PairScreen.svelte";

  let { code = "", landing = false }: { code?: string; landing?: boolean } = $props();
  let pairing = $state(untrack(() => code !== "")), greeting: Hello | undefined = $state(), pin: Pin | undefined = $state();
  let result: Landing | undefined = $state(), loading = $state(true), busy = $state(false);
  let trusted = $state(""), offered = $state("");
  let button: HTMLButtonElement | undefined = $state();
  const errors: Record<ConnectProblem, string> = {
    state: "This sign-in started in another tab or browser. Start again here.",
    code: "This sign-in link no longer works. A link works once, within 5 minutes.",
    limit: "Too many sign-ins in the last minute. Wait a minute, then try again.",
    unreachable: "This machine did not answer. Check this device's connection, then try again.",
    relay: "Resident Labs did not finish the sign-in. Try again.",
  };
  async function show(value: Landing) {
    result = value;
    if (value.kind === "stop" && value.field !== "relay") {
      trusted = await fingerprint(value.pin.key);
      offered = value.offered ? await fingerprint(value.offered).catch(() => "") : "";
    }
  }
  onMount(() => {
    void (async () => {
      if (landing) {
        try { await show(await finishConnect(location.hash, value => { pin = value; })); }
        catch { result = { kind: "failed", problem: "unreachable" }; }
        loading = false; return;
      }
      try {
        const found = await hello(location.origin);
        if (!found) { pairing = true; return; }
        greeting = found;
        pin = await getOriginPin(location.origin) ?? await getPin(found.machine);
        const checked = await checkPin(found, pin);
        if (checked) await show(checked);
        else if (pin) await putPin(pin);
      } catch { result = { kind: "failed", problem: "unreachable" }; }
      finally { loading = false; }
      await tick();
      button?.focus();
    })();
  });
  async function begin() {
    if (!greeting || busy) return;
    busy = true;
    try {
      const value = await beginConnect(greeting);
      if (value) await show(value);
    } catch { result = { kind: "failed", problem: "unreachable" }; }
    busy = false;
  }
</script>

{#if pairing}<PairScreen {code} />
{:else}
  <main class="screen"><div class="column">
    <svg class:danger={result?.kind === "stop"} class="logo" viewBox="0 0 24 24" width="40" height="40" aria-hidden="true"><path d="M5 4h6.2c4.6 0 7.8 3.2 7.8 8s-3.2 8-7.8 8H5z" /></svg>
    {#if loading}
      <h1>Signing in…</h1><p>Checking that this is {pin?.name ?? "this machine"}: its key must match the one this browser trusts.</p>
    {:else if result?.kind === "stop"}
      <h1>Do not continue</h1>
      <p>{result.field === "proof" ? `This page could not prove that it is ${result.pin.name ?? "this machine"}.` : `This page offers a different ${result.field === "key" ? "key" : "sign-in service"} for ${result.pin.name ?? "this machine"} than the one this browser trusts. Either ${result.pin.name ?? "this machine"} was set up again, or someone is posing as it.`}</p>
      <div class="values"><p>Trusted since {new Date(result.pin.at).toLocaleDateString(undefined, { day: "numeric", month: "short" })}</p><code>{result.field === "relay" ? result.pin.relay : trusted}</code>
        {#if result.field !== "proof"}<p>Offered now</p><code class="danger">{result.field === "key" ? offered : result.offered}</code>{/if}</div>
      {#if result.field !== "proof"}<p><strong>If you set {result.pin.name ?? "this machine"} up again:</strong> open Settings › Sharing &amp; access on {result.pin.name ?? "the machine"} itself and {result.field === "relay" ? "check its sign-in service. If it matches the offered one" : "compare its key. If it matches the offered key"}, remove this site’s data in your browser, then sign in again.</p>{/if}
      <p>{result.field === "proof" ? (result.returning ? "Close this tab. Your sign-in code was not used." : "Close this tab. You have not signed in.") : result.returning ? "Otherwise, close this tab. Your sign-in code was not used." : "Otherwise, close this tab. You have not signed in."}</p>
    {:else if result?.kind === "failed"}
      <h1>Sign-in did not finish</h1><p class="danger" role="alert">{result.problem === "unreachable" && pin?.name ? `${pin.name} did not answer. Check this device's connection, then try again.` : errors[result.problem]}</p>
      <button class="primary" onclick={() => { location.replace("/"); }}>Sign in again</button>
      <button class="link" onclick={() => { pairing = true; }}>Pair with a code instead</button>
    {:else if greeting}
      <h1>{pin ? "Signed out" : "Sign in to Dyna"}</h1>
      {#if pin}<p>Your Connect sign-in to {pin.name ?? location.host} on this browser ended. Sign in again to return to where you left off.</p>
      {:else}<p><code>{location.host}</code> uses Resident Labs Connect. Sign in with your Resident Labs passkey.</p>{/if}
      <button bind:this={button} class="primary" disabled={busy} onclick={() => void begin()}><svg viewBox="0 0 24 24" width="20" height="20" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="9" cy="8" r="3.6"/><path d="M3 20a6 6 0 0 1 9.5-4.8"/><circle cx="17.5" cy="14" r="2.4"/><path d="M17.5 16.4V22M17.5 19.5h2.2"/></svg> Sign in with Resident Labs</button>
      <p class="small">You sign in on <code>{allowedOrigin(greeting.relay) ? new URL(greeting.relay).host : "an invalid address"}</code> and come back here.{#if !pin}{" "}From then on, this browser remembers this machine's key and checks it at every sign-in.{/if}</p>
      <div class="or"><span></span>or<span></span></div>
      <button class="outline" onclick={() => { pairing = true; }}>Pair with a code</button>
      <p class="small">A code comes from Settings › Sharing &amp; access on a signed-in device, or from <code>dyna device pair</code> on the machine.</p>
    {/if}
  </div></main>
{/if}

<style>
  .screen { display: grid; place-items: center; min-height: var(--app-height, 100dvh); padding: 24px; background: var(--bg); }
  .column { display: grid; gap: 14px; width: min(360px, 100%); }
  .logo { fill: none; stroke: var(--text); stroke-width: 2; stroke-linejoin: round; }
  .logo.danger { stroke: var(--danger); }
  h1 { margin: 0; font-size: 24px; line-height: 1.2; overflow-wrap: anywhere; }
  p { margin: 0; color: var(--muted); font-size: 14px; overflow-wrap: anywhere; }
  p strong { color: var(--text); }
  code { color: var(--text); font: 13px var(--mono); overflow-wrap: anywhere; font-variant-numeric: tabular-nums; }
  .danger { color: var(--danger); }
  .values { display: grid; gap: 4px; padding: 12px; border: 1px solid var(--line); border-radius: 8px; }
  .values p:not(:first-child) { margin-top: 8px; }
  button { min-height: 48px; border-radius: 8px; font: inherit; font-weight: 600; cursor: pointer; }
  .primary { display: flex; align-items: center; justify-content: center; gap: 8px; border: 0; background: var(--focus); color: var(--bg); }
  .primary:focus { outline: 2px solid var(--focus); outline-offset: 3px; }
  .outline { border: 1px solid var(--firm); background: transparent; color: var(--text); }
  .link { border: 0; background: none; color: var(--focus); }
  .small { font-size: 12.5px; }
  .or { display: flex; align-items: center; gap: 12px; color: var(--muted); font-size: 12.5px; }
  .or span { flex: 1; height: 1px; background: var(--line); }
  @media (max-width: 640px) { .screen { place-items: start center; padding-top: 72px; } }
</style>
