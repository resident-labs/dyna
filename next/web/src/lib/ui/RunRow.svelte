<script lang="ts">
  import Icon from "./Icon.svelte";
  import StatusPill from "./StatusPill.svelte";
  import type { WorkflowInfo } from "../contract.gen.ts";
  import { formatTime } from "../feed.ts";
  import { minutesLabel, runCounts, runStatus, runWords } from "../workflow.ts";
  // One workflow run in the sessions panel. It is not an agent, so it has no round face: a solid squared tile whose color is
  // the run's state (green running, red failed, grey done, and a dimmer grey stopped) and whose figure is its step count, then the name in
  // the text color, then one muted line. A running run says its script's status or "3 of 4 steps · 1 running" and shows how
  // long it has run; a past one says how it ended, its counts and its description, and when.
  let { run }: { run: WorkflowInfo } = $props();
  let live = $derived(run.state === "running");
  let line = $derived(runStatus(run));
  let word = $derived(runWords[run.state]);
  let steps = $derived(run.steps ?? 0);
  // A running run's age counts up.
  let now = $state(Date.now());
  $effect(() => {
    if (!live) return;
    now = Date.now();
    const timer = setInterval(() => { now = Date.now(); }, 30_000);
    return () => clearInterval(timer);
  });
</script>

<span class="tile {run.state}" role="img" aria-label={`Workflow, ${word.word.toLowerCase()}`}>{#if steps}{steps > 999 ? "1k+" : steps}{:else}<Icon name="tool" size={14} />{/if}</span>
<span class="who"><span class="name">{run.name}</span>
  <span class="line" class:said={live && line.script}>{#if live}{line.text}{:else}{#if word.tone}<StatusPill tone={word.tone} label={word.word} word />{:else}{word.word}{/if}{#each runCounts(run) as part (part)}{` · ${part}`}{/each}{#if run.status || run.description}{` · ${run.status || run.description}`}{/if}{/if}</span></span>
<span class="when">{live ? minutesLabel(Math.max(0, now - Date.parse(run.started))) : formatTime(run.ended ?? run.started)}</span>

<style>
  /* Squared where a session's face is round, and filled where a face is a tint. Its figure takes the page's color, so it reads on any theme. */
  .tile { flex: none; display: grid; place-items: center; width: 28px; height: 28px; border-radius: 6px; background: var(--muted); color: var(--bg);
    font-size: 12.5px; font-weight: 600; font-variant-numeric: tabular-nums; line-height: 1; }
  .running { background: var(--success); }
  .failed { background: var(--danger); }
  .stopped { opacity: 0.55; }
  .who { flex: 1; min-width: 0; display: grid; line-height: 1.3; }
  .name { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-weight: 600; font-size: 14px; color: var(--text); }
  .line { min-width: 0; color: var(--muted); font-size: 12.5px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; font-variant-numeric: tabular-nums; }
  .line.said { color: var(--text); }
  .line > :global(.status) { opacity: 1; font-weight: 400; }
  .when { flex: none; align-self: start; margin-top: 7px; color: var(--muted); font-size: 11px; font-variant-numeric: tabular-nums; white-space: nowrap; }
</style>
