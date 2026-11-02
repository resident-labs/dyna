<script lang="ts">
  import type { WorkflowInfo, WorkflowStep } from "../contract.gen.ts";
  import { formatTimelineDuration } from "../timeline.ts";
  import { figures, minutesLabel, stagesOf, stageOf, stepTime, stepTitle, stepWord, timeline, tokens, type Bar } from "../workflow.ts";
  import WorkingDot from "./WorkingDot.svelte";

  // The run along time: worker slots as rows, each step a bar in its stage's colour, labelled by its lane or label. The steps
  // a resumed run kept fold into a band on the left, and a live run has a now line. Under it, the steps running now, then where
  // the time went. A bar opens its step. The chart scrolls sideways in its own box when the page is narrow.
  let { info, steps, now, onopen }: { info: WorkflowInfo; steps: WorkflowStep[]; now: number; onopen: (step: WorkflowStep) => void } = $props();
  let chart = $derived(timeline(info, steps, now));
  let stages = $derived(stagesOf(steps));
  let named = $derived(stages.filter(stage => stage.key));
  let stageTitle = $derived.by(() => { const of = stageOf(steps), titles = new Map(stages.map(stage => [stage.key, stage.title])); return (step: WorkflowStep) => titles.get(of.get(step.key) ?? "") ?? ""; });
  let rows = $derived(Array.from({ length: chart.rows }, (_, row) => chart.bars.filter(bar => bar.row === row)));
  let running = $derived(steps.filter(step => step.state === "running" && step.kind !== "workflow" && stepTime(step, now)));
  let spent = $derived(figures(info, steps, now));
  const span = (ms: number) => ms < 60_000 ? formatTimelineDuration(ms) : minutesLabel(ms);
  const thousands = (n: number) => n < 1000 ? String(n) : `${Math.round(n / 1000)}k`;
  const label = (bar: Bar) => `${bar.step.name}${bar.step.profile ? ` · ${bar.step.profile}` : ""}${stepTime(bar.step, now) ? ` · ${stepTime(bar.step, now)}` : ""} · ${stepWord(bar.step).word}`;
</script>

{#snippet block(bar: Bar, thin: boolean)}
  <button type="button" class="bar {bar.step.state}" class:kept={bar.kept} class:thin style:left="{bar.left}%" style:width="max({thin ? 2 : 3}px, calc({bar.width}% - 1px))" style:--colour={bar.colour}
    title={label(bar)} aria-label={`${label(bar)}. Open it`} onclick={() => onopen(bar.step)}>{#if !thin}{stepTitle(bar.step)}{/if}</button>
{/snippet}

<div class="timeline">
  {#if named.length || chart.fold}
    <p class="legend">{#each named as stage (stage.key)}<span><i style:--colour={stage.colour}></i>{stage.title}</span>{/each}{#if chart.fold}<span class="faded">Faded: kept from the run it resumed</span>{/if}</p>
  {/if}
  <div class="scroll">
    <div class="chart" role="group" aria-label="Steps along time, one row per worker slot">
      <div class="row axis"><span class="name"></span><div class="track">{#each chart.ticks as tick (tick.at)}<span class="tick" class:first={tick.at === 0} style:left="{tick.at}%">{tick.label}</span>{/each}</div></div>
      {#each rows as bars, row (row)}
        <div class="row"><span class="name"><span class="wide">Slot</span>{row + 1}</span><div class="track">{#each bars as bar (bar.step.key)}{@render block(bar, false)}{/each}</div></div>
      {/each}
      {#if chart.commands.length}
        <div class="row"><span class="name"><span class="wide">Commands</span><span class="narrow">cmd</span></span><div class="track">{#each chart.commands as bar (bar.step.key)}{@render block(bar, true)}{/each}</div></div>
      {/if}
      <div class="overlay" aria-hidden="true">
        {#if chart.fold}<div class="zone" style:width="{chart.fold}%"></div><span class="resumed" style:left="calc({chart.fold}% + 6px)">{chart.resumed}</span>{/if}
        {#if chart.now !== undefined}<div class="nowline" style:left="{chart.now}%"><span>now</span></div>{/if}
      </div>
    </div>
  </div>

  {#if running.length}
    <ul class="running" aria-label="Running now">
      {#each running as step (step.key)}
        <li><button type="button" onclick={() => onopen(step)}><WorkingDot /><strong>{stepTitle(step)}</strong>
          <span class="muted">{[step.lane ? step.name : "", stageTitle(step), step.profile ?? ""].filter(Boolean).join(" · ")}</span><span class="when">{stepTime(step, now)}</span></button></li>
      {/each}
    </ul>
  {/if}

  <div class="tiles">
    <div class="tile"><span class="k">Worker time</span><span class="v">{span(spent.work)}</span>
      {#if spent.split.length > 1}
        <span class="meter" role="img" aria-label={spent.split.map(part => `${part.title} ${Math.round(part.ms / spent.work * 100)}%`).join(", ")}>{#each spent.split as part (part.key)}<i style:width="{part.ms / spent.work * 100}%" style:--colour={part.colour} title={`${part.title} ${span(part.ms)}`}></i>{/each}</span>
        <span class="s">{spent.split.slice(0, 2).map(part => `${part.title} ${Math.round(part.ms / spent.work * 100)}%`).join(" · ")}</span>
      {:else}<span class="s">across {spent.split.length ? "every step" : "no steps yet"}</span>{/if}</div>
    <div class="tile"><span class="k">Longest step</span>
      {#if spent.longest}<span class="v">{span(spent.longest.ms)}</span><span class="s" title={spent.longest.step.name}>{[stepTitle(spent.longest.step), spent.longest.step.lane ? spent.longest.step.name : "", spent.longest.step.profile ?? ""].filter(Boolean).join(" · ")}</span>
      {:else}<span class="v">None</span>{/if}</div>
    <div class="tile"><span class="k">Wall time</span><span class="v">{span(spent.wall + spent.earlier)}</span>
      <span class="s">{spent.earlier ? `${span(spent.earlier)} before the resume` : info.state === "running" ? "so far" : "start to end"}</span></div>
    <div class="tile"><span class="k">Tokens{spent.earlier ? " since the resume" : ""}</span><span class="v">{tokens(spent.usage) || "0"}</span>
      <span class="s">{thousands(spent.usage.inputTokens)} in · {thousands(spent.usage.outputTokens)} out</span></div>
  </div>
</div>

<style>
  .timeline { display: flex; flex-direction: column; gap: 12px; min-width: 0; }
  .legend { display: flex; flex-wrap: wrap; gap: 4px 12px; margin: 0; color: var(--muted); font-size: 11px; }
  .legend span { display: inline-flex; align-items: center; gap: 5px; }
  .legend i { width: 10px; height: 10px; border: 1px solid var(--colour); border-radius: 3px; background: color-mix(in srgb, var(--colour) 22%, transparent); }
  /* The chart keeps a readable width and scrolls inside its own box, so the page never scrolls sideways. */
  .scroll { overflow-x: auto; overscroll-behavior-x: contain; padding-bottom: 20px; }
  .chart { position: relative; min-width: 600px; --name: 72px; }
  .row { display: grid; grid-template-columns: var(--name) minmax(0, 1fr); align-items: center; height: 26px; }
  .name { position: sticky; left: 0; z-index: 2; align-self: stretch; display: flex; align-items: center; gap: 0.3em; background: var(--bg); color: var(--muted); font-size: 11px; white-space: nowrap; }
  .narrow { display: none; }
  .track { position: relative; height: 20px; }
  .axis { height: 22px; }
  .axis .track { height: 18px; border-bottom: 1px solid var(--line); }
  .tick { position: absolute; bottom: 2px; color: var(--muted); font-size: 11px; font-variant-numeric: tabular-nums; transform: translateX(-50%); white-space: nowrap; }
  .tick.first { transform: none; }
  /* A bar is its stage's colour: a soft fill and a firm edge. Running is the one green thing on the chart. */
  .bar { all: unset; box-sizing: border-box; position: absolute; top: 0; height: 20px; padding: 0 4px; overflow: hidden; border: 1px solid var(--colour); border-left-width: 2px;
    border-radius: 4px; background: color-mix(in srgb, var(--colour) 22%, transparent); color: var(--text); font-size: 11px; line-height: 18px; white-space: nowrap; cursor: pointer; }
  .bar.kept { opacity: 0.5; }
  .bar.running { border-color: var(--success); background: color-mix(in srgb, var(--success) 18%, transparent); }
  .bar.failed { border-color: var(--danger); background: color-mix(in srgb, var(--danger) 18%, transparent); }
  .bar.thin { top: 3px; height: 14px; padding: 0; border: 0; border-radius: 1px; background: var(--colour); }
  .bar.thin.running { background: var(--success); }
  .bar.thin.failed { background: var(--danger); }
  .bar:focus-visible { z-index: 1; outline: 2px solid var(--focus); outline-offset: 1px; }
  .overlay { position: absolute; top: 0; bottom: 0; left: var(--name); right: 0; pointer-events: none; }
  .zone { position: absolute; top: 22px; bottom: 0; left: 0; border-right: 1px dashed var(--firm); background: repeating-linear-gradient(135deg, var(--hover) 0 6px, transparent 6px 12px); }
  .resumed { position: absolute; bottom: -18px; color: var(--muted); font-size: 11px; white-space: nowrap; }
  .nowline { position: absolute; top: 0; bottom: 0; border-left: 1.5px solid var(--success); }
  .nowline span { position: absolute; top: 0; right: 4px; color: var(--success); font-size: 11px; }
  ul { margin: 0; padding: 0; list-style: none; }
  .running li + li { border-top: 1px solid var(--line); }
  .running button { all: unset; box-sizing: border-box; display: flex; align-items: center; gap: 10px; width: 100%; min-height: 36px; padding: 0 4px; border-radius: 6px; font-size: 13px; cursor: pointer; }
  .running strong { flex: none; max-width: 40%; overflow: hidden; font-weight: 600; text-overflow: ellipsis; white-space: nowrap; }
  .muted { min-width: 0; overflow: hidden; color: var(--muted); text-overflow: ellipsis; white-space: nowrap; }
  .when { margin-left: auto; padding-left: 8px; color: var(--muted); font-size: 12.5px; font-variant-numeric: tabular-nums; white-space: nowrap; }
  .running button:focus-visible { outline: 2px solid var(--focus); outline-offset: -2px; }
  .tiles { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 12px; }
  .tile { display: flex; flex-direction: column; min-width: 0; padding: 10px 12px; border: 1px solid var(--line); border-radius: 12px; background: var(--raised); }
  .k, .s { overflow: hidden; color: var(--muted); font-size: 12.5px; text-overflow: ellipsis; white-space: nowrap; }
  .v { font-size: 24px; font-weight: 600; line-height: 1.25; font-variant-numeric: tabular-nums; }
  .meter { display: flex; height: 6px; margin: 6px 0 4px; overflow: hidden; border-radius: 3px; background: var(--track); }
  .meter i { height: 100%; background: var(--colour); }
  @media (hover: hover) { .bar:hover { filter: brightness(1.15); } .running button:hover { background: var(--hover); } }
  @container run (max-width: 600px) {
    .chart { --name: 34px; }
    .wide { display: none; }
    .narrow { display: inline; }
    .tiles { grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 8px; }
    .v { font-size: 20px; }
  }
  @media (hover: none) { .running button { min-height: 44px; } }
</style>
