<script lang="ts">
  import { onMount, type Snippet } from "svelte";
  import { APIError, call, SignedOut } from "../client.ts";
  import { profileForSave, slugName } from "../profiles.ts";
  import type { ProfileSummary, ThinkingLevel } from "../contract.gen.ts";
  import Icon from "./Icon.svelte";
  import ProfileEditor from "./ProfileEditor.svelte";

  let { bar, phone, signedout }: { bar: Snippet<[string, number | undefined, Snippet | undefined]>; phone: boolean; signedout: () => void } = $props();
  let profiles: ProfileSummary[] = $state([]), loaded = $state(false), failed = $state(false);
  let editing: string | undefined = $state(), creating = $state(false), busy = $state(false), error = $state("");
  const blank: ProfileSummary = { inUse: 0, name: "", provider: "openai", model: "", thinking: "medium", description: "", taste: 0, diagnostics: 0, intelligence: 0, cost: 0 };
  function close() { editing = undefined; creating = false; error = ""; }
  function showError(reason: unknown, action: "save" | "remove") {
    if (reason instanceof SignedOut) signedout();
    else if (reason instanceof APIError && reason.detail.code === "profile_exists") error = "A profile with this name already exists.";
    else if (reason instanceof APIError && action === "remove" && reason.detail.code === "conflict") error = "This profile is in use and cannot be removed.";
    else error = `Could not ${action} this profile. Try again.`;
  }
  async function save(draft: ProfileSummary) {
    if (busy) return;
    busy = true; error = "";
    const profile = profileForSave(draft);
    await call("profile.set", { create: creating, profile }).then(() => { close(); reload(); }, (reason: unknown) => showError(reason, "save"));
    busy = false;
  }
  async function remove(name: string) {
    if (busy) return;
    busy = true; error = "";
    await call("profile.remove", { name }).then(() => { close(); reload(); }, (reason: unknown) => showError(reason, "remove"));
    busy = false;
  }
  const thinking: Record<ThinkingLevel, string> = {
    off: "no thinking", minimal: "minimal thinking", low: "low thinking", medium: "medium thinking", high: "high thinking", xhigh: "very high thinking",
  };
  function reload() {
    void call("profile.list", {}).then(result => { profiles = result.profiles ?? []; loaded = true; failed = false; },
      (error: unknown) => { if (error instanceof SignedOut) signedout(); else if (!loaded) failed = true; });
  }
  onMount(() => { reload(); window.addEventListener("focus", reload); return () => window.removeEventListener("focus", reload); });
  const unrated = (p: ProfileSummary) => !p.taste && !p.diagnostics && !p.intelligence && !p.cost;
  const rating = (n: number) => n || "Not rated";
</script>

{#snippet action()}<button type="button" class="new" onclick={() => { close(); creating = true; }}>{phone ? "New" : "New profile"}</button>{/snippet}
{#if phone && (editing || creating)}
  <header class="phone-bar"><button type="button" onclick={close} aria-label="Back to Profiles"><Icon name="backChevron" /></button><strong>{creating ? "New profile" : editing}</strong></header>
{:else}
  {@render bar("Profiles", loaded ? profiles.length : undefined, action)}
{/if}
<div class="content">
  {#if phone && creating}
    <ProfileEditor profile={blank} isNew={true} {busy} {error} {save} cancel={close} remove={() => {}} />
  {:else if phone && editing}
    {#each profiles.filter(profile => profile.name === editing) as profile (profile.name)}
      <p class="summary">{slugName(profile.model)} · {thinking[profile.thinking]}</p>
      <ProfileEditor {profile} isNew={false} {busy} {error} {save} cancel={close} remove={() => void remove(profile.name)} />
    {/each}
  {:else}
  {#if failed}<p role="alert">The profiles did not load. <button type="button" onclick={reload}>Try again</button></p>
  {:else if !loaded}<p class="muted">Loading profiles…</p>
  {:else}
    {#if creating}<div class="table"><ProfileEditor profile={blank} isNew={true} {busy} {error} {save} cancel={close} remove={() => {}} /></div>{/if}
    <p class="summary">{profiles.length} {profiles.length === 1 ? "profile" : "profiles"} · {profiles.filter(unrated).length} not rated</p>
    {#if !profiles.length}<p class="muted">No profiles yet.</p>
    {:else}<div class="table">
      <div class="head"><span></span><span>Taste</span><span>Diagnostics</span><span>Intelligence</span><span>Thrift</span><span>Limits</span><span></span></div>
      {#each profiles as profile (profile.name)}
        <div class="row">
          <div class="identity"><strong title={profile.name}>{profile.name}</strong><span>{slugName(profile.model)} · {thinking[profile.thinking]}</span></div>
          {#if unrated(profile)}
            <span class="unrated">Not rated · <button type="button" aria-label={`Rate ${profile.name}`} onclick={() => { close(); editing = profile.name; }}>Rate</button></span>
          {:else}
            {#each ([["Taste", profile.taste], ["Diagnostics", profile.diagnostics], ["Intelligence", profile.intelligence], ["Thrift", profile.cost]] as const) as [label, value]}
              <span class="number" class:missing={!value}><span class="rating-label">{label} </span>{rating(value)}</span>
            {/each}
          {/if}
          <span class="limits">{#if profile.maxConcurrent}At most {profile.maxConcurrent} at once{:else}No limit{/if}</span>
          <button type="button" class="edit" onclick={() => { const name = editing === profile.name ? undefined : profile.name; close(); editing = name; }} aria-label={`Edit ${profile.name}`}>{#if phone}<Icon name="forwardChevron" />{:else}Edit{/if}</button>
        </div>
        {#if !phone && editing === profile.name}<ProfileEditor {profile} isNew={false} {busy} {error} {save} cancel={close} remove={() => void remove(profile.name)} />{/if}
      {/each}
    </div>{/if}
  {/if}
  {/if}
</div>

<style>
  .new { margin-left: auto; }
  .new { border: 1px solid var(--line); border-radius: 6px; background: transparent; color: var(--text); font: inherit; padding: 8px 12px; cursor: pointer; }
  .phone-bar { height: 52px; flex: none; display: flex; align-items: center; gap: 8px; padding: 0 16px; border-bottom: 1px solid var(--line); }
  .phone-bar button { width: 32px; height: 40px; display: grid; place-items: center; border: 0; color: var(--muted); background: transparent; cursor: pointer; }
  .content { container-type: inline-size; overflow: auto; padding: 20px 16px 32px; }
  .summary, .muted { color: var(--muted); margin: 0 8px 16px; }
  .summary { font-size: 12.5px; }
  .table { max-width: 1048px; border: 1px solid var(--line); border-radius: 12px; background: var(--raised); overflow: hidden; }
  .head, .row { display: grid; grid-template-columns: minmax(250px, 4fr) repeat(4, minmax(66px, .9fr)) minmax(130px, 2fr) 48px; align-items: center; gap: 10px; padding: 12px 14px; }
  .head { color: var(--muted); font-size: 12px; padding-top: 8px; padding-bottom: 4px; }
  .row { border-top: 1px solid var(--line); min-height: 66px; }
  .identity { min-width: 0; display: flex; flex-direction: column; }
  .identity strong { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .identity span, .number, .limits { color: var(--muted); font-size: 12.5px; }
  .number { font-variant-numeric: tabular-nums; }
  .rating-label { display: none; }
  .unrated { grid-column: span 4; color: var(--muted); font-size: 12.5px; }
  .unrated button { border: 0; background: none; color: var(--text); font: inherit; padding: 0; cursor: pointer; text-decoration: underline; }
  .edit { border: 0; background: none; color: var(--muted); cursor: pointer; font: inherit; padding: 8px; }
  @container (max-width: 800px) {
    .head { display: none; }
    .row { position: relative; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 8px 12px; padding: 14px; }
    .identity { grid-column: 1 / -1; padding-right: 48px; }
    .edit { position: absolute; right: 14px; top: 10px; }
    .number { grid-column: span 1; color: var(--text); }
    .number.missing, .rating-label { color: var(--muted); }
    .rating-label { display: inline; margin-right: 4px; }
    .unrated, .limits { grid-column: 1 / -1; }
  }
</style>
