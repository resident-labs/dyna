<script lang="ts">
  import Icon from "./Icon.svelte";
  import StepList from "./StepList.svelte";
  import { clock, commandLine, describe, eventGroups, formatTime, subject, timeSpan, workSummary, type Row } from "../feed.ts";
  import type { Event } from "../contract.gen.ts";
  import type { TurnTranscripts } from "../transcripts.svelte.ts";

  let { row, name, opened, ontoggle, transcripts, transcriptOf, onjobs }: {
    row: Extract<Row, { kind: "work" }>; name: (session: string) => string; opened: ReadonlySet<string>; ontoggle: (key: string) => void;
    transcripts: ReadonlyMap<string, TurnTranscripts>; transcriptOf: (session: string) => TurnTranscripts; onjobs: (session: string) => void;
  } = $props();
  let open = $derived(opened.has(row.key));
  let workers = $derived([...new Set(row.events.flatMap(event => event.type === "turn.started" || event.type === "turn.ended" ? [event.body.session] : []))].sort((a, b) => Number(row.failedSessions.includes(b)) - Number(row.failedSessions.includes(a))));
  let events = $derived(row.events);
  let other = $derived(events.filter(event => event.type !== "turn.started" && event.type !== "turn.ended" && event.type !== "turn.activity" && event.type !== "computer.changed"));
  let groups = $derived([...other.reduce((groups, event) => groups.set(event.type, [...groups.get(event.type) ?? [], event]), new Map<Event["type"], Event[]>()).values()]);
  let computerSessions = $derived([...new Set(events.flatMap(event => event.type === "computer.changed" ? [event.body.session] : []))]);
  function toggleWork() {
    if (!open && workers.length === 1) transcriptOf(workers[0]).read(events.filter(event => subject(event) === workers[0]));
    ontoggle(row.key);
  }
  function toggleSession(session: string, own: Event[]) {
    const key = `${row.key}-${session}`;
    if (!opened.has(key)) transcriptOf(session).read(own);
    ontoggle(key);
  }
  function computerUsage(session: string): string {
    let start: number | undefined, total = 0, uses = 0;
    for (const event of events) {
      if (event.type !== "computer.changed" || event.body.session !== session) continue;
      if (event.body.to === "running") { start = Date.parse(event.at); uses++; }
      else if (start !== undefined) { total += Math.max(0, Date.parse(event.at) - start); start = undefined; }
    }
    const minutes = Math.round(total / 60_000);
    return `${name(session)} used its computer${uses ? ` ${uses} ${uses === 1 ? "time" : "times"}` : ""}${minutes ? `, ${minutes} min in all` : ""}`;
  }
  let allWorkers = $derived(opened.has(`${row.key}-all`));
</script>

{#snippet requestDetails(event: Extract<Event, { type: "op.requested" }>)}<li class="step"><details><summary>Request details <span class="details-chevron"><Icon name="chevron" size={14} /></span></summary>
  <p>Target: {event.body.card.target.kind}{#if event.body.card.target.name}{" · "}{event.body.card.target.name}{/if}</p>
  {#if event.body.card.body}<p>{event.body.card.body}</p>{/if}
  {#if event.body.card.command?.length}<pre>{commandLine(event.body.card.command)}</pre>{/if}
  {#each event.body.card.changes ?? [] as change (change.field)}<p>{change.field}: {change.old || "none"} → {change.new || "none"}</p>{/each}
</details></li>{/snippet}

<div class="work">
  <span class="glyph" aria-hidden="true"><svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M12 3 3 8l9 5 9-5zM3 13l9 5 9-5" /></svg></span>
  <div class="body">
    <button type="button" class="fold" aria-expanded={open} onclick={toggleWork}>
      <span class="summary">{workSummary(row.events, name, row.until)} <time datetime={row.events[0].at}>{row.events.length > 1 || row.until ? timeSpan(row.events[0].at, row.until ?? row.events.at(-1)?.at ?? row.events[0].at) : clock(row.events[0].at)}</time></span>
      <span class="chevron" class:open><Icon name="chevron" size={14} /></span>
    </button>
    {#if open}<div class="opened">
      {#if workers.length === 1}
        {@const session = workers[0]}
        {@const own = events.filter(event => subject(event) === session)}
        {@const transcript = transcripts.get(session)?.of(own)}
        {#if transcript === "loading" || !transcript}<p>Loading steps…</p>
        {:else if transcript === "failed"}<p>The steps did not load. <button type="button" class="retry" onclick={() => transcriptOf(session).read(own, true)}>Retry</button></p>
        {:else if transcript.steps.length || transcript.truncated}<StepList steps={transcript.steps} truncated={transcript.truncated} id={row.key} {opened} {ontoggle} />{:else}<p>No steps were recorded.</p>{/if}
      {:else if workers.length > 1}
        <ul class="workers">{#each workers.slice(0, allWorkers ? undefined : 6) as session (session)}
          {@const own = events.filter(event => subject(event) === session)}
          {@const transcript = transcripts.get(session)?.of(own)}
          <li><button class="detail" type="button" aria-expanded={opened.has(`${row.key}-${session}`)} onclick={() => toggleSession(session, own)}>{workSummary(own, name)} <Icon name="chevron" size={14} /></button></li>
          {#if opened.has(`${row.key}-${session}`)}
            {#if transcript === "loading" || !transcript}<li class="step">Loading steps…</li>
            {:else if transcript === "failed"}<li class="step">The steps did not load. <button class="retry" type="button" onclick={() => transcriptOf(session).read(own, true)}>Retry</button></li>
            {:else if transcript.steps.length || transcript.truncated}<li class="step step-list"><StepList steps={transcript.steps} truncated={transcript.truncated} id={`${row.key}-${session}`} {opened} {ontoggle} /></li>{:else}<li class="step">No steps were recorded.</li>{/if}
          {/if}
        {/each}{#if !allWorkers && workers.length > 6}<li><button class="detail" type="button" onclick={() => ontoggle(`${row.key}-all`)}>Show {workers.length - 6} more sessions</button></li>{/if}</ul>
      {/if}
      <ul>{#each computerSessions as session (session)}<li><Icon name="monitor" size={14} /><span>{computerUsage(session)}</span></li>{/each}{#each groups as group (group[0].type)}{@const line = describe(group[0], name)}{#if line}<li><Icon name={group[0].type === "session.closed" ? "close" : group[0].type === "session.started" ? "person" : group[0].type === "op.decided" ? "check" : group[0].type === "op.requested" ? "shield" : "list"} size={14} />{#if group.length === 1}<span>{line}{#if group[0].type === "shell.job.ended"}{@const session = group[0].body.session} <button type="button" class="retry" onclick={() => onjobs(session)}>View jobs</button>{/if}</span>{:else}<button class="detail" type="button" aria-expanded={opened.has(`${row.key}-${group[0].type}`)} onclick={() => ontoggle(`${row.key}-${group[0].type}`)}>{group.length} {eventGroups[group[0].type]} <Icon name="chevron" size={14} /></button>{/if}<time datetime={group[0].at}>{formatTime(group[0].at)}</time></li>{#if group.length === 1 && group[0].type === "op.requested"}{@render requestDetails(group[0])}{/if}{#if group.length > 1 && opened.has(`${row.key}-${group[0].type}`)}{#each group as event (event.seq)}<li class="step"><span>{describe(event, name)}{#if event.type === "shell.job.ended"} <button type="button" class="retry" onclick={() => onjobs(event.body.session)}>View jobs</button>{/if}</span><time datetime={event.at}>{formatTime(event.at)}</time></li>{#if event.type === "op.requested"}{@render requestDetails(event)}{/if}{/each}{/if}{/if}{/each}</ul>
    </div>{/if}
  </div>
</div>

<style>
  .work { display: grid; grid-template-columns: 40px minmax(0, 1fr); gap: 16px; padding: 7px 16px; color: var(--muted); font-size: 13px; font-variant-numeric: tabular-nums; }
  .glyph { display: flex; justify-content: center; padding-top: 5px; }
  @media (max-width: 640px) { .work { gap: 8px; } }
  .body { min-width: 0; }
  .fold { all: unset; display: flex; align-items: flex-start; gap: 8px; width: 100%; min-height: 24px; cursor: pointer; border-radius: 6px; }
  .fold:hover, .retry:hover { color: var(--text); }
  .fold:focus-visible, .retry:focus-visible { outline: 2px solid var(--focus); outline-offset: 2px; }
  .summary { flex: 1; min-width: 0; padding-top: 3px; overflow-wrap: anywhere; }
  time { color: var(--muted); opacity: .8; white-space: nowrap; }
  .summary time { margin-left: 8px; }
  .chevron { display: inline-flex; flex: none; padding-top: 4px; transform: rotate(-90deg); }
  .chevron.open { transform: none; }
  .opened { margin: 4px 0 4px 10px; padding-left: 16px; border-left: 1px solid var(--line); }
  ul { margin: 0; padding: 0; list-style: none; }
  li { display: flex; gap: 12px; padding: 4px 0; }
  li span { flex: 1; min-width: 0; overflow-wrap: anywhere; }
  p { margin: 0; padding: 2px 0; }
  .retry, .detail { all: unset; cursor: pointer; color: var(--focus); }
  @media (hover: none) { .retry { display: inline-flex; align-items: center; min-height: 40px; } }
  .detail { display: inline-flex; align-items: center; gap: 6px; color: inherit; overflow-wrap: anywhere; }
  li > .detail { flex: 1; min-width: 0; }
  .detail:focus-visible { outline: 2px solid var(--focus); }
  .opened > .detail { display: flex; width: 100%; }
  .opened > .detail time { margin-left: auto; }
  .workers { margin-left: 6px; padding-left: 10px; border-left: 1px solid var(--line); }
  .step { margin-left: 16px; }
  .step-list { display: block; min-width: 0; }
  details { flex: 1; min-width: 0; overflow-wrap: anywhere; }
  summary { display: flex; align-items: center; gap: 8px; cursor: pointer; list-style: none; }
  summary::-webkit-details-marker { display: none; }
  .details-chevron { flex: none; margin-left: auto; display: inline-flex; transform: rotate(-90deg); }
  details[open] .details-chevron { transform: none; }
  pre { white-space: pre-wrap; overflow-wrap: anywhere; }
</style>
