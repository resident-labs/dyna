<script lang="ts">
  import { onMount, tick, untrack } from "svelte";
  import { SvelteSet } from "svelte/reactivity";
  import type { RoomStore } from "../room.svelte.ts";
  import type { WorkflowStep } from "../contract.gen.ts";
  import { RunState } from "../run.svelte.ts";
  import { boxReason, count } from "../feed.ts";
  import { formatTimelineDuration } from "../timeline.ts";
  import { branchLink, commitLink, failures, openable, phases, resultBlock, runWords, runningFor, shortCommit, shownStep, stepTime, stepWord, tokenDetail, tokens, total } from "../workflow.ts";
  import CollapsibleBody from "./CollapsibleBody.svelte";
  import Icon, { type IconName } from "./Icon.svelte";
  import Markdown from "./Markdown.svelte";
  import SessionThread from "./SessionThread.svelte";
  import StatusPill from "./StatusPill.svelte";

  // A crew's page: what the run does while it runs and what it left once it ends, all from the journal, so a closed crew reads the
  // same. A worker opens in place, in the session thread, by its step.
  let { store, session, roomName, operate, navigate, onpostlocation, clock = Date.now }: {
    store: RoomStore; session: string; roomName: string; operate: boolean;
    navigate: (event: MouseEvent & { currentTarget: HTMLAnchorElement }) => void; onpostlocation: (room: string, seq: number) => void; clock?: () => number;
  } = $props();
  const run = untrack(() => new RunState(session));
  onMount(() => { const stop = store.watchRun(session, run.apply); void run.read(); return stop; });
  let { status, steps, failed } = $derived(run.snapshot);
  let info = $derived(status?.run);
  let merge = $derived(status?.merge);
  let groups = $derived(phases(steps));
  let lost = $derived(failures(steps));
  let conflicts = $derived((merge?.conflicts ?? []).filter(conflict => !conflict.reason));
  let result = $derived(info?.result ? resultBlock(info.result) : undefined);
  let ended = $derived(!!info && info.state !== "running");
  let broken = $derived(store.boxFailures.get(session));
  let words = $derived(runWords[info?.state ?? "running"]);
  let usage = $derived(total(steps));
  let finished = $derived(steps.filter(step => step.state !== "running").length);
  let stuck = $derived(info?.state === "running" && !!broken);

  // Everything that counts up shares one second, which stops with the run.
  let now = $state(untrack(() => clock()));
  $effect(() => {
    if (ended) return;
    now = clock();
    const timer = setInterval(() => { now = clock(); }, 1000);
    return () => clearInterval(timer);
  });

  const icons: Record<WorkflowStep["kind"], IconName> = { agent: "person", run: "terminal", post: "chat", log: "list", workflow: "tool" };
  // A phase the owner toggled differs from its default, however long it grows.
  const toggled = new SvelteSet<string>();
  const failedShown = 20;
  let allFailed = $state(false);

  let frame: HTMLElement | undefined = $state();
  let shown = $state(shownStep(location.search));
  let pushed = false;
  let worker = $derived(shown ? steps.find(step => step.key === shown) : undefined);
  async function show(step: WorkflowStep) {
    history.pushState(history.state, "", `${location.pathname}?step=${encodeURIComponent(step.key)}`);
    pushed = true;
    shown = step.key;
    await tick();
    frame?.querySelector<HTMLElement>(".back")?.focus();
  }
  function hide() {
    const key = shown;
    if (pushed) history.back();
    else history.replaceState(history.state, "", location.pathname);
    pushed = false;
    shown = undefined;
    void tick().then(() => frame?.querySelector<HTMLElement>(`[data-step="${CSS.escape(key ?? "")}"]`)?.focus());
  }
  function closeWorker(event: KeyboardEvent) {
    if (event.key === "Escape" && worker && !event.defaultPrevented) hide();
  }
</script>

<svelte:window onpopstate={() => { shown = shownStep(location.search); pushed = false; }} />

{#snippet line(step: WorkflowStep)}
  {@const state = stepWord(step)}
  <Icon name={icons[step.kind]} size={14} />
  <span class="name" title={step.name}>{step.name}</span>
  <span class="meta"><span class="profile" title={step.profile}>{step.profile ?? ""}</span>
    <span class="figure" title={tokenDetail(step.usage)}>{tokens(step.usage)}</span>
    <span class="figure">{stepTime(step, now)}</span></span>
  <span class="state">{#if state.tone}<StatusPill tone={state.tone} label={state.word} word />{:else}{state.word}{/if}</span>
  <span class="go">{#if openable(step)}<Icon name="forwardChevron" size={14} />{/if}</span>
{/snippet}

{#snippet row(step: WorkflowStep, whole: boolean)}
  <li class:failed={step.state === "failed"}>
    {#if openable(step)}<button type="button" class="step" data-step={step.key} aria-label={`Open ${step.name}'s transcript, ${stepWord(step).word}`} onclick={() => void show(step)}>{@render line(step)}</button>
    {:else}<div class="step">{@render line(step)}</div>{/if}
    {#if step.reason}<p class="reason" class:whole title={whole ? undefined : step.reason}>{step.reason}</p>{/if}
  </li>
{/snippet}

{#snippet commit(sha: string)}
  {@const link = commitLink(info?.repo, sha)}
  {#if link}<a class="code" href={link} target="_blank" rel="noopener noreferrer">{shortCommit(sha)}</a>{:else}<code>{shortCommit(sha)}</code>{/if}
{/snippet}

{#snippet branch(name: string)}
  {@const link = branchLink(info?.repo, name)}
  {#if link}<a class="code" href={link} target="_blank" rel="noopener noreferrer">{name}</a>{:else}<code>{name}</code>{/if}
{/snippet}

<div class="frame" bind:this={frame} role="presentation" onkeydown={closeWorker}>
  {#if worker}
    {@const state = stepWord(worker)}
    <div class="worker-bar">
      <button type="button" class="back" aria-label={`Back to ${info?.name ?? "the run"}`} title="Back to the run" onclick={hide}><Icon name="back" size={16} /></button>
      <strong title={worker.name}>{worker.name}</strong>
      {#if worker.profile}<span class="muted">{worker.profile}</span>{/if}
      <span class="figure">{stepTime(worker, now)}</span>
      {#if state.tone}<StatusPill tone={state.tone} label={state.word} word />{:else}<span class="muted">{state.word}</span>{/if}
    </div>
    {#if worker.reason}<div class="failure worker-failure"><span class="slot"><Icon name="warning" size={16} /></span><span>{worker.reason}</span></div>{/if}
    <div class="thread">{#key worker.key}<SessionThread {store} {session} {roomName} {navigate} {onpostlocation} {operate} step={worker.key} unsent={[]} onunsent={() => {}} onsettled={() => {}} />{/key}</div>
  {:else}
    <div class="run"><div class="body">
      {#if !info && failed}<p class="muted" role="alert">The run did not load. <button type="button" class="retry" onclick={() => void run.read()}>Try again</button></p>
      {:else if !info}<div class="skeleton" aria-hidden="true"><i></i><i></i><i></i></div>
      {:else}
        <header>
          <div class="heading">
            <h2>{info.name}</h2>
            {#if words.tone}<StatusPill tone={words.tone} label={words.word} />{:else}<span class="muted">{words.word}</span>{/if}
            {#if info.state === "running" && operate && store.running(session)}
              <button type="button" class="stop" disabled={store.stopping.has(session)} onclick={() => store.stop(session)}><Icon name="stop" size={14} />{store.stopping.has(session) ? "Stopping" : "Stop"}</button>
            {/if}
          </div>
          {#if info.description}<p class="muted">{info.description}</p>{/if}
          <p class="facts"><span>{formatTimelineDuration(runningFor(info.started, info.ended, now))}</span>
            {#if steps.length}<span>{finished} of {count(steps.length, "step")}</span>{/if}
            {#if lost.length}<span><StatusPill tone="failed" label={`${lost.length} failed`} word /></span>{/if}
            {#if tokens(usage)}<span title={tokenDetail(usage)}>{tokens(usage)} tokens</span>{/if}
            {#if info.commit}<span class="from">{info.repo ?? ""} at {@render commit(info.commit)}</span>{/if}</p>
          {#if store.stopFailed.has(session)}<p class="trouble" role="alert">The stop did not reach the engine. Try again.</p>{/if}
          {#if stuck && broken}<div class="failure" role="status"><span class="slot"><Icon name="warning" size={16} /></span><span>The box failed: {boxReason(broken)}. The run waits until the session restarts.</span></div>{/if}
        </header>

        {#if info.state === "failed"}
          <section aria-label="Why the run failed"><div class="failure"><span class="slot"><Icon name="warning" size={16} /></span>
            <div class="why">{#if info.result}<CollapsibleBody><p class="text">{info.result}</p></CollapsibleBody>{:else}<p class="text">The script failed and left no reason.</p>{/if}</div></div></section>
        {:else if result}
          <section><h3>Result</h3>
            {#if result.code}<pre>{result.text}</pre>{:else}<CollapsibleBody><Markdown rhythm="thread" text={result.text} /></CollapsibleBody>{/if}</section>
        {/if}

        {#if ended && lost.length}
          <section><h3>Failed steps <span class="count">{lost.length}</span></h3>
            <ul>{#each lost.slice(0, allFailed ? undefined : failedShown) as step (step.key)}{@render row(step, true)}{/each}</ul>
            {#if lost.length > failedShown}<button type="button" class="more" aria-expanded={allFailed} onclick={() => { allFailed = !allFailed; }}>{allFailed ? `Show the first ${failedShown}` : `Show ${lost.length - failedShown} more`}</button>{/if}</section>
        {/if}

        {#if merge}
          <section><h3>Merge</h3>
            <dl>
              <dt>Branch</dt><dd>{@render branch(merge.branch)}</dd>
              <dt>Commit</dt><dd>{@render commit(merge.commit)}</dd>
              {#if merge.merged?.length}<dt>Merged</dt><dd>{merge.merged.join(", ")}</dd>{/if}
            </dl>
            {#if conflicts.length}
              <h4>Conflicts <span class="count">{conflicts.length}</span></h4>
              <ul class="conflicts">{#each conflicts as conflict (conflict.step)}
                <li><span class="conflict"><StatusPill tone="failed" label={conflict.step} word />{#if conflict.branch}{@render branch(conflict.branch)}{/if}</span>
                  {#each conflict.files ?? [] as file (file)}<code class="file">{file}</code>{/each}</li>
              {/each}</ul>
            {/if}</section>
        {/if}

        <section><h3>Steps {#if steps.length}<span class="count">{steps.length}</span>{/if}</h3>
          {#if !steps.length}<p class="muted">{ended ? "The run ended before its first step." : "The first step will appear here."}</p>{/if}
          {#each groups as phase (phase.key)}
            {@const open = phase.long === toggled.has(phase.key)}
            <div class="phase">
              {#if phase.title}<button type="button" class="phase-head" aria-expanded={open} onclick={() => { if (!toggled.delete(phase.key)) toggled.add(phase.key); }}>
                <span class="chevron" class:open><Icon name="chevron" size={14} /></span><strong>{phase.title}</strong>
                <span class="summary">{phase.done} of {phase.steps.length}{#if phase.running}{` · ${phase.running} running`}{/if}{#if phase.failed}{" · "}<StatusPill tone="failed" label={`${phase.failed} failed`} word />{/if}</span></button>{/if}
              {#if open}<ul>{#each phase.steps as step (step.key)}{@render row(step, false)}{/each}</ul>{/if}
            </div>
          {/each}
        </section>
      {/if}
    </div></div>
  {/if}
</div>

<style>
  .frame { display: flex; flex-direction: column; flex: 1; min-height: 0; container: run / inline-size; }
  /* The same column as the session thread, so a worker opens where its row was. */
  .run { flex: 1; min-height: 0; overflow-y: auto; scrollbar-gutter: stable both-edges; }
  .body { display: flex; flex-direction: column; gap: 24px; max-width: 784px; margin: auto; padding: 16px 24px; }
  .thread { flex: 1; min-height: 0; }
  header { display: flex; flex-direction: column; gap: 4px; }
  .heading { display: flex; align-items: center; gap: 12px; min-height: 28px; }
  h2 { min-width: 0; margin: 0; overflow: hidden; font-size: 18px; font-weight: 600; line-height: 1.25; text-overflow: ellipsis; white-space: nowrap; }
  h3 { margin: 0 0 8px; font-size: 18px; font-weight: 600; line-height: 1.25; }
  h4 { margin: 12px 0 4px; font-size: 14px; font-weight: 600; line-height: 1.25; }
  section { min-width: 0; }
  p { margin: 0; overflow-wrap: anywhere; line-height: 1.45; }
  .muted, .count { color: var(--muted); }
  .count { font-weight: 400; font-variant-numeric: tabular-nums; }
  .facts { display: flex; flex-wrap: wrap; gap: 0 12px; color: var(--muted); font-size: 12.5px; font-variant-numeric: tabular-nums; }
  .from { min-width: 0; overflow-wrap: anywhere; }
  .trouble { color: var(--danger); font-size: 12.5px; }
  .stop { display: flex; flex: none; align-items: center; gap: 6px; height: 28px; margin-left: auto; padding: 0 10px; border: 1px solid var(--line); border-radius: 6px; background: none; color: var(--text); font: inherit; font-size: 13px; font-weight: 500; cursor: pointer; }
  .stop :global(svg) { fill: currentColor; }
  .stop:hover:not(:disabled) { background: var(--hover); }
  .stop:disabled { color: var(--muted); cursor: default; }
  /* A failure's rows share the thread's: a 24 px icon slot and a 6 px gap. */
  .failure { display: flex; gap: 6px; min-height: 24px; font-size: 14px; line-height: 24px; overflow-wrap: anywhere; }
  .failure .slot { color: var(--danger); }
  .why { flex: 1; min-width: 0; }
  .text { white-space: pre-wrap; line-height: 24px; }
  .slot { display: inline-flex; flex: none; align-items: center; justify-content: center; width: 24px; height: 24px; }
  ul { margin: 0; padding: 0; list-style: none; }
  pre { margin: 0; padding: 8px 12px; max-height: 240px; overflow: auto; border-radius: 6px; background: var(--sidebar); color: var(--text); font-family: var(--mono); font-size: 12.5px; line-height: 1.45; white-space: pre-wrap; overflow-wrap: anywhere; }
  dl { display: grid; grid-template-columns: max-content minmax(0, 1fr); gap: 4px 12px; margin: 0; font-size: 13px; }
  dt { color: var(--muted); }
  dd { min-width: 0; margin: 0; overflow-wrap: anywhere; }
  code, .code { font-family: var(--mono); font-size: 12.5px; overflow-wrap: anywhere; }
  .code { color: var(--focus); text-decoration: none; }
  .code:hover { text-decoration: underline; }
  .code:focus-visible { outline: 2px solid var(--focus); outline-offset: 2px; border-radius: 2px; }
  .conflicts li { display: flex; flex-direction: column; gap: 2px; padding: 4px 0; }
  .conflict { display: flex; flex-wrap: wrap; gap: 0 8px; align-items: baseline; }
  .file { padding-left: 12px; color: var(--muted); }
  /* Rows take StepList's height and wash. Fixed columns keep the figures of every row under each other. */
  .phase + .phase { margin-top: 8px; }
  .phase-head { all: unset; box-sizing: border-box; display: flex; align-items: center; gap: 8px; width: calc(100% + 8px); min-height: 28px; margin: 0 -4px; padding: 0 4px; border-radius: 6px; font-size: 13px; cursor: pointer; }
  .phase-head strong { flex: none; max-width: 60%; overflow: hidden; font-weight: 600; text-overflow: ellipsis; white-space: nowrap; }
  .summary { min-width: 0; color: var(--muted); font-size: 12.5px; font-variant-numeric: tabular-nums; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .chevron { display: inline-flex; flex: none; width: 14px; color: var(--muted); transform: rotate(-90deg); transition: transform 150ms ease-out; }
  .chevron.open { transform: none; }
  .step { all: unset; box-sizing: border-box; display: grid; grid-template-columns: 14px minmax(0, 1fr) 88px 56px 56px 64px 14px; align-items: center; gap: 8px; width: calc(100% + 8px); min-height: 28px; margin: 0 -4px; padding: 0 4px; border-radius: 6px; color: var(--muted); font-size: 13px; }
  button.step { cursor: pointer; }
  .name { min-width: 0; overflow: hidden; color: var(--text); text-overflow: ellipsis; white-space: nowrap; }
  .meta { display: contents; }
  .profile { min-width: 0; overflow: hidden; font-size: 12.5px; text-overflow: ellipsis; white-space: nowrap; }
  .figure { font-size: 12.5px; font-variant-numeric: tabular-nums; text-align: right; white-space: nowrap; }
  .state { font-size: 12.5px; text-align: right; white-space: nowrap; }
  .go { display: inline-flex; width: 14px; }
  .reason { margin: 0 0 4px 22px; overflow: hidden; color: var(--muted); font-size: 12.5px; text-overflow: ellipsis; white-space: nowrap; }
  .reason.whole { white-space: pre-wrap; }
  .more { all: unset; margin-top: 4px; color: var(--muted); font-size: 12.5px; cursor: pointer; }
  .worker-bar { display: flex; flex: none; align-items: center; gap: 8px; min-height: 40px; padding: 4px 24px 4px 16px; border-bottom: 1px solid var(--line); font-size: 13px; }
  .worker-bar strong { min-width: 0; overflow: hidden; font-weight: 600; text-overflow: ellipsis; white-space: nowrap; }
  .worker-bar .muted, .worker-bar .figure { flex: none; font-size: 12.5px; color: var(--muted); }
  .worker-failure { flex: none; padding: 8px 24px 0 16px; }
  .back { all: unset; display: grid; flex: none; place-items: center; width: 28px; height: 28px; border-radius: 6px; color: var(--muted); cursor: pointer; }
  /* A fast load shows nothing; a slow one shows the shape of the page. */
  .skeleton { display: grid; gap: 12px; animation: appear 1ms 200ms backwards; }
  @keyframes appear { from { visibility: hidden; } }
  .skeleton i { width: 60%; height: 16px; border-radius: 6px; background: var(--hover); }
  @media (hover: hover) {
    button.step:hover, .phase-head:hover, .back:hover { background: var(--hover); color: var(--text); }
    .more:hover { color: var(--text); }
  }
  button.step:focus-visible, .phase-head:focus-visible, .back:focus-visible, .more:focus-visible { outline: 2px solid var(--focus); outline-offset: -2px; }
  /* A narrow column keeps the name and the state, and the figures take a second line. */
  @container run (max-width: 600px) {
    .run { scrollbar-gutter: stable; }
    .body { padding: 12px 16px; }
    .step { grid-template-columns: 14px minmax(0, 1fr) auto 14px; grid-template-areas: "icon name state go" ". meta meta go"; gap: 0 8px; padding-block: 4px; }
    .step > :global(svg) { grid-area: icon; }
    .name { grid-area: name; }
    .state { grid-area: state; }
    .go { grid-area: go; }
    .meta { grid-area: meta; display: flex; gap: 8px; min-width: 0; }
    .meta > :empty { display: none; }
    .profile { flex: 0 1 auto; }
    .worker-bar { padding: 4px 16px 4px 8px; }
    .worker-failure { padding: 8px 16px 0; }
  }
  @media (hover: none) { .step, .phase-head { min-height: 40px; } .back { width: 40px; height: 40px; } .stop { height: 40px; } }
  @media (prefers-reduced-motion: reduce) { .chevron { transition: none; } }
</style>
