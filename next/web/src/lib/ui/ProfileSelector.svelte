<script lang="ts">
  import { tick } from "svelte";
  import type { ProfileSummary } from "../contract.gen.ts";
  import { call } from "../client.ts";
  import Icon from "./Icon.svelte";
  import { slugName } from "../profiles.ts";

  let { value = $bindable(""), label = "Profile" }: { value?: string; label?: string } = $props();
  let profiles: ProfileSummary[] = $state([]);
  let loading = $state(true), failure = $state(false), expanded = $state(false);
  let container: HTMLDivElement;
  const limit = 3;
  let visible = $derived.by(() => {
    if (expanded || profiles.length <= limit) return profiles;
    const top = profiles.slice(0, limit);
    const selected = profiles.find(profile => profile.name === value);
    if (selected && !top.includes(selected)) top[limit - 1] = selected;
    return top;
  });

  let focusable = $derived(visible.find(profile => profile.name === value)?.name ?? visible[0]?.name);

  async function loadProfiles() {
    loading = true;
    failure = false;
    await call("profile.list", {}).then(result => {
      profiles = (result.profiles ?? []).sort((a, b) => b.inUse - a.inUse);
      if (!profiles.some(item => item.name === value)) value = profiles[0]?.name ?? "";
    }, () => { failure = true; });
    loading = false;
  }
  $effect(() => { void loadProfiles(); });

  async function move(event: KeyboardEvent, index: number) {
    const step = event.key === "ArrowDown" || event.key === "ArrowRight" ? 1 :
      event.key === "ArrowUp" || event.key === "ArrowLeft" ? -1 : 0;
    if (!step) return;
    event.preventDefault();
    const next = visible[(index + step + visible.length) % visible.length];
    value = next.name;
    await tick();
    container.querySelector<HTMLButtonElement>('[role="radio"][aria-checked="true"]')?.focus();
  }
</script>

<div class="selector" bind:this={container}>
  {#if loading}<p class="hint">Loading profiles…</p>
  {:else if failure}<p class="error" role="alert">Profiles did not load. <button type="button" class="retry" onclick={() => void loadProfiles()}>Retry</button></p>
  {:else if !profiles.length}<p class="hint">Add a profile in settings before continuing.</p>
  {:else}
  <div class="heading">{label} <span>· {profiles.length}</span></div>
  <div class="list" role="radiogroup" aria-label={label}>
    {#each visible as profile, index (profile.name)}
      <button type="button" role="radio" aria-checked={profile.name === value}
        tabindex={profile.name === focusable ? 0 : -1}
        class:selected={profile.name === value}
        onclick={() => value = profile.name} onkeydown={event => void move(event, index)}>
        <span class="radio" aria-hidden="true"></span>
        <span class="identity">
          <span class="title"><strong>{profile.name}</strong><small>{slugName(profile.model)} · {profile.thinking}</small></span>
          {#if profile.description}<span class="description">{profile.description}</span>{/if}
        </span>
        {#if profile.taste || profile.diagnostics || profile.intelligence || profile.cost}
        <span class="bars">
          {#each [{ name: "Taste", value: profile.taste }, { name: "Diagnostics", value: profile.diagnostics }, { name: "Intelligence", value: profile.intelligence }, { name: "Cost", value: profile.cost }] as rating}
            <span class="rating" aria-label={`${rating.name}: ${rating.value} out of 10`}>
              <span>{rating.name} {rating.value}</span><span class="track"><span style:width={`${rating.value * 10}%`}></span></span>
            </span>
          {/each}
        </span>
        {/if}
      </button>
    {/each}
  </div>
  {#if profiles.length > limit}
    <button type="button" class="more" aria-expanded={expanded} onclick={() => expanded = !expanded}>
      <span class:up={expanded}><Icon name="chevron" /></span> {expanded ? "Show fewer profiles" : `Show all ${profiles.length} profiles`}
    </button>
  {/if}
  {/if}
</div>

<style>
  .hint, .error { margin: 0; font-size: 13px; color: var(--muted); }
  .error { color: var(--danger); }
  .retry { padding: 0; border: 0; background: none; color: inherit; font: inherit; text-decoration: underline; cursor: pointer; }
  .up { transform: rotate(180deg); }
  .selector { container-type: inline-size; min-width: 0; }
  .heading { margin-bottom: 8px; font-size: 13px; font-weight: 600; }
  .heading span { color: var(--muted); font-weight: 400; }
  .list { overflow: hidden; border: 1px solid var(--line); border-radius: 12px 12px 0 0; background: var(--raised); }
  .list:last-child { border-radius: 12px; }
  .list button { width: 100%; display: grid; grid-template-columns: 18px minmax(0, 1fr) 380px; align-items: center; gap: 4px 12px; padding: 9px 12px; border: 0; border-top: 1px solid var(--line); background: transparent; color: var(--text); text-align: left; font: inherit; cursor: pointer; }
  .list button:first-child { border-top: 0; }
  .list button:hover { background: var(--hover); }
  .list button.selected { background: var(--selected); }
  .radio { width: 16px; height: 16px; border-radius: 50%; box-shadow: inset 0 0 0 1.5px var(--firm); }
  .selected .radio { box-shadow: inset 0 0 0 5px var(--focus); }
  .identity { display: grid; min-width: 0; gap: 1px; }
  .title { display: flex; align-items: baseline; gap: 8px; white-space: nowrap; min-width: 0; }
  strong { font-size: 14px; font-weight: 600; }
  small, .description { color: var(--muted); font-size: 12.5px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .bars { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 10px; }
  .rating { display: grid; gap: 4px; color: var(--muted); font-size: 11px; }
  .track { display: block; height: 3px; background: var(--track); border-radius: 3px; overflow: hidden; }
  .track span { display: block; height: 100%; background: var(--muted); }
  .more { display: flex; width: 100%; align-items: center; gap: 6px; padding: 9px 12px; border: 1px solid var(--line); border-top: 0; border-radius: 0 0 12px 12px; background: var(--raised); color: var(--focus); font: 13px var(--sans); text-align: left; cursor: pointer; }
  .more:hover { background: var(--hover); }
  @container (max-width: 500px) {
    .bars { gap: 4px; }
    .rating { font-size: 9px; white-space: nowrap; }
  }
  @container (max-width: 900px) {
    .list button { grid-template-columns: 18px minmax(0, 1fr); }
    .bars { grid-column: 2; }
  }
</style>
