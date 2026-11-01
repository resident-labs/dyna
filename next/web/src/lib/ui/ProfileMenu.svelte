<script lang="ts">
  import { onMount } from "svelte";
  import { call } from "../client.ts";
  import type { ProfileSummary } from "../contract.gen.ts";
  import { profileLabel, slugName } from "../profiles.ts";
  import Icon from "./Icon.svelte";
  import MenuList from "./MenuList.svelte";
  // A session's profile, and the menu that changes it, with RoomMenu's keys. The row shows the store's profile, which
  // session.profile_changed updates, so the value moves only once the engine has changed it. Until the list loads, the
  // profile's name stands in for its label; opening the menu asks again after a failed load.
  let { session, profile, switchable }: { session: string; profile: string; switchable: boolean } = $props();
  let profiles: ProfileSummary[] | undefined = $state();
  let open = $state(false), busy = $state(false), failure = $state("");
  let root: HTMLDivElement;
  let button: HTMLButtonElement | undefined = $state();
  let current = $derived(profiles?.find(item => item.name === profile));
  let label = $derived(current && profiles ? profileLabel(current, profiles) : { model: slugName(profile), detail: "" });
  let rows = $derived.by(() => {
    const all = profiles ?? [];
    return all.map(item => ({ name: item.name, shown: profileLabel(item, all) }));
  });
  async function load() {
    failure = "";
    await call("profile.list", {}).then(answer => { profiles = answer.profiles ?? []; }, () => { failure = "The profiles did not load."; });
  }
  onMount(() => { void load(); });
  async function show(next: boolean, refocus = false) {
    if (busy) return;
    if (next && !profiles) await load();
    open = next && Boolean(profiles);
    if (!open && refocus) button?.focus();
  }
  async function choose(name: string) {
    await show(false, true);
    if (name === profile) return;
    busy = true;
    failure = "";
    await call("session.set_profile", { session, profile: name }).catch(() => { failure = "The profile did not change. Try again."; });
    busy = false;
  }
</script>

<svelte:window onpointerdown={event => { if (open && event.target instanceof Node && !root.contains(event.target)) void show(false); }} />
<div class="menu" bind:this={root}>
  {#if !switchable}<span class="names">{label.model}{#if label.detail}<small>{label.detail}</small>{/if}</span>{:else}
  <button type="button" class="open" bind:this={button} aria-disabled={busy} aria-haspopup="menu" aria-expanded={open} aria-label={`Profile: ${label.model} ${label.detail}. Change profile`}
    onclick={() => void show(!open)} onkeydown={event => { if (event.key === "ArrowDown") { event.preventDefault(); void show(true); } }}>
    <span class="names">{label.model}{#if label.detail}<small>{label.detail}</small>{/if}</span><Icon name="chevron" /></button>{/if}
  {#if open && profiles}
    <MenuList label="Profiles" items={rows} keyOf={entry => entry.name} checked={profile} onchoose={name => void choose(name)} onclose={refocus => void show(false, refocus)}>
      {#snippet row(entry)}<span class="names">{entry.shown.model}<small>{entry.shown.detail}</small></span>{/snippet}
    </MenuList>
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
  p { margin: 4px 0 0; color: var(--danger); font-size: 12.5px; line-height: 1.3; }
</style>
