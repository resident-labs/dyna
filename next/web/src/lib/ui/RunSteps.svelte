<script lang="ts">
  import { untrack } from "svelte";
  import { fade, slide } from "svelte/transition";
  import { MediaQuery, SvelteSet } from "svelte/reactivity";
  import type { WorkflowInfo, WorkflowStep } from "../contract.gen.ts";
  import { LONG_PHASE, failedNodes, foldable, openable, phases, runScale, spanReads, stepSpan, stepTime, stepWord, strip, tally, timed, tokenDetail, tokens, type StepNode } from "../workflow.ts";
  import Icon, { type IconName } from "./Icon.svelte";
  import StatusPill from "./StatusPill.svelte";
  import StepDetail from "./StepDetail.svelte";

  // Steps of a run, as a node of its flowchart or a bar of its timeline opens them: the strip says how far they are, each phase
  // folds, a workflow step holds the steps it ran, and a step that is not a worker opens in place. opened names the rows that
  // start open. The rows of a closed crew read the same.
  let { session, info, steps, now, onopen, title = "Steps", opened = [] }: {
    session: string; info: WorkflowInfo; steps: WorkflowStep[]; now: number; onopen: (step: WorkflowStep) => void; title?: string; opened?: string[];
  } = $props();
  const icons: Record<WorkflowStep["kind"], IconName> = { agent: "person", run: "terminal", post: "chat", log: "list", workflow: "tool" };
  const quiet = new MediaQuery("(prefers-reduced-motion: reduce)");
  let motion = $derived({ duration: quiet.current ? 0 : 200 });

  let groups = $derived(phases(steps));
  let counts = $derived(tally(steps));
  let parts = $derived(strip(groups));
  let scale = $derived(runScale(info, now));
  let settled = $derived(counts.done + counts.failed);
  // Once the column shows it stays, so a live run's scale moving on does not pull it away under the reader.
  let timedOnce = false;
  let spans = $derived(timedOnce ||= timed(steps, scale, now));

  // What the owner toggled differs from its default, however long a phase grows.
  const toggled = new SvelteSet<string>(untrack(() => opened));
  const toggle = (key: string) => { if (!toggled.delete(key)) toggled.add(key); };
  // A phase folds or not when it first shows, so one that grows past the limit during a live run stays open under the reader.
  const startsFolded = new Map<string, boolean>();
  function folded(key: string, long: boolean): boolean {
    if (!startsFolded.has(key)) startsFolded.set(key, long);
    return startsFolded.get(key) ?? long;
  }
  let onlyFailed = $state(false);
  let filtering = $derived(onlyFailed && counts.failed > 0);
</script>

{#snippet line(step: WorkflowStep, open: boolean | undefined)}
  {@const state = stepWord(step)}
  {@const span = stepSpan(step, scale, now)}
  {@const time = stepTime(step, now)}
  <span class="fold">{#if open !== undefined}<span class="chevron" class:open><Icon name="chevron" size={14} /></span>{/if}</span>
  <Icon name={icons[step.kind]} size={14} />
  <span class="name" title={step.name}>{step.name}</span>
  {#if spans}<span class="when" aria-hidden="true">{#if time && spanReads(span)}<i class={step.state} style:left="{span.left}%" style:width="{span.width}%"></i>{/if}</span>{/if}
  <span class="meta"><span class="profile" title={step.profile}>{step.profile ?? ""}{#if (info.version ?? 1) > 1 && step.version}<span class="version" title="Started by script v{step.version}"> v{step.version}</span>{/if}</span>
    <span class="figure" title={tokenDetail(step.usage)}>{tokens(step.usage)}</span>
    <span class="figure">{time}</span></span>
  <span class="state">{#key state.word}<span in:fade={motion}>{#if state.tone}<StatusPill tone={state.tone} label={state.word} word />{:else}{state.word}{/if}</span>{/key}</span>
  <span class="go">{#if open === undefined && openable(step)}<Icon name="forwardChevron" size={14} />{/if}</span>
{/snippet}

{#snippet row(node: StepNode, depth: number)}
  {@const step = node.step}
  {@const nest = node.children.length > 0}
  {@const shut = !filtering && nest && node.children.length > LONG_PHASE}
  {@const open = nest ? filtering || shut === toggled.has(step.key) : toggled.has(step.key)}
  {@const folds = nest || foldable(step)}
  <li in:slide={motion} style:--depth={depth}>
    {#if folds}<button type="button" class="step" data-step={step.key} aria-expanded={open} aria-label={`${step.name}, ${stepWord(step).word}`} onclick={() => toggle(step.key)}>{@render line(step, open)}</button>
    {:else if openable(step)}<button type="button" class="step" data-step={step.key} aria-label={`Open ${step.name}'s transcript, ${stepWord(step).word}`} onclick={() => onopen(step)}>{@render line(step, undefined)}</button>
    {:else}<div class="step">{@render line(step, undefined)}</div>{/if}
    {#if open && foldable(step)}<div class="opened" transition:slide={motion}><StepDetail {session} {step} repo={info.repo} /></div>
    {:else if step.reason}<p class="reason" class:whole={filtering || !openable(step)} title={filtering ? undefined : step.reason} in:slide={motion}>{step.reason}</p>{/if}
    {#if nest && open}<ul class="children" transition:slide={motion}>{#each node.children as child (child.step.key)}{@render row(child, depth + 1)}{/each}</ul>{/if}
  </li>
{/snippet}

<section class="steps" class:spans>
  <h3>{title} {#if steps.length > 1}<span class="tally">{#if counts.running || !settled}{settled} of {steps.length}{:else}{steps.length}{/if}{#if counts.running}{` · ${counts.running} running`}{/if}{#if counts.failed}{" · "}<button type="button" class="filter" aria-pressed={filtering} title={filtering ? "Show every step" : "Show only the failed steps"} onclick={() => { onlyFailed = !onlyFailed; }}><StatusPill tone="failed" label={`${counts.failed} failed`} word /></button>{/if}</span>{/if}</h3>
  {#if parts.length && steps.length > 1}
    <div class="strip" role="img" aria-label={`${counts.done} done, ${counts.running} running, ${counts.failed} failed`}>
      {#each parts as part (part.key)}
        <span class="part" style:flex-grow={part.share} title={part.title}>{#each part.segments as segment (segment.state)}<i class={segment.state} style:flex-grow={segment.share}></i>{/each}</span>
      {/each}
    </div>
  {/if}
  {#if !steps.length}<p class="muted">{info.state !== "running" ? "The run ended before its first step." : "The first step will appear here."}</p>{/if}
  {#each groups as phase (phase.key)}
    {@const nodes = filtering ? failedNodes(phase.nodes) : phase.nodes}
    {@const open = filtering || groups.length === 1 || folded(phase.key, phase.long) === toggled.has(`phase ${phase.key}`)}
    {#if nodes.length}<div class="phase">
      {#if phase.title && groups.length > 1}<button type="button" class="phase-head" aria-expanded={open} onclick={() => toggle(`phase ${phase.key}`)}>
        <span class="chevron" class:open><Icon name="chevron" size={14} /></span><strong title={phase.title}>{phase.title}</strong>
        <span class="summary">{phase.done + phase.failed} of {phase.steps.length}{#if phase.running}{` · ${phase.running} running`}{/if}{#if phase.failed}{" · "}<StatusPill tone="failed" label={`${phase.failed} failed`} word />{/if}</span></button>{/if}
      {#if open}<ul transition:slide={motion}>{#each nodes as node (node.step.key)}{@render row(node, 0)}{/each}</ul>{/if}
    </div>{/if}
  {/each}
</section>

<style>
  section { min-width: 0; }
  h3 { margin: 0 0 8px; font-size: 18px; font-weight: 600; line-height: 1.25; }
  .tally { color: var(--muted); font-size: 13px; font-weight: 400; font-variant-numeric: tabular-nums; }
  .filter { padding: 0; border: 0; border-radius: 2px; background: none; color: inherit; font: inherit; cursor: pointer; }
  .filter[aria-pressed="true"] { text-decoration: underline; text-underline-offset: 3px; color: var(--danger); }
  .muted { margin: 0; color: var(--muted); }
  ul { margin: 0; padding: 0; list-style: none; }
  /* The strip is a meter (design-taste: 6 px on the track). A step is at least a sliver, so one failure in 200 still shows. */
  .strip { display: flex; gap: 2px; height: 6px; margin-bottom: 12px; }
  .part { display: flex; flex: 1 1 0; min-width: 3px; overflow: hidden; border-radius: 3px; background: var(--track); transition: flex-grow 200ms ease-out; }
  .part i { flex: 1 1 0; min-width: 3px; background: var(--muted); opacity: 0.55; transition: flex-grow 200ms ease-out; }
  .part .failed { background: var(--danger); opacity: 1; }
  .part .running { background: var(--success); opacity: 1; }
  /* Rows take StepList's height and wash. Fixed columns keep the figures of every row under each other. */
  .phase + .phase { margin-top: 8px; }
  .phase-head { all: unset; box-sizing: border-box; display: flex; align-items: center; gap: 8px; width: calc(100% + 8px); min-height: 28px; margin: 0 -4px; padding: 0 4px; border-radius: 6px; font-size: 13px; cursor: pointer; }
  .phase-head strong { flex: none; max-width: 60%; overflow: hidden; font-weight: 600; text-overflow: ellipsis; white-space: nowrap; }
  .summary { min-width: 0; color: var(--muted); font-size: 12.5px; font-variant-numeric: tabular-nums; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .chevron { display: inline-flex; flex: none; width: 14px; color: var(--muted); transform: rotate(-90deg); transition: transform 150ms ease-out; }
  .chevron.open { transform: none; }
  .spans { --when: 72px; }
  /* A row that opens in place has a phase's chevron, in the same place. The column on the right is for a row that goes elsewhere. */
  .fold { display: inline-flex; width: 14px; }
  /* A child's indent comes out of the name, which is the one column that gives, so the figures stay under the parent's. */
  .step { all: unset; box-sizing: border-box; position: relative; display: grid; grid-template-columns: 14px 14px minmax(0, 1fr) var(--when,) 88px 56px 56px 64px 14px; align-items: center; gap: 8px; width: calc(100% + 8px); min-height: 28px; margin: 0 -4px; padding: 0 4px 0 calc(4px + var(--depth) * 18px); border-radius: 6px; color: var(--muted); font-size: 13px; transition: color 150ms ease-out; }
  button.step { cursor: pointer; }
  /* When a step ran within the run: bars that share a span of this column ran together. */
  .when { position: relative; height: 3px; }
  .when i { position: absolute; top: 0; height: 3px; border-radius: 2px; background: var(--muted); opacity: 0.55; }
  .when .failed { background: var(--danger); opacity: 1; }
  .when .running { background: var(--success); opacity: 1; }
  .name { min-width: 0; overflow: hidden; color: var(--text); text-overflow: ellipsis; white-space: nowrap; }
  [aria-expanded="true"] > .name { overflow-wrap: anywhere; white-space: normal; }
  .meta { display: contents; }
  .profile { min-width: 0; overflow: hidden; font-size: 12.5px; text-overflow: ellipsis; white-space: nowrap; }
  .version { opacity: 0.7; font-variant-numeric: tabular-nums; }
  .figure { font-size: 12.5px; font-variant-numeric: tabular-nums; text-align: right; white-space: nowrap; }
  .state { font-size: 12.5px; text-align: right; white-space: nowrap; }
  .go { display: inline-flex; width: 14px; }
  .reason { margin: 0 0 4px calc(44px + var(--depth) * 18px); overflow: hidden; color: var(--muted); font-size: 12.5px; line-height: 1.45; overflow-wrap: anywhere; text-overflow: ellipsis; white-space: nowrap; }
  .reason.whole { white-space: pre-wrap; }
  .opened { padding-left: calc(44px + var(--depth) * 18px); min-width: 0; }
  /* The hairline runs under the workflow step's icon, as the feed draws opened work. */
  .children { position: relative; }
  .children::before { content: ""; position: absolute; top: 2px; bottom: 6px; left: calc(var(--depth) * 18px + 7px); border-left: 1px solid var(--line); }
  @media (hover: hover) {
    button.step:hover, .phase-head:hover { background: var(--hover); color: var(--text); }
    .filter:hover { text-decoration: underline; text-underline-offset: 3px; }
  }
  button.step:focus-visible, .phase-head:focus-visible { outline: 2px solid var(--focus); outline-offset: -2px; }
  .filter:focus-visible { outline: 2px solid var(--focus); outline-offset: 2px; }
  /* A narrow column keeps the name and the state, and the figures take a second line. */
  @container run (max-width: 600px) {
    .step { grid-template-columns: 14px 14px minmax(0, 1fr) auto 14px; grid-template-areas: "fold icon name state go" ". . meta meta go"; gap: 0 8px; padding-block: 4px; }
    .fold { grid-area: fold; }
    .step > :global(svg) { grid-area: icon; }
    .name { grid-area: name; }
    .when { display: none; }
    .state { grid-area: state; }
    .go { grid-area: go; }
    .meta { grid-area: meta; display: flex; gap: 8px; min-width: 0; }
    .meta > :empty { display: none; }
    .profile { flex: 0 1 auto; }
  }
  /* The filter sits in a header's line, so its touch area grows without moving the line. */
  @media (hover: none) { .step, .phase-head { min-height: 40px; } .filter { margin: -12px -4px; padding: 12px 4px; } }
  @media (prefers-reduced-motion: reduce) { .chevron, .step, .part, .part i { transition: none; } }
</style>
