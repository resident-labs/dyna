<script lang="ts">
  import type { ModelProvider, ProfileSummary, ThinkingLevel } from "../contract.gen.ts";
  let { profile, isNew, busy, error, save, cancel, remove }: { profile: ProfileSummary; isNew: boolean; busy: boolean; error: string; save: (profile: ProfileSummary) => void; cancel: () => void; remove: () => void } = $props();
  let draft = $state({ ...profile, maxCallsPerRun: profile.maxCallsPerRun ?? 0 });
  let compactText = $state((profile.compactTokens ?? 0).toLocaleString(undefined, { numberingSystem: "latn" }));
  const providers: Record<ModelProvider, string> = { anthropic: "Anthropic", openai: "OpenAI-compatible" };
  const ratings = [
    { key: "taste", name: "Taste", help: "choosing the solution" },
    { key: "diagnostics", name: "Diagnostics", help: "finding what is wrong, with few false positives" },
    { key: "intelligence", name: "Intelligence", help: "hard work with many steps" },
    { key: "cost", name: "Thrift", help: "higher is cheaper" },
  ] as const;
  const levels: { value: ThinkingLevel; label: string }[] = [
    { value: "off", label: "Off" }, { value: "minimal", label: "Minimal" }, { value: "low", label: "Low" },
    { value: "medium", label: "Medium" }, { value: "high", label: "High" }, { value: "xhigh", label: "Very high" },
  ];
</script>

<div class="editor-container"><div class="editor">
  {#if isNew}<label class="field"><span>Name</span><input bind:value={draft.name} autocomplete="off" /></label>{/if}
  <label class="field"><span>Provider</span><select bind:value={draft.provider}>{#each Object.entries(providers) as [value, label]}<option {value}>{label}</option>{/each}</select></label>
  <label class="field"><span>Model ID</span><input bind:value={draft.model} autocomplete="off" /></label>
  <label class="field"><span>Thinking</span><select bind:value={draft.thinking}>{#each levels as level}<option value={level.value}>{level.label}</option>{/each}</select></label>
  <div class="field ratings"><span>Ratings</span><div>
    {#each ratings as item}
      <div class="rating">
        <span class="rating-name">{item.name}</span>
        <div class="steps"><button type="button" aria-label={`Clear ${item.name} rating`} onclick={() => draft[item.key] = 0}>–</button>
          {#each Array.from({ length: 10 }, (_, i) => i + 1) as value}
            <button type="button" class:filled={value <= draft[item.key]} aria-label={`${item.name}: ${value}`} aria-pressed={value === draft[item.key]} onclick={() => draft[item.key] = value}
              onkeydown={(event) => { if (event.key === "Backspace" || event.key === "Delete") { event.preventDefault(); draft[item.key] = 0; } else if (event.key === "ArrowRight" || event.key === "ArrowUp") { event.preventDefault(); draft[item.key] = Math.min(10, draft[item.key] + 1); } else if (event.key === "ArrowLeft" || event.key === "ArrowDown") { event.preventDefault(); draft[item.key] = Math.max(0, draft[item.key] - 1); } }}></button>
          {/each}
        </div>
        <span class="rating-value">{draft[item.key] || "Not rated"}</span><span class="help">{item.help}</span>
      </div>
    {/each}
    <p class="help">1 to 10, higher is better. The dash or Backspace clears a rating. Agents read the ratings and the notes when they choose a profile.</p>
  </div></div>
  <label class="field"><span>Notes</span><textarea bind:value={draft.description} rows="3"></textarea></label>
  <label class="field"><span>Calls per run</span><div><input class="numeric" type="number" min="0" bind:value={draft.maxCallsPerRun} /> <span class="help">a run fails past this many calls; 0 means no limit</span></div></label>
  <label class="field"><span>Compact at</span><div><input class="numeric" inputmode="numeric" value={compactText} oninput={(event) => { compactText = event.currentTarget.value; draft.compactTokens = Number(compactText.replace(/[^0-9]/g, "")); }} onblur={() => compactText = (draft.compactTokens ?? 0).toLocaleString(undefined, { numberingSystem: "latn" })} /> <span class="help">tokens of context; 0 means Pi's default</span></div></label>
  {#if error}<p role="alert" class="help">{error}</p>{/if}
  <div class="actions"><button class="btn fill save" type="button" disabled={busy || !draft.name.trim() || !draft.model.trim()} onclick={() => save(draft)}>Save</button><button class="btn" type="button" disabled={busy} onclick={cancel}>Cancel</button>{#if !isNew}{#if profile.inUse > 0}<span class="in-use">In use by {profile.inUse} {profile.inUse === 1 ? "session" : "sessions"}, so it cannot be removed.</span>{:else}<button class="btn danger remove" type="button" disabled={busy} onclick={remove}>Remove</button>{/if}{/if}</div>
</div></div>

<style>
  .editor-container { container-type: inline-size; }
  .editor { padding: 16px 5%; border-top: 1px solid var(--line); display: grid; gap: 12px; }
  .field { display: grid; grid-template-columns: 150px minmax(0, 1fr); gap: 12px; align-items: center; color: var(--muted); }
  input, select, textarea { box-sizing: border-box; width: 100%; max-width: 260px; border: 1px solid var(--line); border-radius: 6px; background: var(--bg); color: var(--text); font: inherit; padding: 8px 10px; }
  textarea { max-width: 440px; resize: none; }
  .numeric { width: 110px; }
  .rating { display: grid; grid-template-columns: 100px 220px 65px minmax(0, 1fr); align-items: center; gap: 10px; color: var(--text); font-size: 12.5px; margin-bottom: 5px; }
  .steps { display: flex; gap: 3px; }
  .steps button { flex: 1; height: 16px; min-width: 0; border: 1px solid var(--line); border-radius: 4px; background: var(--selected); color: var(--muted); padding: 0; cursor: pointer; }
  .steps button:first-child { background: transparent; border: 0; }
  .steps button.filled { background: var(--muted); }
  .help { color: var(--muted); font-size: 12.5px; }
  p.help { margin: 8px 0 0; }
  .actions { display: flex; gap: 12px; }
  .actions button.remove, .in-use { margin-left: auto; }
  .in-use { align-self: center; color: var(--muted); font-size: 12.5px; }
  .actions button:disabled { opacity: .45; }
  @container (max-width: 479px) {
    .numeric + .help { display: block; margin-top: 6px; }
  }
  @container (max-width: 640px) {
    .editor { padding: 16px 12px; }
    .actions { flex-wrap: wrap; }
    .actions button.remove, .in-use { flex-basis: 100%; }
    .field { grid-template-columns: minmax(0, 1fr); gap: 8px; }
    input:not(.numeric), select, textarea { max-width: 100%; }
    .rating { grid-template-columns: minmax(0, 1fr) auto; gap: 8px; margin-bottom: 16px; }
    .rating-name { grid-column: 1; }.rating-value { grid-column: 2; grid-row: 1; }
    .steps { grid-column: 1 / -1; }.steps button { height: 40px; }.help { grid-column: 1 / -1; }
  }
</style>
