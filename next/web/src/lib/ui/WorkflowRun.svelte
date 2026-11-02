<script lang="ts">
  import { onMount, tick, untrack } from "svelte";
  import type { RoomStore } from "../room.svelte.ts";
  import type { WorkflowStep } from "../contract.gen.ts";
  import { RunState } from "../run.svelte.ts";
  import { boxReason, formatTime, fullTime } from "../feed.ts";
  import { formatTimelineDuration } from "../timeline.ts";
  import { branchLink, commitLink, mergeFields, reading, runWords, runningFor, shortCommit, shownFacts, shownStep, stepTime, stepWord, tokenDetail, tokens, total } from "../workflow.ts";
  import CollapsibleBody from "./CollapsibleBody.svelte";
  import Icon from "./Icon.svelte";
  import Markdown from "./Markdown.svelte";
  import RunFields from "./RunFields.svelte";
  import RunSteps from "./RunSteps.svelte";
  import SessionThread from "./SessionThread.svelte";
  import StatusPill from "./StatusPill.svelte";

  // A crew's page: what the run does while it runs and what it left once it ends, all from the journal, so a closed crew reads the
  // same. It leads with why a run failed and with a reply, then the steps, then what the steps made. A worker opens in place, in
  // the session thread, by its step; the page stays behind it, so Back returns to the same folds and the same place.
  let { store, session, roomName, operate, navigate, onpostlocation, clock = Date.now }: {
    store: RoomStore; session: string; roomName: string; operate: boolean;
    navigate: (event: MouseEvent & { currentTarget: HTMLAnchorElement }) => void; onpostlocation: (room: string, seq: number) => void; clock?: () => number;
  } = $props();
  const run = untrack(() => new RunState(session));
  onMount(() => { const stop = store.watchRun(session, run.apply); void run.read(); return stop; });
  let { status, steps, failed } = $derived(run.snapshot);
  let info = $derived(status?.run);
  let merge = $derived(status?.merge);
  let conflicts = $derived((merge?.conflicts ?? []).filter(conflict => !conflict.reason));
  let result = $derived(info?.result && info.state !== "failed" ? reading(info.result, shownFacts(info, steps, merge)) : undefined);
  let ended = $derived(!!info && info.state !== "running");
  let broken = $derived(store.boxFailures.get(session));
  let words = $derived(runWords[info?.state ?? "running"]);
  let usage = $derived(total(steps));
  let stuck = $derived(info?.state === "running" && !!broken);

  // Everything that counts up shares one second, which stops with the run.
  let now = $state(untrack(() => clock()));
  $effect(() => {
    if (ended) return;
    now = clock();
    const timer = setInterval(() => { now = clock(); }, 1000);
    return () => clearInterval(timer);
  });

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
  {/if}
  <div class="run" hidden={!!worker}><div class="body">
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
        <p class="facts">{#if info.ended}<span title={fullTime(info.ended)}>{formatTime(info.ended)}</span>{/if}
          <span>{formatTimelineDuration(runningFor(info.started, info.ended, now))}</span>
          {#if tokens(usage)}<span title={tokenDetail(usage)}>{tokens(usage)} tokens</span>{/if}
          {#if info.commit}<span class="from">{info.repo ?? ""} at {@render commit(info.commit)}</span>{/if}</p>
        {#if store.stopFailed.has(session)}<p class="trouble" role="alert">The stop did not reach the engine. Try again.</p>{/if}
        {#if stuck && broken}<div class="failure" role="status"><span class="slot"><Icon name="warning" size={16} /></span><span>The box failed: {boxReason(broken)}. The run waits until the session restarts.</span></div>{/if}
      </header>

      {#if info.state === "failed"}
        <section aria-label="Why the run failed"><div class="failure"><span class="slot"><Icon name="warning" size={16} /></span>
          <div class="why">{#if info.result}<CollapsibleBody><p class="text">{info.result}</p></CollapsibleBody>{:else}<p class="text">The script failed and left no reason.</p>{/if}</div></div></section>
      {:else if result?.text}
        <section><h3>Result</h3><CollapsibleBody><Markdown rhythm="thread" text={result.text} /></CollapsibleBody></section>
      {/if}

      <RunSteps {session} {info} {steps} {now} onopen={step => void show(step)} />

      {#if merge}
        <section><h3>Merge</h3>
          <RunFields fields={mergeFields(merge)} repo={info.repo} />
          {#if conflicts.length}
            <h4>Conflicts <span class="count">{conflicts.length}</span></h4>
            <ul class="conflicts">{#each conflicts as conflict (conflict.step)}
              <li><span class="conflict"><StatusPill tone="failed" label={conflict.step} word />{#if conflict.branch}{@render branch(conflict.branch)}{/if}</span>
                {#each conflict.files ?? [] as file (file)}<code class="file">{file}</code>{/each}</li>
            {/each}</ul>
          {/if}</section>
      {/if}

      {#if result?.fields?.length}<section><h3>Result</h3><RunFields fields={result.fields} repo={info.repo} /></section>{/if}
    {/if}
  </div></div>
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
  code, .code { font-family: var(--mono); font-size: 12.5px; overflow-wrap: anywhere; }
  .code { color: var(--focus); text-decoration: none; }
  .code:hover { text-decoration: underline; }
  .code:focus-visible { outline: 2px solid var(--focus); outline-offset: 2px; border-radius: 2px; }
  .conflicts li { display: flex; flex-direction: column; gap: 2px; padding: 4px 0; }
  .conflict { display: flex; flex-wrap: wrap; gap: 0 8px; align-items: baseline; }
  .file { padding-left: 12px; color: var(--muted); }
  .worker-bar { display: flex; flex: none; align-items: center; gap: 8px; min-height: 40px; padding: 4px 24px 4px 16px; border-bottom: 1px solid var(--line); font-size: 13px; }
  .worker-bar strong { min-width: 0; overflow: hidden; font-weight: 600; text-overflow: ellipsis; white-space: nowrap; }
  .worker-bar .muted, .worker-bar .figure { flex: none; font-size: 12.5px; color: var(--muted); font-variant-numeric: tabular-nums; }
  .worker-failure { flex: none; padding: 8px 24px 0 16px; }
  .back { all: unset; display: grid; flex: none; place-items: center; width: 28px; height: 28px; border-radius: 6px; color: var(--muted); cursor: pointer; }
  /* A fast load shows nothing; a slow one shows the shape of the page. */
  .skeleton { display: grid; gap: 12px; animation: appear 1ms 200ms backwards; }
  @keyframes appear { from { visibility: hidden; } }
  .skeleton i { width: 60%; height: 16px; border-radius: 6px; background: var(--hover); }
  @media (hover: hover) { .back:hover { background: var(--hover); color: var(--text); } }
  .back:focus-visible { outline: 2px solid var(--focus); outline-offset: -2px; }
  /* A narrow column keeps the page's gutter. */
  @container run (max-width: 600px) {
    .run { scrollbar-gutter: stable; }
    .body { padding: 12px 16px; }
    .worker-bar { padding: 4px 16px 4px 8px; }
    .worker-failure { padding: 8px 16px 0; }
  }
  @media (hover: none) { .back { width: 40px; height: 40px; } .stop { height: 40px; } }
</style>
