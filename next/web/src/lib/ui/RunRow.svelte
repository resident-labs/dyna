<script lang="ts">
  import Icon from "./Icon.svelte";
  import StatusPill from "./StatusPill.svelte";
  import type { WorkflowInfo } from "../contract.gen.ts";
  import { count, formatTime } from "../feed.ts";
  import { minutesLabel, runWords } from "../workflow.ts";
  // One workflow run in the sessions panel. It is not an agent, so it has no round face: a solid squared tile whose color is
  // the run's state (green running, red failed, grey done, and an outline for stopped) and whose figure is its step count, then the name in
  // the text color, then one muted line that starts with the state word (see facts). Beside the name, the right edge shows how
  // long a running run has run, or when a past one ended.
  let { run }: { run: WorkflowInfo } = $props();
  let live = $derived(run.state === "running");
  let word = $derived(runWords[run.state]);
  let steps = $derived(run.steps ?? 0);
  // What is unusual comes first, so the cut falls on the description: the failures, then the progress of a run with a step in
  // flight or the steps a past run took, which say what the tile's figure counts, then the script's status. "/" only means progress.
  let facts = $derived([...run.failed ? [`${count(run.failed, "step")} failed`] : [],
    ...live ? run.running ? [`${steps - run.running}/${steps} steps`] : run.steps === 0 ? ["No steps yet"] : [] : run.steps ? [count(steps, "step")] : [],
    ...[run.status || run.description].filter(text => text && text !== "No steps yet")]);
  // A running run's age counts up.
  let now = $state(Date.now());
  $effect(() => {
    if (!live) return;
    now = Date.now();
    const timer = setInterval(() => { now = Date.now(); }, 30_000);
    return () => clearInterval(timer);
  });
  // A past run's stamp is the clock today and only the date before, so an old row keeps its room for the line.
  let at = $derived(run.ended ?? run.started);
  let stamp = $derived(live ? minutesLabel(Math.max(0, now - Date.parse(run.started))) : new Date(at).toDateString() === new Date().toDateString() ? formatTime(at) : new Date(at).toLocaleDateString([], { month: "short", day: "numeric" }));
</script>

<span class="tile {run.state}" class:hurt={run.failed > 0 && run.state !== "failed"} role="img" aria-label={`Workflow, ${word.word.toLowerCase()}${run.steps ? `, ${count(steps, "step")}` : ""}${run.failed ? `, ${run.failed} failed` : ""}`} title={run.steps ? count(steps, "step") : undefined}>{#if steps}{steps > 999 ? "1k+" : steps}{:else}<Icon name="tool" size={14} />{/if}</span>
<span class="who"><span class="name">{run.name}</span><span class="when" title={`${live ? "Running for" : word.word} ${live ? stamp : formatTime(at)}`}>{stamp}</span>
  <span class="line">{#if word.tone}<StatusPill tone={word.tone} label={word.word} word />{:else}{word.word}{/if}{#each facts as part}{` · ${part}`}{/each}</span></span>

<style>
  /* Squared where a session's face is round, and filled where a face is a tint. Its figure takes the page's color, so it reads on any theme. */
  .tile { flex: none; display: grid; place-items: center; width: 28px; height: 28px; border-radius: 6px; background: var(--muted); color: var(--bg);
    font-size: 12.5px; font-weight: 600; font-variant-numeric: tabular-nums; line-height: 1; }
  .running { background: var(--success); }
  .failed { background: var(--danger); }
  .stopped { background: none; box-shadow: inset 0 0 0 1.5px var(--muted); color: var(--muted); }
  /* A running, done or stopped run with a failed step keeps its tile and a red dot on the corner, ringed in the panel's color. */
  .hurt { position: relative; }
  .hurt::after { content: ""; position: absolute; top: -3px; right: -3px; width: 9px; height: 9px; border-radius: 50%; background: var(--danger); box-shadow: 0 0 0 2px var(--sidebar); }
  /* The stamp sits on the name's row, so the line below keeps the whole width. */
  .who { flex: 1; min-width: 0; display: grid; grid-template-columns: minmax(0, 1fr) auto; column-gap: 8px; line-height: 1.3; }
  .name { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-weight: 600; font-size: 14px; color: var(--text); }
  .line { grid-column: 1 / -1; min-width: 0; color: var(--muted); font-size: 12.5px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; font-variant-numeric: tabular-nums; }
  .line > :global(.status) { opacity: 1; font-weight: 400; }
  .when { align-self: center; color: var(--muted); font-size: 11px; font-variant-numeric: tabular-nums; white-space: nowrap; }
</style>
