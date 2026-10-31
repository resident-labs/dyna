<script lang="ts">
  import { onMount, tick } from "svelte";
  import { call } from "../client.ts";
  import type { ProfileSummary } from "../contract.gen.ts";
  import { profileLabel, slugName } from "../profiles.ts";
  import Icon from "./Icon.svelte";
  // A session's profile, and the menu that changes it, with RoomMenu's keys. The row shows the store's profile, which
  // session.profile_changed updates, so the value moves only once the engine has changed it. Until the list loads, the
  // profile's name stands in for its label; opening the menu asks again after a failed load.
  let { session, profile, switchable }: { session: string; profile: string; switchable: boolean } = $props();
  let profiles: ProfileSummary[] | undefined = $state();
  let open = $state(false), busy = $state(false), failure = $state("");
  let root: HTMLDivElement;
  let button: HTMLButtonElement | undefined = $state();
  let list: HTMLDivElement | undefined = $state();
  let current = $derived(profiles?.find(item => item.name === profile));
  let label = $derived(current && profiles ? profileLabel(current, profiles) : { model: slugName(profile), detail: "" });
  const items = () => Array.from(list?.querySelectorAll<HTMLButtonElement>("[role=menuitemradio]") ?? []);
  async function load() {
    failure = "";
    await call("profile.list", {}).then(answer => { profiles = answer.profiles ?? []; }, () => { failure = "The profiles did not load."; });
  }
  onMount(() => { void load(); });
  async function show(next: boolean, refocus = false) {
    if (busy) return;
    if (next && !profiles) await load();
    open = next && Boolean(profiles);
    if (open) { await tick(); (items().find(item => item.ariaChecked === "true") ?? items()[0])?.focus(); } else if (refocus) button?.focus();
  }
  async function choose(name: string) {
    await show(false, true);
    if (name === profile) return;
    busy = true;
    failure = "";
    await call("session.set_profile", { session, profile: name }).catch(() => { failure = "The profile did not change. Try again."; });
    busy = false;
  }
  // Up and Down move between entries, Home and End jump, Escape closes and Tab leaves.
  function move(event: KeyboardEvent) {
    const all = items();
    const index = all.findIndex(item => item === document.activeElement);
    const to = { ArrowDown: index + 1, ArrowUp: index - 1, Home: 0, End: all.length - 1 }[event.key];
    if (to !== undefined) { event.preventDefault(); all[(to + all.length) % all.length]?.focus(); }
    if (event.key === "Escape") { event.stopPropagation(); void show(false, true); }
    if (event.key === "Tab") void show(false);
  }
</script>

<svelte:window onpointerdown={event => { if (open && event.target instanceof Node && !root.contains(event.target)) void show(false); }} />
<div class="menu" bind:this={root}>
  {#if !switchable}<span class="names">{label.model}{#if label.detail}<small>{label.detail}</small>{/if}</span>{:else}
  <button type="button" class="open" bind:this={button} aria-disabled={busy} aria-haspopup="menu" aria-expanded={open} aria-label={`Profile: ${label.model} ${label.detail}. Change profile`}
    onclick={() => void show(!open)} onkeydown={event => { if (event.key === "ArrowDown") { event.preventDefault(); void show(true); } }}>
    <span class="names">{label.model}{#if label.detail}<small>{label.detail}</small>{/if}</span><Icon name="chevron" /></button>{/if}
  {#if open && profiles}
    <div class="list" role="menu" aria-label="Profiles" tabindex="-1" bind:this={list} onkeydown={move}>
      {#each profiles as item (item.name)}{@const shown = profileLabel(item, profiles)}<button type="button" role="menuitemradio" aria-checked={item.name === profile} tabindex="-1" onclick={() => void choose(item.name)}>
        <span class="names">{shown.model}<small>{shown.detail}</small></span>{#if item.name === profile}<Icon name="check" />{/if}</button>{/each}
    </div>
  {/if}
</div>
{#if failure}<p role="alert">{failure}</p>{/if}

<style>
  .menu { position: relative; }
  .open { display: inline-flex; align-items: start; gap: 4px; max-width: 100%; margin: 0 -4px; padding: 0 4px; border: 0; border-radius: 4px; background: none; color: var(--text); font: inherit; text-align: left; cursor: pointer; }
  .open :global(svg) { flex: none; width: 14px; height: 14px; margin-top: 3px; color: var(--muted); }
  .names { display: grid; grid-template-columns: minmax(0, 1fr); min-width: 0; }
  small { color: var(--muted); font-size: 12.5px; line-height: 18px; }
  .open:hover:not([aria-disabled="true"]), .open[aria-expanded="true"] { background: var(--hover); }
  .open[aria-disabled="true"] { opacity: 0.6; cursor: default; }
  .open:focus-visible { outline: 2px solid var(--focus); outline-offset: 2px; }
  .list { position: absolute; right: 0; top: calc(100% + 4px); z-index: 2; width: 232px; max-width: calc(100vw - 32px); padding: 4px; display: grid;
    background: var(--raised); border: 1px solid var(--line); border-radius: 8px; box-shadow: 0 6px 20px var(--shadow-md); }
  [role="menuitemradio"] { display: flex; align-items: start; justify-content: space-between; gap: 12px; padding: 6px 8px; border: 0; border-radius: 6px; background: none; color: var(--text); font: inherit; font-size: 13px; line-height: 18px; text-align: left; cursor: pointer; }
  [role="menuitemradio"] :global(svg) { flex: none; width: 16px; height: 16px; margin-top: 1px; color: var(--muted); }
  /* Items have no border or ring of their own; a fill marks the hovered or focused one. */
  [role="menuitemradio"]:hover, [role="menuitemradio"]:focus { background: var(--selected); outline: none; }
  p { margin: 4px 0 0; color: var(--danger); font-size: 12.5px; line-height: 1.3; }
  @media (max-width: 640px) { [role="menuitemradio"] { padding: 11px 8px; } }
</style>
