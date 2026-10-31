<script lang="ts">
  import { onMount, untrack } from "svelte";
  import { call } from "../client.ts";
  import { clock, pairAddresses, pairLink, secondsLeft, showCode, startingScopes } from "../devices.ts";
  import { qrPath } from "../qr.ts";
  import Icon from "./Icon.svelte";
  import ScopeBoxes from "./ScopeBoxes.svelte";
  import type { Device, DevicePairResponse, Scope } from "../contract.gen.ts";
  // The pair flow, inside the page, so a new device appears in the list while the owner watches. The list is read every
  // 2 seconds while a code lives, as no event says that a code was used; the card closes when a new row arrives.
  let { devices, reload, onclose }: { devices: readonly Device[]; reload: () => void; onclose: (paired: boolean) => void } = $props();
  const known = new Set(untrack(() => devices).map(device => device.id));
  let chosen = $state(startingScopes(location.hostname));
  // The live code and the scopes it carries. While a new code is on its way, the old one is hidden, as it grants other scopes.
  let live: { code: DevicePairResponse; scopes: Scope[] } | undefined = $state.raw();
  let changing = $state(false);
  let route = $state("");
  let failed = $state(false);
  let now = $state(Date.now());
  let element: HTMLElement | undefined = $state();

  // Scopes live on the code, and a new code replaces the old one. So each change makes a code, and every exit (close,
  // or leaving the pane) makes one that nobody sees, which ends the shown one, even one still on its way. Calls run one
  // at a time, so the code shown is always the engine's live one.
  let closing = false, gone = false, running = false, again = false;
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
        if (made) { if (!gone) onclose(false); }
        // After an exit nobody can press Try again, so a failed discard tries twice more.
        else if (gone) again = ++tries < 3;
        else { failed = true; closing = false; }
      }
      // A newer request follows, so this code is never shown: its scopes are not the boxes'.
      else if (again) continue;
      else if (made) { live = { code: made, scopes }; now = Date.now(); changing = false; }
      // The old code stays hidden, and the boxes keep the owner's latest choice for Try again.
      else failed = true;
    }
    running = false;
  }
  function close() {
    if (!running && left === 0) { onclose(false); return; }
    closing = true;
    void make();
  }
  onMount(() => {
    void make();
    element?.focus();
    const ticks = setInterval(() => { now = Date.now(); }, 1000);
    const reads = setInterval(() => { if (left > 0 && addresses.length) reload(); }, 2000);
    return () => {
      clearInterval(ticks); clearInterval(reads);
      gone = true;
      if (!closing && (running || left > 0)) { closing = true; void make(); }
    };
  });
  $effect(() => { if (devices.some(device => !known.has(device.id))) onclose(true); });

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

<!-- svelte-ignore a11y_no_noninteractive_element_interactions: Escape closes the card from any control inside it. -->
<section class="pair" class:plain={!qr && !blank} aria-label="Pair a device" tabindex="-1" bind:this={element} onkeydown={event => { if (event.key === "Escape") close(); }}>
  {#if qr}<div class="tile"><svg class:dead={left === 0} viewBox={`0 0 ${qr.size} ${qr.size}`} role="img" aria-label="QR code of the pairing link" shape-rendering="crispEdges"><path d={qr.d} /></svg>
    {#if left === 0}<button type="button" class="btn fill" onclick={() => void make()}>Make a new code</button>{/if}</div>
  {:else if blank}<div class="tile blank" aria-hidden="true"></div>{/if}
  <div class="side">
    <h3>Pair a device<button type="button" class="close" aria-label="Close and end this code" title="Close" onclick={close}><Icon name="close" /></button></h3>
    {#if failed}<p class="problem" role="alert">{live ? "The code did not change." : "The code was not made."} <button type="button" class="retry" onclick={() => void make()}>Try again</button></p>{/if}
    {#if live && !addresses.length}<p>No web address is set, so another device cannot reach this engine. Start the engine with <code>--web.route</code>, then pair again.</p>
    {:else if live}
      <div><div class="code" class:dead={left === 0 || changing}>{changing ? "·····-·····" : showCode(live.code.code)}</div><div class="left" role="timer">{changing ? "Making a new code…" : left ? `Works once, for ${clock(left)}` : "Expired"}</div></div>
      {#if addresses.length > 1}<span class="address"><select aria-label="Web address" bind:value={() => address, value => { route = value; }}>
        {#each addresses as item (item)}<option value={item}>{item}</option>{/each}</select><Icon name="chevron" size={14} /></span>{/if}
      <p>Scan it with the device's camera, or type the code there. A paired device acts as you.</p>
      <ScopeBoxes value={chosen} onchange={next => { chosen = next; changing = true; void make(); }} />
      <p class="note">Changing the access makes a new code.</p>
      <div><button type="button" class="btn" disabled={left === 0 || changing} onclick={copy} aria-live="polite">{copied || "Copy link"}</button></div>
    {/if}
  </div>
</section>

<style>
  .pair { display: grid; grid-template-columns: 176px minmax(0, 1fr); gap: 20px; padding: 16px; border: 1px solid var(--line); border-radius: 12px; background: var(--raised); }
  .pair.plain { grid-template-columns: minmax(0, 1fr); }
  .pair:focus-visible { outline: none; }
  .tile { position: relative; display: grid; place-items: center; width: 176px; height: 176px; border-radius: 8px; overflow: hidden; }
  .tile svg { position: absolute; inset: 0; box-sizing: border-box; width: 100%; height: 100%; padding: 10px; background: var(--qr-paper); fill: var(--qr-ink); }
  .tile svg.dead { opacity: 0.28; }
  .tile .btn { position: relative; }
  .tile.blank { background: var(--selected); }
  .side { display: grid; gap: 12px; align-content: start; min-width: 0; }
  h3 { display: flex; align-items: center; margin: 0; font-size: 16px; line-height: 1.25; }
  .close { display: grid; place-items: center; width: 28px; height: 28px; margin: -4px -6px -4px auto; padding: 0; border: 0; border-radius: 6px; background: none; color: var(--muted); cursor: pointer; }
  .close:hover { background: var(--hover); color: var(--text); }
  .code { font: 600 26px/1.2 var(--mono); letter-spacing: 0.04em; font-variant-ligatures: none; }
  .code.dead { color: var(--muted); }
  .left, .note { color: var(--muted); font-size: 12.5px; font-variant-numeric: tabular-nums; }
  p { margin: 0; color: var(--muted); font-size: 13px; }
  p code { color: var(--text); font: 12.5px var(--mono); }
  .problem { color: var(--danger); font-weight: 500; }
  .address { position: relative; display: inline-flex; align-items: center; width: max-content; max-width: 100%; color: var(--muted); }
  .address select { appearance: none; max-width: 100%; height: 32px; padding: 0 28px 0 10px; border: 1px solid var(--firm); border-radius: 6px; background: none; color: var(--text); font: 12.5px var(--mono); cursor: pointer; }
  .address select option { background: var(--raised); }
  .address :global(svg) { position: absolute; right: 8px; pointer-events: none; }
  @media (hover: none) { .close { width: 40px; height: 40px; margin: -8px -10px -8px auto; } .address select { height: 40px; } }
  @media (max-width: 640px) {
    .pair { grid-template-columns: minmax(0, 1fr); }
    .tile { justify-self: center; }
  }
</style>
