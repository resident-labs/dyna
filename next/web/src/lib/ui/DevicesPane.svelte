<script lang="ts">
  import { tick, type Snippet } from "svelte";
  import { call } from "../client.ts";
  import { access, expiring, isPhone, listed, seen } from "../devices.ts";
  import Icon from "./Icon.svelte";
  import PairCard from "./PairCard.svelte";
  import ScopeBoxes from "./ScopeBoxes.svelte";
  import StatusPill from "./StatusPill.svelte";
  import type { Device, Scope } from "../contract.gen.ts";
  // Settings > Devices. One row per browser, each with Scopes and Revoke, and the pair flow above the list.
  // Revoking is one tap plus a confirm in place; a revoked device must pair again.
  let { devices, failed, reload, signedout, bar }: { devices: readonly Device[] | undefined; failed: boolean; reload: () => void; signedout: () => void;
    bar: Snippet<[string, number | undefined, Snippet | undefined]> } = $props();
  let pairing = $state(false);
  let pairButton: HTMLButtonElement | undefined = $state();
  let editing: { id: string; scopes: Scope[]; confirm: boolean } | undefined = $state();
  let revoking: string | undefined = $state();
  let busy = $state(false);
  // One row's failed change, in the owner's words.
  let problem: { id: string; text: string } | undefined = $state();
  async function closePair(paired: boolean) {
    pairing = false;
    if (paired) return;
    await tick();
    pairButton?.focus();
  }
  async function act(id: string, text: string, run: () => Promise<unknown>, done: () => void) {
    busy = true;
    problem = undefined;
    await run().then(done, () => { problem = { id, text }; });
    busy = false;
  }
  const revoke = (device: Device) => act(device.id, "The device was not revoked. Try again.", () => call("device.revoke", { id: device.id }), () => {
    revoking = undefined;
    if (device.current) signedout(); else reload();
  });
  // Without admin this browser loses the Devices pane, so that change confirms first. Its row goes when the list reloads.
  function save(device: Device) {
    if (!editing) return;
    const { scopes, confirm } = editing;
    if (device.current && device.scopes?.includes("admin") && !scopes.includes("admin") && !confirm) { editing.confirm = true; return; }
    void act(device.id, "The access did not change. Try again.", () => call("device.set_scopes", { id: device.id, scopes }), () => { editing = undefined; reload(); });
  }
</script>

{#snippet action()}{#if !pairing}<button type="button" class="btn pair-button" bind:this={pairButton} onclick={() => { pairing = true; }}>Pair a device</button>{/if}{/snippet}
{@render bar("Devices", devices?.length, devices ? action : undefined)}
<div class="body">
  {#if failed}<p class="load">The devices did not load. <button type="button" class="retry" onclick={reload}>Try again</button></p>
  {:else if !devices}<div class="list" aria-label="Loading devices">{#each [0, 1, 2] as row (row)}<div class="row"><i class="bone"></i><span class="lines"><i class="bone name"></i><i class="bone line"></i></span></div>{/each}</div>
  {:else}
    {#if pairing}<PairCard {devices} {reload} onclose={paired => void closePair(paired)} />{/if}
    <div class="list">
      {#if devices.length === 0}<p class="load">No devices yet.</p>{/if}
      {#each listed(devices) as device (device.id)}{@const warning = expiring(device.expiresAt, Date.now())}
        <div class="row">
          <Icon name={isPhone(device.name) ? "phone" : "laptop"} size={20} />
          <span class="lines">
            <span class="title">{device.name}{#if device.current}<span class="badge">This device</span>{/if}</span>
            <span class="line">{#if warning}<StatusPill tone="warning" label={warning} />{" · "}{/if}{seen(device, Date.now())} · {access(device.scopes ?? [])}</span>
          </span>
          {#if revoking === device.id}
            <span class="ask">{device.current ? "Revoke? You will be signed out." : "Revoke?"}
              <span class="choices"><button type="button" class="btn danger" disabled={busy} onclick={() => void revoke(device)}>Revoke</button>
              <button type="button" class="btn ghost" disabled={busy} onclick={() => { revoking = undefined; }}>Cancel</button></span></span>
          {:else if editing?.id !== device.id}
            <span class="acts"><button type="button" class="btn ghost" aria-label={`Change the access of ${device.name}`} onclick={() => { problem = undefined; editing = { id: device.id, scopes: [...device.scopes ?? []], confirm: false }; }}>Scopes</button>
              <button type="button" class="btn ghost" aria-label={`Revoke ${device.name}`} onclick={() => { problem = undefined; revoking = device.id; }}>Revoke</button></span>
          {/if}
        </div>
        {#if editing?.id === device.id}
          <div class="edit">
            <ScopeBoxes value={editing.scopes} disabled={busy} onchange={next => { if (editing) editing = { ...editing, scopes: next, confirm: false }; }} />
            {#if editing.confirm}<span class="ask">Remove Admin? This browser loses the Devices pane.
              <span class="choices"><button type="button" class="btn danger" disabled={busy} onclick={() => save(device)}>Remove</button>
              <button type="button" class="btn ghost" disabled={busy} onclick={() => { if (editing) editing.confirm = false; }}>Cancel</button></span></span>
            {:else}<span class="choices"><button type="button" class="btn fill" disabled={busy} onclick={() => save(device)}>Save</button>
              <button type="button" class="btn ghost" disabled={busy} onclick={() => { editing = undefined; }}>Cancel</button></span>{/if}
          </div>
        {/if}
        {#if problem?.id === device.id}<p class="problem" role="alert">{problem.text}</p>{/if}
      {/each}
    </div>
  {/if}
</div>

<style>
  .body { flex: 1; min-height: 0; display: grid; gap: 16px; align-content: start; width: min(1040px, 100%); padding: 20px 24px; overflow-y: auto; }
  .pair-button { margin-left: auto; }
  .list { display: grid; padding: 6px; border: 1px solid var(--line); border-radius: 12px; background: var(--raised); }
  .row { display: flex; align-items: center; gap: 12px; min-height: 44px; padding: 4px 8px; border-radius: 6px; }
  .row > :global(svg) { color: var(--muted); }
  .lines { flex: 1; min-width: 0; display: flex; flex-direction: column; line-height: 1.3; }
  .title { display: flex; align-items: center; gap: 6px; min-width: 0; font-size: 14px; font-weight: 600; }
  .line { min-width: 0; color: var(--muted); font-size: 12.5px; overflow-wrap: anywhere; }
  .acts, .choices { flex: none; display: inline-flex; align-items: center; gap: 4px; }
  .ask { flex: none; display: inline-flex; align-items: center; flex-wrap: wrap; gap: 8px; font-size: 13px; font-weight: 500; }
  .edit { display: grid; gap: 10px; justify-items: start; padding: 8px 8px 10px 40px; }
  .problem { margin: 0; padding: 0 8px 8px 40px; color: var(--danger); font-size: 13px; font-weight: 500; }
  .load { margin: 0; color: var(--muted); font-size: 14px; }
  .bone { display: block; flex: none; width: 20px; height: 20px; border-radius: 6px; background: var(--selected); }
  .bone.name { width: 140px; height: 12px; margin-bottom: 6px; }
  .bone.line { width: 240px; height: 10px; }
  @media (max-width: 640px) {
    .body { padding: 12px 16px; }
    .row { flex-wrap: wrap; padding: 6px 8px; }
    /* Scopes and Revoke align with the name's left edge. */
    .lines { flex-basis: calc(100% - 32px); }
    .acts, .ask { width: 100%; margin-left: 32px; }
    .acts .btn { padding: 0 10px 0 0; }
    .edit, .problem { padding-left: 40px; }
  }
</style>
