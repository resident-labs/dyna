<script lang="ts">
  import { onMount } from "svelte";
  import { call } from "../client.ts";
  import type { ProfileSummary, RoomRepo, SessionStarted } from "../contract.gen.ts";
  import { nameProblem } from "../name-problem.ts";
  import ProfileSelector from "./ProfileSelector.svelte";

  let { room, roomName, sessions, onclose, onstarted }: {
    room: string; roomName: string; sessions: SessionStarted[];
    onclose: () => void; onstarted: (session: string) => void;
  } = $props();

  const clientId = crypto.randomUUID();
  let name = $state(""), parent = $state(sessions.find(item => !item.parent)?.session ?? "");
  let repo = $state(""), profile = $state(""), brief = $state(""), goal = $state("");
  let showGoal = $state(false), busy = $state(false), attempted = $state(false);
  let profiles: ProfileSummary[] = $state([]), repos: RoomRepo[] = $state([]);
  let loaded = $state(false), failure = $state("");
  let problem = $derived(nameProblem(name, sessions.map(item => item.name)));

  onMount(() => {
    void Promise.all([call("profile.list", {}), call("room.repos", { room })]).then(([profileResult, repoResult]) => {
      profiles = profileResult.profiles ?? [];
      repos = repoResult.repos ?? [];
      profile = [...profiles].sort((a, b) => b.inUse - a.inUse)[0]?.name ?? "";
      repo = repos.find(item => item.default)?.repo ?? repos[0]?.repo ?? "";
      loaded = true;
    }, () => { failure = "The form could not load profiles or repositories. Close it and try again."; });
  });

  async function start() {
    if (busy) return;
    attempted = true;
    if (problem || !profile || !loaded) return;
    busy = true;
    failure = "";
    await call("session.start", {
      clientId, room, name: name.trim(), parent: parent || undefined,
      repo: repos.length >= 2 ? repo : undefined, profile,
      brief: brief.trim() || undefined, goal: goal.trim() || undefined,
    }).then(result => onstarted(result.session), (error: unknown) => {
      failure = error instanceof Error ? error.message : "The session could not start. Try again.";
      busy = false;
    });
  }
</script>

<div class="pagebar"><span class="hash">#</span> {roomName} <span class="slash">/</span> New session</div>
<form onsubmit={event => { event.preventDefault(); void start(); }} onkeydown={event => {
  if (event.key === "Enter" && (event.ctrlKey || event.metaKey)) { event.preventDefault(); void start(); }
}}>
  <header><button class="back" type="button" onclick={onclose} disabled={busy}>‹ <span>{roomName}</span></button><h1>New session</h1></header>
  <div class:three={repos.length >= 2} class="fields">
    <label>Name
      <input bind:value={name} oninput={() => attempted = true} onblur={() => attempted = true} autocomplete="off" aria-invalid={attempted && !!problem} aria-describedby={attempted && problem ? "name-problem" : undefined} />
      {#if attempted && problem}<small id="name-problem" class="error">{problem}</small>{/if}
    </label>
    <label>Under
      <select bind:value={parent}>
        {#each sessions as session (session.session)}<option value={session.session}>{session.name}</option>{/each}
      </select>
    </label>
    {#if repos.length >= 2}
      <label>Repository
        <select bind:value={repo}>{#each repos as item (item.repo)}<option value={item.repo}>{item.repo}{item.default ? " · default" : ""}</option>{/each}</select>
      </label>
    {/if}
  </div>
  {#if loaded}<ProfileSelector {profiles} bind:value={profile} />{:else}<p class="muted">Loading profiles…</p>{/if}
  <label>Brief
    <textarea bind:value={brief} rows="6"></textarea>
    <small>Its first message. Markdown works, as in the composer.</small>
  </label>
  {#if showGoal}
    <label>Goal <span class="muted">optional</span><input bind:value={goal} /></label>
  {:else}
    <button type="button" class="add" onclick={() => showGoal = true}>+ &nbsp;Add a goal</button>
  {/if}
  {#if failure}<p class="error" role="alert">{failure}</p>{/if}
  {#if attempted && !profile && loaded}<p class="error" role="alert">Choose a profile.</p>{/if}
  <div class="actions">
    <button class="primary" type="submit" disabled={busy || !loaded || !!problem || !profile}>{busy ? "Starting…" : "Start session"} <kbd>Ctrl Enter</kbd></button>
    {#if problem}<span class="muted">Fix the name to start.</span>{:else if !profile}<span class="muted">Pick a profile to start.</span>{/if}
    <button type="button" onclick={onclose} disabled={busy}>Cancel</button>
  </div>
</form>

<style>
  .pagebar { flex: none; height: 52px; display: flex; align-items: center; gap: 10px; padding: 0 20px; border-bottom: 1px solid var(--line); font-weight: 600; }
  .hash, .slash { color: var(--muted); }
  form { display: grid; align-content: start; flex: 1; min-height: 0; overflow-y: auto; gap: 24px; width: min(100%, 800px); margin: 0 auto; padding: 32px 24px 48px; color: var(--text); }
  header { display: flex; align-items: center; gap: 18px; }
  h1 { margin: 0; font-size: 21px; line-height: 28px; }
  button { border: 0; background: none; color: var(--text); font: inherit; cursor: pointer; }
  button:disabled { opacity: .6; cursor: default; }
  button:focus-visible, input:focus-visible, select:focus-visible, textarea:focus-visible { outline: 2px solid var(--focus); outline-offset: 2px; }
  .back { padding: 0; color: var(--muted); font-size: 22px; }
  .back span { font-size: 13px; }
  .fields { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 16px; }
  .fields.three { grid-template-columns: repeat(3, minmax(0, 1fr)); }
  label { display: grid; align-content: start; gap: 8px; min-width: 0; font-size: 14px; font-weight: 600; }
  input, select, textarea { box-sizing: border-box; width: 100%; min-width: 0; padding: 9px 10px; border: 1px solid var(--line); border-radius: 6px; background: transparent; color: var(--text); font: inherit; font-weight: 400; }
  textarea { resize: vertical; line-height: 1.5; }
  small, .muted { color: var(--muted); font-size: 13px; font-weight: 400; }
  p { margin: 0; }
  .error { color: var(--danger); font-size: 13px; }
  .add { justify-self: start; padding: 0; color: var(--focus); }
  .actions { display: flex; align-items: center; gap: 16px; }
  .actions button { padding: 9px 14px; border-radius: 6px; }
  .actions button:not(.primary):hover { background: var(--hover); }
  .primary { background: var(--focus); color: var(--bg); font-weight: 600; }
  kbd { margin-left: 6px; padding: 1px 4px; border: 1px solid currentColor; border-radius: 3px; font-size: 11px; font-weight: 400; }
  @media (pointer: coarse) { kbd { display: none; } }
  @media (max-width: 640px) {
    form { padding: 20px 16px 32px; gap: 20px; }
    .fields, .fields.three { grid-template-columns: 1fr; }
  }
</style>
