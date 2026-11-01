<script lang="ts">
  import { onMount } from "svelte";
  import { call } from "../client.ts";
  import type { ProfileSummary, RoomSummary } from "../contract.gen.ts";
  import { nameProblem } from "../name-problem.ts";
  import ProfileSelector from "./ProfileSelector.svelte";

  let { rooms, first, onclose, oncreated }: {
    rooms: RoomSummary[]; first: boolean; onclose: () => void; oncreated: (room: string) => void;
  } = $props();
  let name = $state(""), repo = $state(""), profile = $state("");
  let profiles: ProfileSummary[] = $state([]);
  let loading = $state(true), busy = $state(false), attempted = $state(false);
  let failure = $state(""), profileFailure = $state("");
  let created = $state("");
  let problem = $derived(nameProblem(name, rooms.map(room => room.name)));

  async function loadProfiles() {
    loading = true;
    profileFailure = "";
    await call("profile.list", {}).then(result => {
      profiles = result.profiles ?? [];
      if (!profiles.some(item => item.name === profile)) profile = [...profiles].sort((a, b) => b.inUse - a.inUse)[0]?.name ?? "";
    }, () => { profileFailure = "Profiles did not load."; });
    loading = false;
  }
  onMount(() => { void loadProfiles(); });

  async function submit() {
    if (busy) return;
    attempted = true;
    if (!created && (problem || !profile || loading)) return;
    busy = true;
    failure = "";
    if (!created) {
      try {
        const result = await call("room.create", { name: name.trim(), rootProfile: profile });
        created = result.room;
      } catch {
        failure = "The room could not be created. Try again.";
        busy = false;
        return;
      }
    }
    if (repo.trim()) {
      try {
        await call("room.repo.add", { room: created, repo: repo.trim(), default: true });
      } catch (error: unknown) {
        failure = `${name} exists, but adding ${repo.trim()} failed: ${error instanceof Error ? error.message : "Try again."}`; 
        busy = false;
        return;
      }
    }
    oncreated(created);
    busy = false;
  }
</script>

<div class="bar">New room</div>
<div class="page">
  <form onsubmit={event => { event.preventDefault(); void submit(); }} onkeydown={event => {
    if ((event.ctrlKey || event.metaKey) && event.key === "Enter") { event.preventDefault(); void submit(); }
  }}>
    <h1>{first ? "Create your first room" : "Create a room"}</h1>
    <p class="intro">Each room is one feed with its own root session. Root starts as soon as the room exists.</p>
    {#if !created}
      <div class="field">
        <label for="room-name">Name</label>
        <input id="room-name" class="mono" bind:value={name} oninput={() => attempted = true} onblur={() => attempted = true} disabled={busy} aria-invalid={attempted && !!problem} aria-describedby="room-name-hint" autocomplete="off" />
        <span id="room-name-hint" class:error={attempted && !!problem}>
          {#if attempted && problem}{problem}{:else if name.trim()}Agents address it as <span class="mono">#{name.trim()}</span>.{:else}Use letters, digits, dots, underscores or hyphens.{/if}
        </span>
      </div>
      {#if loading}<p class="hint">Loading profiles…</p>
      {:else if profileFailure}<p class="error" role="alert">{profileFailure} <button type="button" class="retry" onclick={() => void loadProfiles()}>Retry</button></p>
      {:else if profiles.length}<ProfileSelector {profiles} bind:value={profile} room={true} label="Root profile" />
      {:else}<p class="hint">Add a profile in settings before creating a room.</p>{/if}
    {/if}
    <div class="field">
      <label for="room-repo">Repository <span>· optional</span></label>
      <input id="room-repo" class="mono" bind:value={repo} disabled={busy} placeholder="owner/name" autocomplete="off" />
      <span>Sessions in the room clone it. If adding it fails, the room still exists.</span>
    </div>
    {#if failure}<p class="error" role="alert">{failure}</p>{/if}
    <div class="actions">
      <button class="primary" type="submit" disabled={busy || (!created && (loading || !profile || !!problem))}>{busy ? "Saving…" : created ? "Retry repository" : "Create room"} <kbd>Ctrl Enter</kbd></button>
      {#if created}<button type="button" class="quiet" disabled={busy} onclick={() => oncreated(created)}>Open room</button>
      {:else if !first}<button type="button" class="quiet" disabled={busy} onclick={onclose}>Cancel</button>{/if}
    </div>
  </form>
</div>

<style>
  .bar { height: 52px; display: flex; align-items: center; padding: 0 20px; border-bottom: 1px solid var(--line); font-weight: 600; }
  .page { overflow-y: auto; padding: 32px; }
  form { display: grid; gap: 20px; width: min(640px, 100%); margin: 0 auto; }
  h1 { margin: 0; font-size: 22px; line-height: 1.3; }
  .intro { margin: -12px 0 0; color: var(--muted); font-size: 14px; line-height: 1.5; }
  .field { display: grid; gap: 7px; }
  label { font-size: 14px; font-weight: 600; }
  label span, .field > span, .hint { color: var(--muted); font-size: 13px; font-weight: 400; }
  input { box-sizing: border-box; width: 100%; height: 38px; padding: 0 10px; border: 1px solid var(--firm); border-radius: 6px; background: var(--raised); color: var(--text); font: inherit; }
  .mono { font-family: var(--mono, monospace); font-size: 13px; }
  input:focus-visible, button:focus-visible { outline: 2px solid var(--focus); outline-offset: 2px; }
  .field > .error, .error { margin: 0; color: var(--danger); font-size: 13px; }
  .actions { display: flex; flex-wrap: wrap; gap: 8px; }
  button { min-height: 34px; padding: 0 12px; border: 1px solid transparent; border-radius: 6px; font: inherit; cursor: pointer; }
  button:disabled { opacity: .55; cursor: default; }
  .primary { background: var(--focus); color: var(--bg); font-weight: 600; }
  .quiet { background: transparent; color: var(--muted); }
  .quiet:hover { background: var(--hover); color: var(--text); }
  .retry { min-height: 0; padding: 0; background: none; color: inherit; text-decoration: underline; }
  kbd { margin-left: 7px; font: inherit; font-size: 11px; opacity: .75; }
  @media (pointer: coarse) { kbd { display: none; } }
  @media (max-width: 640px) { .page { padding: 24px 16px; } button { min-height: 44px; } }
</style>
