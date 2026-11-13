<script lang="ts">
  import Icon from "./Icon.svelte";
  import StatusPill from "./StatusPill.svelte";
  import type { WorkflowInfo } from "../contract.gen.ts";
  import { count, formatTime } from "../feed.ts";
  import { minutesLabel, runWords } from "../workflow.ts";
  // One workflow run in the sessions panel. It is not an agent, so it has no round face: a solid squared tile whose color is
  // the run's state (green running, red failed, grey done, and a dimmer grey stopped) and whose figure is its step count, then the name in
  // the text color, then one muted line that starts with the state word and says what is unusual: how far a running run has got
  // and what failed, then its script's status or description. The tile holds the step count, so the line does not repeat it
  // except for the failed run's share. The right edge shows how long a running run has run, or when a past one ended.
  let { run }: { run: WorkflowInfo } = $props();
  let live = $derived(run.state === "running");
  let word = $derived(runWords[run.state]);
  let steps = $derived(run.steps ?? 0);
  let facts = $derived([...live ? [steps ? `${steps - (run.running ?? 0)} of ${steps}` : "No steps yet"] : run.state === "failed" && run.failed && steps ? [`${run.failed} of ${steps} steps`] : [],
    ...run.failed && (live || run.state !== "failed") ? [`${run.failed} failed`] : [], ...run.status || run.description ? [run.status || run.description] : []]);
  // A running run's age counts up.
  let now = $state(Date.now());
  $effect(() => {
    if (!live) return;
    now = Date.now();
    const timer = setInterval(() => { now = Date.now(); }, 30_000);
    return () => clearInterval(timer);
  });
  let stamp = $derived(live ? minutesLabel(Math.max(0, now - Date.parse(run.started))) : formatTime(run.ended ?? run.started));
</script>

<span class="tile {run.state}" class:hurt={live && run.failed > 0} role="img" aria-label={`Workflow, ${word.word.toLowerCase()}, ${count(steps, "step")}${run.failed ? `, ${run.failed} failed` : ""}`} title={count(steps, "step")}>{#if steps}{steps > 999 ? "1k+" : steps}{:else}<Icon name="tool" size={14} />{/if}</span>
<span class="who"><span class="name">{run.name}</span>
  <span class="line">{#if word.tone}<StatusPill tone={word.tone} label={word.word} word />{:else}{word.word}{/if}{#each facts as part (part)}{` · ${part}`}{/each}</span></span>
<span class="when" title={`${live ? "Running for" : word.word} ${stamp}`}>{stamp}</span>

<style>
  /* Squared where a session's face is round, and filled where a face is a tint. Its figure takes the page's color, so it reads on any theme. */
  .tile { flex: none; display: grid; place-items: center; width: 28px; height: 28px; border-radius: 6px; background: var(--muted); color: var(--bg);
    font-size: 12.5px; font-weight: 600; font-variant-numeric: tabular-nums; line-height: 1; }
  .running { background: var(--success); }
  .failed { background: var(--danger); }
  .stopped { opacity: 0.55; }
  /* A running run with a failed step keeps its green tile and a red dot on the corner, ringed in the panel's color. */
  .hurt { position: relative; }
  .hurt::after { content: ""; position: absolute; top: -3px; right: -3px; width: 9px; height: 9px; border-radius: 50%; background: var(--danger); box-shadow: 0 0 0 2px var(--sidebar); }
  .who { flex: 1; min-width: 0; display: grid; line-height: 1.3; }
  .name { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-weight: 600; font-size: 14px; color: var(--text); }
  .line { min-width: 0; color: var(--muted); font-size: 12.5px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; font-variant-numeric: tabular-nums; }
  .line > :global(.status) { opacity: 1; font-weight: 400; }
  .when { flex: none; align-self: start; margin-top: 7px; color: var(--muted); font-size: 11px; font-variant-numeric: tabular-nums; white-space: nowrap; }
</style>
