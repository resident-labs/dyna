<script lang="ts">
  import { onMount } from "svelte";
  import type { ThinkingLevel } from "../contract.gen.ts";
  import { countField, providers, ratingKey, replacements, sessionCount, type ProfileModel, type ProfileSummary } from "../profiles.ts";
  import Modal from "./Modal.svelte";
  // A profile's detail on Settings > Profiles (spec 5.13): its fields with each explanation once, Save, and Remove, which
  // first asks where the sessions of a profile in use go. note is what the last save said, until the next edit.
  let { profile, profiles, models, isNew, busy, error, note, save, remove }: {
    profile: ProfileSummary; profiles: ProfileSummary[]; models: ProfileModel[]; isNew: boolean; busy: boolean; error: string; note: string;
    save: (profile: ProfileSummary) => void; remove: (replacement?: string) => void;
  } = $props();
  // svelte-ignore state_referenced_locally: the parent keys the editor by profile, so a draft starts once per profile.
  let draft = $state({ ...profile });
  let edited = $state(false);
  // Removing a profile in use first asks where its sessions go.
  let moving = $state(false), target = $state("");
  let others = $derived(replacements(profiles, profile.name));
  function askToRemove() {
    if (profile.inUse === 0) { remove(); return; }
    target = others[0]?.name ?? "";
    moving = true;
  }
  const grouped = (n: number | undefined) => n ? n.toLocaleString("en") : "";
  // svelte-ignore state_referenced_locally
  let compactText = $state(grouped(profile.compactTokens)), limitText = $state(grouped(profile.maxConcurrent));
  const ratings = [
    { key: "taste", name: "Taste", help: "Choosing the solution." },
    { key: "diagnostics", name: "Diagnostics", help: "Finding what is wrong, with few false positives." },
    { key: "intelligence", name: "Intelligence", help: "Hard work with many steps." },
    { key: "cost", name: "Thrift", help: "Higher is cheaper." },
  ] as const;
  const levels: { value: ThinkingLevel; label: string }[] = [
    { value: "off", label: "Off" }, { value: "minimal", label: "Minimal" }, { value: "low", label: "Low" },
    { value: "medium", label: "Medium" }, { value: "high", label: "High" }, { value: "xhigh", label: "Very high" },
  ];
  type Rated = (typeof ratings)[number]["key"];
  // A rating is one slider (ratingKey). The pointer previews the fill up to the step under it.
  let hover: { key: Rated; value: number } | undefined = $state();
  function rate(event: KeyboardEvent, key: Rated) {
    const next = ratingKey(event.key, draft[key]);
    if (next === undefined) return;
    event.preventDefault();
    draft[key] = next;
    edited = true;
  }
  const models_id = $props.id();
  // A new profile starts at its name.
  let nameInput: HTMLInputElement | undefined = $state();
  onMount(() => nameInput?.focus());
</script>

<!-- svelte-ignore a11y_no_static_element_interactions: any edit hides what the last save said. -->
<div class="editor" oninput={() => { edited = true; }}>
  {#if isNew}<label class="field"><span class="label">Name</span><input bind:this={nameInput} bind:value={draft.name} autocomplete="off" spellcheck="false" /></label>
  {:else}<p class="small muted">{profile.inUse ? `${sessionCount(profile.inUse)} ${profile.inUse === 1 ? "uses" : "use"} it.` : "No session uses it."}</p>{/if}
  <label class="field"><span class="label">Model</span><input class="mono" list={`${models_id}-models`} bind:value={draft.model} autocomplete="off" spellcheck="false"
    oninput={() => { const known = models.find(model => model.id === draft.model.trim()); if (known) draft.provider = known.provider; }} /></label>
  <datalist id={`${models_id}-models`}>{#each models as model (model.id)}<option value={model.id}></option>{/each}</datalist>
  <label class="field"><span class="label">Provider</span><select bind:value={draft.provider}>{#each Object.entries(providers) as [value, label] (value)}<option {value}>{label}</option>{/each}</select></label>
  <label class="field"><span class="label">Thinking</span><select bind:value={draft.thinking}>{#each levels as level (level.value)}<option value={level.value}>{level.label}</option>{/each}</select></label>
  <div class="field"><span class="label">Ratings</span><p class="small muted">1 to 10, higher is better. Agents read the ratings and the notes when they choose a profile.</p>
    {#each ratings as item (item.key)}
      <div class="rating">
        <span class="rating-name">{item.name}</span>
        <div class="steps" role="slider" onpointerleave={() => { hover = undefined; }} tabindex="0" aria-label={item.name} aria-valuemin={0} aria-valuemax={10} aria-valuenow={draft[item.key]}
          aria-valuetext={draft[item.key] ? String(draft[item.key]) : "Not rated"} onkeydown={event => rate(event, item.key)}>
          {#each Array.from({ length: 10 }, (_, i) => i + 1) as value (value)}
            <!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions: the slider takes the keys. -->
            <span class="step" class:on={value <= draft[item.key]} class:preview={hover?.key === item.key && value <= hover.value}
              onpointerenter={event => { if (event.pointerType === "mouse") hover = { key: item.key, value }; }} title={value === draft[item.key] ? "Clear" : String(value)}
              onclick={() => { draft[item.key] = value === draft[item.key] ? 0 : value; edited = true; }}></span>
          {/each}
        </div>
        <span class="rating-value">{draft[item.key] || "–"}</span>
        <span class="help">{item.help}</span>
      </div>
    {/each}
  </div>
  <label class="field"><span class="label">Notes</span><textarea bind:value={draft.description} rows="3"></textarea></label>
  <label class="field"><span class="label">At most at once</span><input class="short" inputmode="numeric" placeholder="No limit" value={limitText}
    oninput={event => { limitText = event.currentTarget.value; draft.maxConcurrent = countField(limitText); }} onblur={() => { limitText = grouped(draft.maxConcurrent); }} />
    <span class="help">Workflow workers on this profile at the same time. Empty is no limit.</span></label>
  <label class="field switch"><span class="label">Images</span><input type="checkbox" role="switch" checked={draft.images ?? true} onchange={event => { draft.images = event.currentTarget.checked; }} />
    <span class="help">Whether the model takes images. Off sends it text only.</span></label>
  <label class="field"><span class="label">Compact at</span><input class="short" inputmode="numeric" placeholder="Pi's default" value={compactText}
    oninput={event => { compactText = event.currentTarget.value; draft.compactTokens = countField(compactText); }} onblur={() => { compactText = grouped(draft.compactTokens); }} />
    <span class="help">Tokens of context before the session compacts.</span></label>
  {#if error}<p role="alert" class="problem">{error}</p>{/if}
  <div class="actions"><button class="btn fill" type="button" disabled={busy || !draft.name.trim() || !draft.model.trim()} onclick={() => { edited = false; save(draft); }}>{isNew ? "Create" : "Save"}</button>
    <span class="small muted" role="status">{edited ? "" : note}</span></div>
  {#if !isNew}<div class="removal">{#if profile.inUse}<span class="small muted">Its sessions move to a profile you pick.</span>{/if}
    <button class="btn danger" type="button" aria-haspopup={profile.inUse ? "dialog" : undefined} disabled={busy} onclick={askToRemove}>Remove</button></div>{/if}
</div>
{#if moving}
  <Modal title={`Remove ${profile.name}?`} fit {busy} initial="[data-cancel]" onclose={() => { moving = false; }}>
    {#if others.length}
      <label class="move"><span>Move {sessionCount(profile.inUse)} to</span><select bind:value={target}>{#each others as other (other.name)}<option value={other.name}>{other.label}</option>{/each}</select></label>
    {:else}<p>{sessionCount(profile.inUse)} use this profile, and there is no other profile to move them to.</p>{/if}
    {#if error}<p role="alert" class="problem">{error}</p>{/if}
    {#snippet actions()}
      <button class="btn" type="button" data-cancel disabled={busy} onclick={() => { moving = false; }}>Keep profile</button>
      {#if others.length}<button class="btn danger" type="button" disabled={busy || !target} onclick={() => remove(target)}>Move and remove</button>{/if}
    {/snippet}
  </Modal>
{/if}

<style>
  .editor { display: grid; gap: 18px; }
  .field { display: grid; gap: 6px; min-width: 0; }
  .label { color: var(--text); font-size: 14px; font-weight: 600; }
  .field.switch { grid-template-columns: minmax(0, 1fr) auto; align-items: center; }
  .field.switch .help { grid-column: 1 / -1; }
  input:not([role="switch"]), select, textarea { box-sizing: border-box; width: 100%; border: 1px solid var(--line); border-radius: 6px; background: var(--bg); color: var(--text); font: inherit; font-size: 14px; padding: 7px 10px; }
  input:focus, select:focus, textarea:focus { outline: none; border-color: var(--focus); }
  input.mono { font-family: var(--mono); font-size: 13px; }
  .short { max-width: 160px; }
  textarea { resize: vertical; }
  .help { color: var(--muted); font-size: 12.5px; line-height: 1.35; }
  .rating { display: grid; grid-template-columns: 96px minmax(0, 1fr) 24px; gap: 2px 10px; align-items: center; font-size: 13.5px; }
  .rating > .help { grid-column: 1 / -1; }
  .steps { display: flex; gap: 3px; padding: 0 2px; border-radius: 6px; }
  .steps:focus-visible { outline: 2px solid var(--focus); outline-offset: 1px; }
  /* The step is the hit area, taller than the bar it draws. */
  .step { flex: 1; padding: 3px 0; cursor: pointer; }
  .step::after { content: ""; display: block; height: 16px; box-sizing: border-box; border: 1px solid var(--line); border-radius: 4px; background: var(--selected); }
  .step.preview::after { background: color-mix(in srgb, var(--focus) 45%, var(--selected)); border-color: color-mix(in srgb, var(--focus) 45%, var(--line)); }
  .step.on::after { background: var(--focus); border-color: var(--focus); }
  .rating-value { color: var(--muted); text-align: right; font-variant-numeric: tabular-nums; }
  .actions { display: flex; align-items: center; gap: 12px; }
  .removal { display: flex; align-items: center; gap: 12px; padding-top: 14px; border-top: 1px solid var(--line); }
  .removal button { margin-left: auto; }
  .problem { margin: 0; color: var(--danger); font-size: 13px; font-weight: 500; }
  .move { display: grid; gap: 8px; color: var(--muted); }
  .move select { max-width: 100%; }
  p { margin: 0; }
  @media (hover: none) { .step { padding: 8px 0; } .step::after { height: 22px; } }
</style>
