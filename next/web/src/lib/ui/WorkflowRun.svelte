<script lang="ts">
  import { call } from "../client.ts";
  import { onMount, tick, untrack } from "svelte";
  import type { RoomStore } from "../room.svelte.ts";
  import type { RoomID, WorkflowStep } from "../contract.gen.ts";
  import { RunState } from "../run.svelte.ts";
  import { boxReason, formatTime, fromNow, fullTime } from "../feed.ts";
  import { formatTimelineDuration } from "../timeline.ts";
  import { FAILED, branchLink, commitLink, countedLine, flow, mergeFields, minutesLabel, openable, reading, runWords, runningFor, shortCommit, shownFacts, shownStep, stepTime, stepTitle, stepWord, tally, tokenDetail, tokens, total } from "../workflow.ts";
  import CollapsibleBody from "./CollapsibleBody.svelte";
  import Icon from "./Icon.svelte";
  import Markdown from "./Markdown.svelte";
  import RunFields from "./RunFields.svelte";
  import RunFlow from "./RunFlow.svelte";
  import RunSteps from "./RunSteps.svelte";
  import RunTimeline from "./RunTimeline.svelte";
  import SessionThread from "./SessionThread.svelte";
  import StatusPill from "./StatusPill.svelte";
  import WorkingDot from "./WorkingDot.svelte";

  // A crew's page: what the run does while it runs and what it left once it ends, all from the journal, so a closed crew reads the
  // same. It leads with the run's name, state and figures, its one status line and why it failed, then the run as a flowchart
  // or a timeline. A node or a bar opens its steps under the chart, and what the steps made follows. A worker opens in place, in
  // the session thread, by its step; the page stays behind it, so Back returns to the same view and the same place.
  let { store, hasRoom, session, roomName, operate, navigate, onpostlocation, clock = Date.now }: {
    hasRoom: (room: RoomID) => boolean;
    store: RoomStore; session: string; roomName: string; operate: boolean;
    navigate: (event: MouseEvent & { currentTarget: HTMLAnchorElement }) => void; onpostlocation: (room: string, seq: number) => void; clock?: () => number;
  } = $props();
  const run = untrack(() => new RunState(session));
  onMount(() => { const stop = store.watchRun(session, run.apply, run.say); void run.read(); return stop; });
  let { status, steps, lines, failed } = $derived(run.snapshot);
  let info = $derived(status?.run);
  let merge = $derived(status?.merge);
  let conflicts = $derived((merge?.conflicts ?? []).filter(conflict => !conflict.reason));
  let result = $derived(info?.result && info.state !== "failed" ? reading(info.result, shownFacts(info, steps, merge)) : undefined);
  let ended = $derived(!!info && info.state !== "running");
  let broken = $derived(store.boxFailures.get(session));
  let words = $derived(runWords[info?.state ?? "running"]);
  let usage = $derived(total(steps));
  let stuck = $derived(info?.state === "running" && !!broken);
  let counts = $derived(tally(steps));
  let settled = $derived(counts.done + counts.failed);
  // The script's latest line, or the counts when it wrote none or cleared it.
  let said = $derived(lines.at(-1)?.text ? lines.at(-1) : undefined);
  let shownLines = $derived(lines.filter(line => line.text));
  // A line's time counts from the run's first step, which a resumed run kept from before its own start.
  let origin = $derived(Math.min(Date.parse(info?.started ?? ""), ...steps.map(step => Date.parse(step.started))));
  let earlier = $state(false);
  let graph = $derived(flow(steps));

  // The view is the viewer's own choice, kept in this browser. A browser that blocks storage keeps it for the page.
  const viewKey = "dyna-run-view";
  let view = $state<"flow" | "timeline">(untrack(() => { try { return localStorage.getItem(viewKey) === "timeline" ? "timeline" : "flow"; } catch { return "flow"; } }));
  function choose(next: "flow" | "timeline") {
    view = next;
    try { localStorage.setItem(viewKey, next); } catch { /* Storage is blocked: the choice lasts for this page. */ }
  }

  // What a node or a bar opened under the chart: a stage's steps, the failed steps, or one step unfolded.
  let detail = $state<{ stage: string } | { step: string }>();
  let detailBox: HTMLElement | undefined = $state();
  let opened = $derived.by(() => {
    if (!detail) return undefined;
    if ("step" in detail) { const key = detail.step, step = steps.find(item => item.key === key); return step && { title: stepTitle(step), steps: [step], opened: [step.key], key }; }
    const key = detail.stage;
    if (key === FAILED) return { title: "Failed steps", steps: graph.failed, opened: [], key };
    const node = graph.nodes.find(item => item.key === key);
    return node && { title: node.title, steps: node.steps, opened: [], key };
  });
  async function reveal() {
    await tick();
    detailBox?.scrollIntoView({ block: "nearest", behavior: matchMedia("(prefers-reduced-motion: reduce)").matches ? "auto" : "smooth" });
  }
  function select(stage: string) {
    detail = detail && "stage" in detail && detail.stage === stage ? undefined : { stage };
    if (detail) void reveal();
  }
  // A worker opens its transcript; any other step unfolds under the chart.
  function open(step: WorkflowStep) {
    if (openable(step)) { void show(step); return; }
    detail = { step: step.key };
    void reveal();
  }

  // Everything that counts up shares one second, which stops with the run.
  let now = $state(untrack(() => clock()));
  $effect(() => {
    if (ended) return;
    now = clock();
    const timer = setInterval(() => { now = clock(); }, 1000);
    return () => clearInterval(timer);
  });

  let shown = $state(shownStep(location.search));
  let worker = $derived(shown ? steps.find(step => step.key === shown) : undefined);
  let drafts = $state<Record<string, string>>({});
  let steering = $derived(worker ? drafts[worker.key] ?? "" : "");
  let sending = $state<string[]>([]);
  let steerBusy = $derived(!!worker && sending.includes(worker.key));
  let steerSent = $state(false);
  let pauseBusy = $state(false);
  let controlError = $state("");
  async function pause() {
    if (!info || pauseBusy) return;
    pauseBusy = true;
    controlError = "";
    try {
      await call(info.paused ? "workflow.unpause" : "workflow.pause", { run: info.run });
      await run.read();
    } catch (error) { controlError = error instanceof Error ? error.message : String(error); }
    finally { pauseBusy = false; }
  }
  let navigation = 0;
  $effect(() => {
    shown;
    navigation++;
    steerSent = false;
    controlError = "";
  });
  async function steer(event: SubmitEvent) {
    event.preventDefault();
    if (!info || !worker || worker.state !== "running" || !steering.trim() || steerBusy) return;
    const key = worker.key, visit = navigation, text = steering;
    sending = [...sending, key];
    controlError = "";
    steerSent = false;
    try {
      await call("workflow.steer", { run: info.run, step: key, text });
      if (drafts[key] === text) drafts[key] = "";
      if (visit === navigation) steerSent = true;
    } catch (error) {
      await run.read();
      if (visit === navigation && steps.find(step => step.key === key)?.state === "running") controlError = "Could not send; your message is still here. " + (error instanceof Error ? error.message : String(error));
    }
    finally { sending = sending.filter(item => item !== key); }
  }
  let frame: HTMLElement | undefined = $state();
  let pushed = false;
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
    if (event.key === "Escape" && worker && !steering && !event.defaultPrevented) hide();
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

{#snippet controlFailure()}
  {#if controlError}<p class="control-error" role="alert">{controlError}</p>{/if}
{/snippet}

<div class="frame" bind:this={frame} role="presentation" onkeydown={closeWorker}>
  {#if worker}
    {@const state = stepWord(worker)}
    <div class="worker-bar">
      <button type="button" class="back" aria-label={`Back to ${info?.name ?? "the run"}`} title="Back to the run" onclick={hide}><Icon name="back" size={16} /></button>
      <strong title={worker.name}>{worker.name}</strong>
      {#if worker.profile}<span class="muted">Profile: {worker.profile}</span>{/if}
      <span class="figure">{stepTime(worker, now)}</span>
      {#if state.tone}<StatusPill tone={state.tone} label={state.word} word />{:else}<span class="muted">{state.word}</span>{/if}
    </div>
    {#if operate && ((!ended && worker.state === "running") || steering)}
      <form class="steer" onsubmit={steer}>
        <label for="worker-steer">{worker.state === "running" ? "Steer this worker" : "Your unsent message"}</label>
        <div><textarea id="worker-steer" rows="1" bind:value={drafts[worker.key]} placeholder="Tell this worker what to do differently" readonly={worker.state !== "running"} disabled={steerBusy} onkeydown={event => { if (event.key === "Enter" && !event.shiftKey && !event.isComposing && matchMedia("(hover: hover)").matches) { event.preventDefault(); event.currentTarget.form?.requestSubmit(); } }} oninput={() => { steerSent = false; controlError = ""; }}></textarea><button type="submit" disabled={steerBusy || !steering.trim() || worker.state !== "running"}>{steerBusy ? "Sending…" : "Send"}</button></div>
        <p class="steer-help">{worker.state !== "running" ? "This worker has ended. Your message was not sent." : info?.paused ? "The run is paused. Running workers can still be steered." : "Only this worker gets your message."}<span class="keyboard-hint">{worker.state === "running" ? " Enter to send · Shift+Enter for a new line" : ""}</span></p>
        {@render controlFailure()}
        <span class="steer-status" role="status">{steerSent ? "Sent to this worker." : ""}</span>
      </form>
    {/if}
    {#if worker.reason}<div class="failure worker-failure"><span class="slot"><Icon name="warning" size={16} /></span><span>{worker.reason}</span></div>{/if}
    <div class="thread">{#key worker.key}<SessionThread {store} {hasRoom} {session} {roomName} {navigate} {onpostlocation} {operate} step={worker.key} unsent={[]} onunsent={() => {}} onsettled={() => {}} />{/key}</div>
  {/if}
  <div class="run" hidden={!!worker}><div class="body">
    {#if !info && failed}<p class="muted" role="alert">The run did not load. <button type="button" class="retry" onclick={() => void run.read()}>Try again</button></p>
    {:else if !info}<div class="skeleton" aria-hidden="true"><i></i><i></i><i></i></div>
    {:else}
      <header>
        <div class="heading">
          <h2>{info.name}</h2>
          {#if info.paused && !ended}<StatusPill tone="warning" label="Paused" />{:else if words.tone}<StatusPill tone={words.tone} label={words.word} />{:else}<span class="muted">{words.word}</span>{/if}
          {#if info.state === "running" && operate && store.running(session)}
            <div class="controls">
              <button type="button" class="control" disabled={pauseBusy} title={info.paused ? "Start new steps again" : "Stop starting new steps; running steps finish"} onclick={pause}>{pauseBusy ? (info.paused ? "Unpausing…" : "Pausing…") : info.paused ? "Unpause" : "Pause"}</button>
              <button type="button" class="control" disabled={store.stopping.has(session)} onclick={() => store.stop(session)}><Icon name="stop" size={14} />{store.stopping.has(session) ? "Stopping" : "Stop"}</button>
            </div>
          {/if}
          <p class="facts">{#if info.ended}<span title={fullTime(info.ended)}>{formatTime(info.ended)}</span>{/if}
            <span>{formatTimelineDuration(runningFor(info.started, info.ended, now))}</span>
            {#if steps.length}<span>{counts.running || !settled ? `${settled} of ${steps.length} steps` : `${steps.length} steps`}</span>{/if}
            {#if tokens(usage)}<span title={tokenDetail(usage)}>{tokens(usage)} tokens</span>{/if}
            {#if (info.version ?? 1) > 1}<span title="Reloaded {(info.version ?? 1) - 1} {info.version === 2 ? 'time' : 'times'}">script v{info.version}</span>{/if}
            {#if info.commit}<span class="from">{info.repo ?? ""} at {@render commit(info.commit)}</span>{/if}</p>
        </div>
        {@render controlFailure()}
        {#if info.paused && !ended}<p class="muted">No new steps start. Running workers keep going and can still be steered. Unpause to continue.</p>{/if}
        {#if info.description}<p class="muted">{info.description}</p>{/if}
        {#if !ended && steps.some(step => step.kind === "agent" && step.state === "running" && step.transcript)}
          <div class="running-workers"><span>Workers:</span>{#each steps.filter(step => step.kind === "agent" && step.state === "running" && step.transcript).slice(0, 4) as step (step.key)}<button type="button" title="Open transcript and steer this worker" onclick={() => show(step)}>{step.name}</button>{/each}{#if counts.running > 4}<span>and {counts.running - 4} more in the chart</span>{/if}</div>
        {/if}
        <div class="said" class:script={!!said}>
          {#if info.state === "running"}<WorkingDot />{/if}
          {#if shownLines.length > 1}<button type="button" class="line" aria-expanded={earlier} title={earlier ? "Hide what the script said before" : "Show what the script said before"}
            onclick={() => { earlier = !earlier; }}>{said?.text ?? countedLine({ steps: steps.length, ...counts })}<span class="chevron" class:open={earlier}><Icon name="chevron" size={14} /></span></button>
          {:else}<p class="line">{said?.text ?? countedLine({ steps: steps.length, ...counts })}</p>{/if}
          {#if said}<span class="age" title={fullTime(said.at)}>written by the script · {fromNow(said.at, now)}</span>{/if}
        </div>
        {#if earlier && shownLines.length > 1}
          <ol class="history" aria-label="What the script said, newest last">{#each shownLines as line, index (index)}
            <li class:current={index === shownLines.length - 1}><span class="at" title={fullTime(line.at)}>{minutesLabel(Math.max(0, Date.parse(line.at) - origin))}</span><span>{line.text}</span></li>
          {/each}</ol>
        {/if}
        {#if store.stopFailed.has(session)}<p class="trouble" role="alert">The stop did not reach the engine. Try again.</p>{/if}
        {#if stuck && broken}<div class="failure" role="status"><span class="slot"><Icon name="warning" size={16} /></span><span>The box failed: {boxReason(broken)}. The run waits until the session restarts.</span></div>{/if}
      </header>

      {#if info.state === "failed"}
        <section aria-label="Why the run failed"><div class="failure"><span class="slot"><Icon name="warning" size={16} /></span>
          <div class="why">{#if info.result}<CollapsibleBody><p class="text">{info.result}</p></CollapsibleBody>{:else}<p class="text">The script failed and left no reason.</p>{/if}</div></div></section>
      {/if}

      <section class="views" aria-label="The run">
        <div class="switch" role="group" aria-label="View">
          <button type="button" aria-pressed={view === "flow"} onclick={() => choose("flow")}>Flow</button>
          <button type="button" aria-pressed={view === "timeline"} onclick={() => choose("timeline")}>Timeline</button>
        </div>
        {#if !steps.length}<p class="muted">{info.state !== "running" ? "The run ended before its first step." : "The first step will appear here."}</p>
        {:else if view === "flow"}<RunFlow nodes={graph.nodes} edges={graph.edges} failed={graph.failed} laned={graph.laned} {now} selected={detail && "stage" in detail ? detail.stage : undefined} onselect={select} />
        {:else}<RunTimeline {info} {steps} {now} onopen={open} />{/if}
      </section>

      {#if opened}
        <section class="detail" bind:this={detailBox}>
          {#key opened.key}<RunSteps {session} {info} steps={opened.steps} {now} title={opened.title} opened={opened.opened} onopen={step => void show(step)} />{/key}
          <button type="button" class="close" aria-label={`Close ${opened.title}`} title="Close" onclick={() => { detail = undefined; }}><Icon name="close" size={14} /></button>
        </section>
      {/if}

      {#if result?.text}
        <section><h3>Result</h3><CollapsibleBody><Markdown rhythm="thread" text={result.text} /></CollapsibleBody></section>
      {/if}

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
  .steer { padding: 8px 16px; border-bottom: 1px solid var(--line); }
  .steer label { display: block; font-size: 12px; margin-bottom: 4px; }
  .steer div { display: flex; align-items: flex-end; gap: 8px; }
  .steer textarea { flex: 1; min-width: 0; height: 32px; padding: 0 10px; border: 1px solid var(--line); border-radius: 6px; background: var(--raised); color: var(--text); font: inherit; font-size: 13px; }
  .steer textarea::placeholder { color: var(--muted); }
  .steer button { height: 32px; min-width: 84px; padding: 0 12px; border: 1px solid var(--line); border-radius: 6px; background: none; color: var(--text); font: inherit; font-size: 13px; cursor: pointer; }
  .steer button:disabled { opacity: 0.45; cursor: default; }
  .steer textarea:focus-visible, .steer button:focus-visible { outline: 2px solid var(--focus); outline-offset: 2px; }
  .steer-status { display: block; color: var(--muted); font-size: 12px; }
  .steer-status { min-height: 16px; }
  .steer-help { color: var(--muted); font-size: 12px; margin-top: 4px; }
  .steer textarea { resize: vertical; field-sizing: content; min-height: 32px; max-height: 144px; height: auto; padding-block: 6px; }
  @media (hover: none) { .steer textarea, .steer button { min-height: 40px; } .keyboard-hint { display: none; } .running-workers button { min-height: 40px; } }
  .control-error { color: var(--danger); font-size: 12px; margin-top: 4px; }

  .frame { display: flex; flex-direction: column; flex: 1; min-height: 0; container: run / inline-size; }
  /* Wider than the session thread, so a flowchart's row of stages and a timeline have room. */
  .run { flex: 1; min-height: 0; overflow-y: auto; scrollbar-gutter: stable both-edges; }
  .body { display: flex; flex-direction: column; gap: 24px; max-width: 1120px; margin: auto; padding: 16px 24px; }
  .thread { flex: 1; min-height: 0; }
  header { display: flex; flex-direction: column; gap: 4px; }
  .heading { display: flex; flex-wrap: wrap; align-items: center; gap: 4px 12px; min-height: 28px; }
  .heading .facts { margin-left: auto; }
  /* The status line: the script's own words, or the counts, in the run's voice under its name. */
  .said { display: flex; align-items: center; gap: 10px; min-height: 28px; margin-top: 6px; padding: 8px 0; border-block: 1px solid var(--line); }
  .said .line { all: unset; display: inline-flex; align-items: center; gap: 6px; min-width: 0; color: var(--muted); font-size: 15px; overflow-wrap: anywhere; }
  .said.script .line { color: var(--text); font-weight: 500; }
  button.line { cursor: pointer; border-radius: 4px; }
  button.line:focus-visible { outline: 2px solid var(--focus); outline-offset: 2px; }
  .age { flex: none; margin-left: auto; color: var(--muted); font-size: 11px; font-variant-numeric: tabular-nums; white-space: nowrap; }
  .chevron { display: inline-flex; flex: none; color: var(--muted); transform: rotate(-90deg); transition: transform 150ms ease-out; }
  .chevron.open { transform: none; }
  .history { margin: 4px 0 0; padding: 0; list-style: none; }
  .history li { display: grid; grid-template-columns: 52px minmax(0, 1fr); gap: 8px; padding: 6px 0; border-top: 1px solid var(--line); color: var(--muted); font-size: 13px; overflow-wrap: anywhere; }
  .history li:first-child { border-top: 0; }
  .history li.current { color: var(--text); font-weight: 500; }
  .history .at { font-size: 12.5px; font-weight: 400; font-variant-numeric: tabular-nums; }
  .views { display: flex; flex-direction: column; gap: 16px; }
  .switch { display: flex; gap: 4px; }
  .switch button { padding: 4px 10px; border: 0; border-radius: 6px; background: none; color: var(--muted); font: inherit; font-size: 13px; font-weight: 500; cursor: pointer; }
  .switch button[aria-pressed="true"] { background: var(--selected); color: var(--text); }
  .switch button:focus-visible { outline: 2px solid var(--focus); outline-offset: 2px; }
  .detail { position: relative; padding: 12px 16px; border: 1px solid var(--line); border-radius: 12px; }
  .close { all: unset; position: absolute; top: 10px; right: 10px; display: grid; place-items: center; width: 28px; height: 28px; border-radius: 6px; color: var(--muted); cursor: pointer; }
  .close:focus-visible { outline: 2px solid var(--focus); outline-offset: -2px; }
  @media (hover: hover) { .switch button:hover, .close:hover, button.line:hover { background: var(--hover); color: var(--text); } }
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
  .running-workers { display: flex; flex-wrap: wrap; align-items: center; gap: 4px 12px; color: var(--muted); font-size: 12px; }
  .running-workers button { border: 0; border-bottom: 1px solid var(--line); padding: 2px 0; max-width: 220px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; background: none; color: var(--text); font: inherit; cursor: pointer; }
  .running-workers button:hover { border-color: var(--text); }
  .running-workers button:focus-visible { outline: 2px solid var(--focus); outline-offset: 2px; }
  .controls { display: flex; gap: 8px; }
  .control { display: flex; flex: none; align-items: center; gap: 6px; height: 28px; padding: 0 10px; border: 1px solid var(--line); border-radius: 6px; background: none; color: var(--text); font: inherit; font-size: 13px; font-weight: 500; cursor: pointer; }
  .control :global(svg) { fill: currentColor; }
  .control:hover:not(:disabled) { background: var(--hover); }
  .control:disabled { color: var(--muted); cursor: default; }
  .control:focus-visible { outline: 2px solid var(--focus); outline-offset: 2px; }
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
    .heading .facts { flex-basis: 100%; margin-left: 0; }
    .said { flex-wrap: wrap; gap: 2px 10px; }
    .said .line { flex: 1 1 calc(100% - 20px); font-size: 14px; }
    .age { margin-left: 17px; }
    .detail { padding: 8px 12px; }
    .worker-bar { padding: 4px 16px 4px 8px; }
    .worker-failure { padding: 8px 16px 0; }
  }
  @media (hover: none) { .back { width: 40px; height: 40px; } .control { height: 40px; } }
</style>
