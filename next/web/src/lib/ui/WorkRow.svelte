<script lang="ts">
  import { untrack } from "svelte";
  import Icon from "./Icon.svelte";
  import StatusPill from "./StatusPill.svelte";
  import StepList from "./StepList.svelte";
  import { count, describe, workSummary } from "../feed.ts";
  import type { Row } from "../feed.ts";
  import type { TurnTranscripts } from "../transcripts.svelte.ts";
  // Folded work reads as one quiet line that starts with a chevron, as T3 Code's "Worked for 20s"; it opens in place.
  // In a DM it opens to the turn's saved steps, which replace the lines that only say a turn started and ended.
  // Other events, and a turn with no saved steps, read as one line per event.
  let { row, name, opened, ontoggle, transcripts }: {
    row: Extract<Row, { kind: "work" }>; name: (session: string) => string; opened: ReadonlySet<string>; ontoggle: (key: string) => void;
    transcripts?: TurnTranscripts;
  } = $props();
  let open = $derived(opened.has(row.key));
  let read = $derived(transcripts?.of(row.events));
  let saved = $derived(typeof read === "object" ? read : undefined);
  let steps = $derived(saved?.steps ?? []);
  let failures = $derived(steps.filter(step => step.failed).length);
  let events = $derived(steps.length || read === "loading" ? row.events.filter(event => !["turn.started", "turn.ended", "turn.activity"].includes(event.type)) : row.events);
  $effect(() => { if (open) { const events = row.events; untrack(() => transcripts?.read(events)); } });
</script>

<div class="work">
  <button type="button" class="fold" aria-expanded={open} onclick={() => ontoggle(row.key)}><span class="chevron" class:open><Icon name="chevron" size={14} /></span><span>{workSummary(row.events, name, row.until, steps[0]?.started)}{#if steps.length}{" · "}{count(steps.length, "step")}{/if}{#if failures}{" · "}<StatusPill tone="failed" label="{failures} failed" word />{/if}</span></button>
  {#if open}<div class="opened">
    {#if read === "loading"}<ul class="waiting" aria-label="Loading steps"><li></li><li></li><li></li></ul>
    {:else if read === "failed"}<p>The steps did not load. <button type="button" class="retry" onclick={() => transcripts?.read(row.events, true)}>Retry</button></p>
    {:else if saved && steps.length}<StepList {steps} truncated={saved.truncated} id={row.key} {opened} {ontoggle} />{/if}
    {#if events.length}<ul>{#each events as event (event.seq)}{@const line = describe(event, name)}{#if line}<li>{line}</li>{/if}{/each}</ul>{/if}
  </div>{/if}
</div>

<style>
  /* The chevron sits left of the post text's edge, so the words line up with the posts above and below. */
  .work { padding: 2px 16px 2px calc(var(--gutter) - 18px); color: var(--muted); font-size: 12.5px; font-variant-numeric: tabular-nums; }
  .fold { all: unset; display: inline-flex; align-items: center; gap: 4px; min-height: 24px; cursor: pointer; border-radius: 6px; }
  .fold:hover, .retry:hover { color: var(--text); }
  .fold:focus-visible, .retry:focus-visible { outline: 2px solid var(--focus); outline-offset: 2px; }
  @media (hover: none) { .fold, .retry { min-height: 40px; } }
  .chevron { display: inline-flex; transform: rotate(-90deg); transition: transform 150ms ease-out; }
  .open { transform: none; }
  .opened { margin: 2px 0 4px 18px; padding: 0 0 0 12px; border-left: 1px solid var(--line); }
  ul { margin: 0; padding: 0; list-style: none; }
  li { padding: 1px 0; }
  p { margin: 0; padding: 2px 0; }
  .retry { all: unset; display: inline-flex; align-items: center; cursor: pointer; border-radius: 6px; color: var(--focus); }
  /* Skeleton rows in the shape of a step; they sit still, since only Working animates. */
  .waiting li { height: 12px; margin: 8px 0; max-width: 320px; border-radius: 6px; background: var(--hover); }
</style>
