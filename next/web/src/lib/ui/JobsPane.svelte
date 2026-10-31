<script lang="ts">
  import { tick } from "svelte";
  import { SvelteMap, SvelteSet } from "svelte/reactivity";
  import Avatar from "./Avatar.svelte";
  import Icon from "./Icon.svelte";
  import StatusPill from "./StatusPill.svelte";
  import { failed, failingLine, jobGroups, jobTime } from "../jobs.ts";
  import { watchShell, type ShellJob } from "../shell.ts";
  // The dock's Jobs tab (round 2 winners, Jobs design B): the room's background jobs on the left, the chosen job's
  // output on the right. It watches each session's jobs only while it is mounted and the page is visible, so closing
  // the tab closes every watch (fix 1). only narrows the list to one session, as its row's jobs icon asks.
  // onrunning reports how many of its rows run, none until the first job comes, and opened gets each failed job the
  // owner picks, for the tab's dot.
  let { sessions, only = $bindable(), opened, onrunning }: {
    sessions: { id: string; name: string; root: boolean }[]; only?: string;
    opened: Set<string>; onrunning: (count: number | undefined) => void;
  } = $props();

  const outputs = new SvelteMap<string, SvelteMap<string, ShellJob>>();
  let visible = $state(document.visibilityState === "visible");
  // One watch per session: a session that gains jobs starts one, and one that leaves stops its own, so the others
  // keep their output. The engine refuses a session that is not running; its jobs leave the list.
  const stops = new Map<string, () => void>();
  $effect(() => {
    const ids = new Set(visible ? sessions.map(session => session.id) : []);
    for (const [id, stop] of stops) if (!ids.has(id)) { stop(); stops.delete(id); outputs.delete(id); }
    for (const id of ids) {
      if (stops.has(id)) continue;
      const jobs = new SvelteMap<string, ShellJob>();
      outputs.set(id, jobs);
      stops.set(id, watchShell(id, jobs, closed => { if (closed) jobs.clear(); }));
    }
  });
  $effect(() => () => { for (const stop of stops.values()) stop(); stops.clear(); });

  let names = $derived(new Map(sessions.map(session => [session.id, session])));
  let groups = $derived(jobGroups([...outputs].filter(([id]) => !only || id === only).map(([id, jobs]) => ({ id, jobs: jobs.values() })), Date.now()));
  // With 2 or more sessions, each group has its session's name above it (fix 2).
  let grouped = $derived(groups.length > 1);
  const unfolded = new SvelteSet<string>();
  const keyOf = (session: string, job: ShellJob) => `${session}/${job.job}`;
  $effect(() => {
    const seen = [...outputs.values()].flatMap(jobs => [...jobs.values()]);
    onrunning(seen.length ? seen.filter(job => job.running).length : undefined);
  });

  // The first job shows until the owner picks one: running jobs come first.
  let chosen: string | undefined = $state();
  let shown = $derived.by(() => {
    const rows = groups.flatMap(group => [...group.jobs, ...group.older].map(job => ({ session: group.session, job })));
    return rows.find(row => keyOf(row.session, row.job) === chosen) ?? rows[0];
  });
  let fail = $derived(shown && failed(shown.job) ? failingLine(shown.job.text) : undefined);
  const outcome = (job: ShellJob) => job.running ? "running" : failed(job) ? `failed · exit ${job.exitCode}` : `passed · ${jobTime(job)}`;

  // A running job's output follows its end unless the owner scrolls up. A failed job opens at its first failing line
  // (fix 3), marked; with none, at its end.
  let output: HTMLPreElement | undefined = $state();
  let failing: HTMLElement | undefined = $state();
  let following = $state(true);
  let openedKey = "";
  $effect(() => {
    if (!shown || !output) return;
    const key = keyOf(shown.session, shown.job);
    if (key !== openedKey) {
      openedKey = key;
      following = !fail;
      void tick().then(() => { if (failing) failing.scrollIntoView({ block: "start" }); });
    }
    void shown.job.text;
    if (following) output.scrollTop = output.scrollHeight;
  });

  let list: HTMLElement | undefined = $state();
  export function focus() { (list?.querySelector<HTMLElement>("[aria-current=true]") ?? list)?.focus(); }
  // Up and Down move between jobs, as in the sessions panel.
  function walk(event: KeyboardEvent) {
    if (event.key !== "ArrowDown" && event.key !== "ArrowUp" || !list) return;
    event.preventDefault();
    const rows = Array.from(list.querySelectorAll<HTMLElement>("button.job, button.older"));
    const index = rows.findIndex(row => row === document.activeElement); // -1 when focus is elsewhere; Down then takes the first
    rows[Math.max(0, Math.min(rows.length - 1, index + (event.key === "ArrowDown" ? 1 : -1)))]?.focus();
  }
</script>

{#snippet jobRow(session: string, job: ShellJob)}
  <button type="button" class="job" aria-current={shown?.job === job && shown.session === session} title={job.command}
    onclick={() => { chosen = keyOf(session, job); if (failed(job)) opened.add(chosen); }}>
    <span class="dot">{#if job.running}<StatusPill tone="working" dot label="Running" />{:else if failed(job)}<StatusPill tone="failed" dot label="Failed" />
      {:else}<i></i>{/if}</span>
    <code>{job.command}</code>
    {#if failed(job)}<StatusPill tone="failed" word label={outcome(job)} />{:else}<span class="state">{outcome(job)}</span>{/if}
  </button>
{/snippet}

<svelte:document onvisibilitychange={() => { visible = document.visibilityState === "visible"; }} />
<div class="pane"><div class="jobs">
  <!-- svelte-ignore a11y_no_noninteractive_tabindex, a11y_no_noninteractive_element_interactions (the list takes the arrow keys) -->
  <div class="list" role="group" aria-label="Background jobs" tabindex="-1" bind:this={list} onkeydown={walk}>
    {#if only}<p class="only">{names.get(only)?.name ?? "This session"}'s jobs
      <button type="button" class="retry" onclick={() => { only = undefined; }}>All sessions</button></p>{/if}
    {#each groups as group (group.session)}
      {#if grouped}<p class="session"><Avatar kind="session" name={names.get(group.session)?.name ?? ""} root={names.get(group.session)?.root} size={16} />
        {names.get(group.session)?.name}</p>{/if}
      {#each group.jobs as job (job.job)}{@render jobRow(group.session, job)}{/each}
      {#if group.older.length}
        <button type="button" class="older" aria-expanded={unfolded.has(group.session)}
          onclick={() => { if (!unfolded.delete(group.session)) unfolded.add(group.session); }}><Icon name="chevron" size={12} />{group.older.length === 1 ? "1 older job" : `${group.older.length} older jobs`}</button>
        {#if unfolded.has(group.session)}{#each group.older as job (job.job)}{@render jobRow(group.session, job)}{/each}{/if}
      {/if}
    {:else}<p class="hint">No job output yet.</p>{/each}
  </div>
  <section class="output" aria-label="Job output">
    {#if shown}
      <header><code class="command">{shown.job.command}</code>
        <span class="meta">{names.get(shown.session)?.name} · {#if failed(shown.job)}<StatusPill tone="failed" word label={outcome(shown.job)} /> · {jobTime(shown.job)}{:else}{outcome(shown.job)}{/if}</span></header>
      <!-- svelte-ignore a11y_no_noninteractive_tabindex (the keyboard scrolls the output) -->
      <pre bind:this={output} tabindex="0" aria-label={`Output of ${shown.job.command}`}
        onscroll={() => { if (output) following = output.scrollHeight - output.scrollTop - output.clientHeight < 40; }}
        >{#if fail}{shown.job.text.slice(0, fail.start)}<mark bind:this={failing}>{shown.job.text.slice(fail.start, fail.end)}</mark>{shown.job.text.slice(fail.end)}{:else}{shown.job.text || "No output yet."}{/if}</pre>
      {#if !following && shown.job.running}<button type="button" class="retry resume" onclick={() => { following = true; }}>Resume following</button>{/if}
    {/if}
  </section>
</div></div>

<style>
  /* The list on the left and the output on the right; in a narrow dock, such as on a phone, the list sits above. */
  .pane { flex: 1; min-height: 0; display: flex; container-type: inline-size; }
  .jobs { flex: 1; min-width: 0; display: grid; grid-template-columns: minmax(240px, 2fr) 3fr; }
  .list { min-height: 0; overflow-y: auto; padding: 6px; border-right: 1px solid var(--line); }
  .list:focus-visible { outline: 2px solid var(--focus); outline-offset: -2px; }
  .only { display: flex; align-items: baseline; gap: 8px; margin: 2px 6px 6px; color: var(--muted); font-size: 12.5px; }
  .session { display: flex; align-items: center; gap: 6px; margin: 8px 6px 2px; color: var(--muted); font-size: 12.5px; font-weight: 600; }
  .session:first-child { margin-top: 2px; }
  .job { all: unset; box-sizing: border-box; display: flex; align-items: center; gap: 8px; width: 100%; min-height: 30px; padding: 0 8px; border-radius: 6px;
    color: var(--muted); font-size: 12.5px; cursor: pointer; }
  .job:hover { background: var(--hover); }
  .job[aria-current="true"] { background: var(--selected); color: var(--text); }
  .job:focus-visible { outline: 2px solid var(--focus); outline-offset: -2px; }
  .older { all: unset; box-sizing: border-box; display: flex; align-items: center; gap: 8px; width: 100%; min-height: 28px; padding: 0 9px; border-radius: 6px;
    color: var(--muted); font-size: 12.5px; cursor: pointer; }
  .older:hover { background: var(--hover); color: var(--text); }
  .older:focus-visible { outline: 2px solid var(--focus); outline-offset: -2px; }
  .older[aria-expanded="false"] :global(svg) { transform: rotate(-90deg); }
  .dot { flex: none; display: grid; place-items: center; width: 14px; }
  .dot > i { width: 7px; height: 7px; border-radius: 50%; background: var(--muted); opacity: 0.6; }
  .job code { flex: 1; min-width: 0; overflow: hidden; color: var(--text); font: 12.5px/1.4 var(--mono); text-overflow: ellipsis; white-space: nowrap; }
  .state { flex: none; font-variant-numeric: tabular-nums; }
  .output { min-width: 0; min-height: 0; display: flex; flex-direction: column; }
  .output header { display: grid; gap: 2px; padding: 8px 12px; border-bottom: 1px solid var(--line); }
  .command { font: 12.5px/1.4 var(--mono); overflow-wrap: anywhere; }
  .meta { color: var(--muted); font-size: 12.5px; }
  pre { flex: 1; min-height: 0; margin: 0; padding: 8px 12px; overflow-y: auto; font: 12.5px/1.5 var(--mono); white-space: pre-wrap; overflow-wrap: anywhere; }
  pre:focus-visible { outline: 2px solid var(--focus); outline-offset: -2px; }
  mark { display: inline-block; width: 100%; background: color-mix(in srgb, var(--danger) 14%, transparent); color: inherit; scroll-margin-top: 8px; }
  .resume { align-self: center; margin: 6px 0; font-size: 12.5px; }
  @container (max-width: 560px) {
    .jobs { grid-template-columns: minmax(0, 1fr); grid-template-rows: minmax(0, 2fr) minmax(0, 3fr); }
    .list { border-right: 0; border-bottom: 1px solid var(--line); }
  }
</style>
