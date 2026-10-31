<script lang="ts">
  import { untrack } from "svelte";
  import type { WorkTurn } from "../session-work.ts";
  import type { TurnTranscripts } from "../transcripts.svelte.ts";
  import { foldOpen, workStepSummary } from "../work-steps.ts";
  import Icon from "./Icon.svelte";
  import StepList from "./StepList.svelte";

  let { turn, transcripts, opened, ontoggle, activity, answered }: {
    turn: WorkTurn; transcripts: TurnTranscripts; opened: ReadonlySet<string>;
    ontoggle: (key: string) => void; activity?: string; answered: boolean;
  } = $props();
  let id = $derived(turn.started.body.turn);
  let events = $derived(turn.ended ? [turn.started, turn.ended] : [turn.started]);
  let open = $derived(foldOpen(turn.ended?.body.outcome === "failed", opened.has(id)));
  let read = $derived(transcripts.of(events));
  let saved = $derived(typeof read === "object" ? read : undefined);
  $effect(() => {
    if (turn.ended && open) {
      const current = events;
      untrack(() => transcripts.read(current));
    }
  });
</script>

{#if turn.ended}
  <div class="work-steps">
    <button type="button" class="fold" aria-expanded={open} onclick={() => ontoggle(id)}>
      <span class="chevron" class:open><Icon name="chevron" size={14} /></span>
      <span>{workStepSummary(turn.started.at, turn.ended.at, saved?.steps.length)}{answered ? " · answered you in Messages" : ""}</span>
    </button>
    {#if open}
      <div class="steps">
        {#if read === "loading" || !read}<p>Loading steps…</p>
        {:else if read === "failed"}<p>The steps did not load. <button type="button" class="retry" onclick={() => transcripts.read(events, true)}>Try again</button></p>
        {:else if read.steps.length}<StepList steps={read.steps} truncated={read.truncated} {id} {opened} {ontoggle} />
        {:else}<p>No steps were recorded.</p>{/if}
      </div>
    {/if}
  </div>
{:else}<p class="live">Working{activity ? ` · ${activity}` : "…"}</p>{/if}

<style>
  .work-steps { margin: 6px 0 0 64px; color: var(--muted); font-size: 12.5px; }
  button { border: 0; background: none; color: inherit; font: inherit; cursor: pointer; }
  button:hover { color: var(--text); }
  button:focus-visible { outline: 2px solid var(--focus); outline-offset: 2px; }
  .fold { display: inline-flex; align-items: center; gap: 6px; min-height: 28px; padding: 0; }
  .chevron { display: inline-flex; transform: rotate(-90deg); }
  .chevron.open { transform: none; }
  .steps { margin: 3px 0 6px 6px; padding-left: 16px; border-left: 1px solid var(--line); }
  .steps p, .live { margin: 0; }
  .live { margin: 8px 0 0 64px; color: var(--muted); font-size: 12.5px; }
  .retry { color: var(--focus); }
  @media (max-width: 600px) { .work-steps, .live { margin-left: 0; } }
  @media (pointer: coarse) { .fold, .retry { min-height: 40px; } }
</style>
