<script lang="ts">
  import { onMount, untrack } from "svelte";
  import { call } from "../client.ts";
  import { clock, pairAddresses, pairLink, secondsLeft, showCode, startingScopes } from "../devices.ts";
  import { qrPath } from "../qr.ts";
  import Icon from "./Icon.svelte";
  import ScopeBoxes from "./ScopeBoxes.svelte";
  import type { Device, DevicePairResponse, Scope } from "../contract.gen.ts";
  // The pair flow, in the detail's place of Settings > Sharing & access, so a new device appears in the list while the
  // owner watches. The list is read every 2 seconds while a code lives, as no event says that a code was used; when a
  // new device arrives, onpaired names it. close ends the code first and then calls onclose; leaving any other way, as
  // Back or another row does, ends it as the flow unmounts.
  let { devices, reload, onclose, onpaired }: { devices: readonly Device[]; reload: () => void; onclose: () => void; onpaired: (id: string) => void } = $props();
  const known = new Set(untrack(() => devices).map(device => device.id));
  let chosen = $state(startingScopes(location.hostname));
  // The live code and the scopes it carries. While a new code is on its way, the old one is hidden, as it grants other scopes.
  let live: { code: DevicePairResponse; scopes: Scope[] } | undefined = $state.raw();
  let changing = $state(false);
  let route = $state("");
  let failed = $state(false);
  let now = $state(Date.now());

  // Scopes live on the code, and a new code replaces the old one. So each change makes a code, and every exit (close,
  // or leaving the pane) makes one that nobody sees, which ends the shown one, even one still on its way. Calls run one
  // at a time, so the code shown is always the engine's live one.
  let closing = $state(false);
  let gone = false, running = false, again = false;
  async function make() {
    again = true;
    if (running) return;
    running = true;
    failed = false;
    let tries = 0;
    while (again) {
      again = false;
      const discard = closing;
      const scopes: Scope[] = discard ? ["read"] : chosen;
      const made = await call("device.pair", { scopes }).catch(() => undefined);
      if (discard) {
        if (made) { if (!gone) onclose(); }
        // After an exit nobody can press Try again, so a failed discard tries twice more.
      // If all three fail, the engine still ends the code after 5 minutes: an accepted limit, listed in the PR body under Accepted limits.
        else if (gone) again = ++tries < 3;
        // The close stays asked for, so Try again tries to end the code again.
        else failed = true;
      }
      // A newer request follows, so this code is never shown: its scopes are not the boxes'.
      else if (again) continue;
      else if (made) { live = { code: made, scopes }; now = Date.now(); changing = false; }
      // The old code stays hidden, and the boxes keep the owner's latest choice for Try again.
      else failed = true;
    }
    running = false;
  }
  export function close() {
    if (!running && left === 0) { onclose(); return; }
    closing = true;
    void make();
  }
  onMount(() => {
    void make();
    const ticks = setInterval(() => { now = Date.now(); }, 1000);
    const reads = setInterval(() => { if (left > 0 && addresses.length) reload(); }, 2000);
    return () => {
      clearInterval(ticks); clearInterval(reads);
      gone = true;
      if (!closing && (running || left > 0)) { closing = true; void make(); }
    };
  });
  $effect(() => { const fresh = devices.find(device => !known.has(device.id)); if (fresh) onpaired(fresh.id); });

  let left = $derived(live ? secondsLeft(live.code.expiresAt, now) : 0);
  let addresses = $derived(pairAddresses(location.origin, location.hostname, live?.code.routes ?? []));
  let address = $derived(addresses.includes(route) ? route : addresses[0]);
  let link = $derived(live && address && !changing ? pairLink(address, live.code.code) : "");
  let qr = $derived(link ? qrPath(link) : undefined);
  // The tile keeps its place while a new code is on its way, so the card does not jump.
  let blank = $derived(changing && addresses.length > 0);

  // Copy answers in place for 2 seconds, as a post's Copy does. A plain http page has no navigator.clipboard, so its throw becomes a rejection.
  let copied = $state("");
  let timer: ReturnType<typeof setTimeout> | undefined;
  function copy() {
    clearTimeout(timer);
    Promise.resolve().then(() => navigator.clipboard.writeText(link)).then(() => { copied = "Copied"; }, () => { copied = "Could not copy"; })
      .finally(() => { timer = setTimeout(() => { copied = ""; }, 2000); });
  }
</script>
<!-- The flow fills the detail: what to do, the code to scan or type in a card, the address, the access, Copy link. -->
<div class="pair">
  {#if failed}<p class="problem" role="alert">{closing ? "The code was not ended." : live ? "The code did not change." : "The code was not made."} <button type="button" class="retry" onclick={() => void make()}>Try again</button></p>{/if}
  {#if !live}{#if !failed}<p>Making a code…</p>{/if}
  {:else if !addresses.length}<p>No web address is set, so another device cannot reach this engine. Start the engine with <code>--web.route</code>, then pair again.</p>
  {:else}
    <p>Scan it with the device's camera, or type the code there. A paired device acts as you.</p>
    <div class="card">
      {#if qr}<div class="tile"><svg class:dead={left === 0} viewBox={`0 0 ${qr.size} ${qr.size}`} role="img" aria-label="QR code of the pairing link" shape-rendering="crispEdges"><path d={qr.d} /></svg>
        {#if left === 0}<button type="button" class="btn fill" onclick={() => void make()}>Make a new code</button>{/if}</div>
      {:else if blank}<div class="tile blank" aria-hidden="true"></div>{/if}
      <div class="code" class:dead={left === 0 || changing}>{changing ? "·····-·····" : showCode(live.code.code)}</div>
      <div class="left" role="timer">{changing ? (failed ? "" : "Making a new code…") : left ? `Works once, for ${clock(left)}` : "Expired"}</div>
    </div>
    {#if addresses.length > 1}<label class="field">Web address<span class="address"><select bind:value={() => address, value => { route = value; }}>
      {#each addresses as item (item)}<option value={item}>{item}</option>{/each}</select><Icon name="chevron" size={14} /></span></label>{/if}
    <div class="field"><span>Access for the new device</span>
      <ScopeBoxes value={chosen} onchange={next => { chosen = next; changing = true; void make(); }} />
      <span class="note">Changing the access makes a new code.</span></div>
    <div><button type="button" class="btn" disabled={left === 0 || changing} onclick={copy} aria-live="polite">{copied || "Copy link"}</button></div>
  {/if}
</div>

<style>
  .pair { display: grid; gap: 16px; min-width: 0; }
  .card { display: grid; justify-items: center; gap: 6px; padding: 16px; border: 1px solid var(--line); border-radius: 12px; background: var(--raised); }
  .tile { position: relative; display: grid; place-items: center; width: 176px; height: 176px; margin-bottom: 6px; border-radius: 8px; overflow: hidden; }
  .tile svg { position: absolute; inset: 0; box-sizing: border-box; width: 100%; height: 100%; padding: 10px; background: var(--qr-paper); fill: var(--qr-ink); }
  .tile svg.dead { opacity: 0.28; }
  .tile .btn { position: relative; }
  .tile.blank { background: var(--selected); }
  .code { font: 600 24px/1.2 var(--mono); letter-spacing: 0.04em; font-variant-ligatures: none; }
  .code.dead { color: var(--muted); }
  .left, .note { color: var(--muted); font-size: 12.5px; font-weight: 400; font-variant-numeric: tabular-nums; }
  p { margin: 0; color: var(--muted); font-size: 12.5px; line-height: 1.4; }
  p code { color: var(--text); font: 12.5px var(--mono); }
  .problem { color: var(--danger); font-size: 13px; font-weight: 500; }
  .field { display: grid; gap: 8px; min-width: 0; font-size: 14px; font-weight: 600; }
  .address { position: relative; display: inline-flex; align-items: center; width: max-content; max-width: 100%; color: var(--muted); }
  .address select { appearance: none; max-width: 100%; height: 32px; padding: 0 28px 0 10px; border: 1px solid var(--firm); border-radius: 6px; background: none; color: var(--text); font: 12.5px var(--mono); cursor: pointer; }
  .address select option { background: var(--raised); }
  .address :global(svg) { position: absolute; right: 8px; pointer-events: none; }
  @media (hover: none) { .address select { height: 40px; } }
</style>
