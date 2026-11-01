<script lang="ts">
  import { tick } from "svelte";
  import { SvelteSet } from "svelte/reactivity";
  import Avatar from "./Avatar.svelte";
  import Icon from "./Icon.svelte";
  import JobOutput from "./JobOutput.svelte";
  import StatusPill from "./StatusPill.svelte";
  import { endCause, failed, jobGroups, jobTime, jobTitle } from "../jobs.ts";
  import { ShellWatches, type ShellJob } from "../shell.ts";
  // The room header's Jobs tool (dock design C). Its slot never moves; only its counts change: the running jobs as a
  // muted count, then the failures the owner has not opened, in the failed tone; only a failure gets a mark. A click opens a popover under
  // it, or a sheet on a phone, with the jobs grouped by session; a job opens as a dock tab (onopen), or on a phone as
  // a full-height sheet. The lists watch each session's jobs only while they are open and the page is visible.
  type JobSession = { id: string; name: string; root: boolean; running: number };
  let { sessions, allSessions, failures, allFailures, phone, onopen, onseen }: {
    sessions: JobSession[]; allSessions: JobSession[]; failures: string[]; allFailures: string[]; phone: boolean;
    onopen: (session: JobSession, job: ShellJob) => void; onseen: (key: string) => void;
  } = $props();

  let open = $state(false);
  let only: string | undefined = $state();
  let shown: { session: JobSession; job: string } | undefined = $state();
  let button: HTMLButtonElement | undefined = $state();
  let opener: HTMLElement | undefined;
  let list: HTMLElement | undefined = $state();
  let root: HTMLElement | undefined = $state();
  let visible = $state(document.visibilityState === "visible");
  const watches = new ShellWatches();
  let listSessions = $derived(only && !sessions.some(session => session.id === only) ? allSessions.filter(session => session.id === only) : sessions);
  $effect(() => { watches.sync(new Set(open && visible ? listSessions.map(session => session.id) : [])); });
  $effect(() => () => watches.sync(new Set()));

  let running = $derived(sessions.reduce((sum, session) => sum + session.running, 0));
  let summary = $derived([running && `${running} running`, failures.length && `${failures.length} failed`].filter(Boolean).join(", ") || "No jobs running");
  let names = $derived(new Map(listSessions.map(session => [session.id, session])));
  let groups = $derived(jobGroups([...watches.jobs].filter(([id]) => !only || id === only).map(([id, jobs]) => ({ id, jobs: jobs.values() }))));
  let listed = $derived(groups.flatMap(group => group.jobs.concat(group.older)));
  let listedRunning = $derived(listed.filter(job => job.running).length);
  let listedFailed = $derived(listed.filter(failed).length);
  let counts = $derived([listedRunning && `${listedRunning} running`, listedFailed && `${listedFailed} failed`].filter(Boolean).join(" · "));
  const older = new SvelteSet<string>();

  // A session row, About's Jobs row or a work row's jobs open the list on that session's jobs.
  export async function show(session?: string) {
    opener = document.activeElement instanceof HTMLElement ? document.activeElement : undefined;
    only = session;
    shown = undefined;
    open = true;
    await tick();
    (list?.querySelector<HTMLElement>("button.job") ?? list)?.focus();
  }
  async function close() {
    open = false;
    shown = undefined;
    await tick();
    (opener?.isConnected ? opener : button)?.focus();
  }
  function pick(session: JobSession, job: ShellJob) {
    const key = `${session.id}/${job.job}`;
    if (allFailures.includes(key)) onseen(key);
    if (phone) { shown = { session, job: job.job }; return; }
    open = false;
    onopen(session, job);
  }
  // Up and Down move between the rows, Escape closes and returns to the button.
  function keys(event: KeyboardEvent) {
    if (event.key === "Escape") { event.stopPropagation(); void (shown ? (shown = undefined) : close()); return; }
    if (event.key !== "ArrowDown" && event.key !== "ArrowUp" || !list) return;
    event.preventDefault();
    const rows = Array.from(list.querySelectorAll<HTMLElement>("button.job, button.older"));
    const index = rows.findIndex(row => row === document.activeElement);
    rows[Math.max(0, Math.min(rows.length - 1, index + (event.key === "ArrowDown" ? 1 : -1)))]?.focus();
  }
  const stateText = (job: ShellJob, session: JobSession) => job.running ? "Running" : failed(job) ? endCause(job, session.name)
    : `${endCause(job, session.name)}${jobTime(job) ? ` · ${jobTime(job)}` : ""}`;
</script>

{#snippet row(session: JobSession, job: ShellJob)}
  <button type="button" class="job" onclick={() => pick(session, job)}>
    <span class="dot">{#if job.running}<StatusPill tone="working" dot label="Running" />{:else if failed(job)}<StatusPill tone="failed" dot label="Failed" />{/if}</span>
    <span class="lines"><span class="title">{jobTitle(job)}</span><code>{job.command}</code></span>
    <span class="state" class:bad={failed(job)}>{stateText(job, session)}</span>
  </button>
{/snippet}

<svelte:document onvisibilitychange={() => { visible = document.visibilityState === "visible"; }}
  onpointerdown={event => { if (open && !phone && event.target instanceof Node && !root?.contains(event.target)) open = false; }} />
<span class="jobs" bind:this={root}>
  <button type="button" class="open" class:live={running || failures.length} class:phone bind:this={button} aria-haspopup="dialog" aria-expanded={open}
    aria-label={`Jobs, ${summary}`} title={summary} onclick={() => { if (open) void close(); else void show(); }}>
    <Icon name="briefcase" />{#if phone}{#if failures.length}<span class="corner"><StatusPill tone="failed" dot label="Failed" /></span>{/if}
    {:else}{#if running}<span class="n">{running}</span>{/if}
      {#if failures.length}<StatusPill tone="failed" label={`${failures.length} failed`} />{/if}{/if}</button>
  {#if open}
    {#if phone}<button type="button" class="scrim" tabindex="-1" aria-label="Close the jobs" onclick={() => void close()}></button>{/if}
    <!-- svelte-ignore a11y_no_noninteractive_element_interactions (the arrow keys and Escape act on the whole list) -->
    <div class="pop" class:sheet={phone} class:full={shown} role="dialog" tabindex="-1" aria-label="Jobs" onkeydown={keys}>
      {#if shown}{@const session = shown.session}{@const job = watches.jobs.get(session.id)?.get(shown.job)}
        <JobOutput {session} {job} label={job ? jobTitle(job) : "Job"} onback={() => { shown = undefined; }} />
      {:else}
        {#if phone}<span class="grab"></span>{/if}
        <div class="head"><strong>Jobs</strong>{#if counts}<span class="counts">{counts}</span>{/if}<span class="space"></span>
          {#if listSessions.length > 1}<label class="filter"><span class="hidden">Show the jobs of</span><select bind:value={only}>
            <option value={undefined}>All sessions</option>{#each listSessions as session (session.id)}<option value={session.id}>{session.name}</option>{/each}</select>
            <Icon name="chevron" size={12} /></label>{/if}</div>
        <!-- svelte-ignore a11y_no_noninteractive_tabindex (the list takes the arrow keys) -->
        <div class="list" role="group" aria-label="Background jobs" tabindex="-1" bind:this={list}>
          {#each groups as group (group.session)}{@const session = names.get(group.session)}{#if session}
            {#if listSessions.length > 1}<p class="session"><Avatar kind="session" name={session.name} root={session.root} size={16} />{session.name}</p>{/if}
            {#each group.jobs as job (job.job)}{@render row(session, job)}{/each}
            {#if older.has(group.session)}{#each group.older as job (job.job)}{@render row(session, job)}{/each}
            {:else if group.older.length}<button type="button" class="older" onclick={() => older.add(group.session)}>Show older</button>{/if}
          {/if}{:else}<p class="none">{listSessions.length ? "No job output yet." : "No session has run a background job."}</p>{/each}
        </div>
      {/if}
    </div>
  {/if}
</span>

<style>
  .jobs { position: relative; margin-left: auto; display: flex; }
  .open { all: unset; display: flex; align-items: center; gap: 6px; height: 30px; padding: 0 7px; border-radius: 6px; color: var(--muted);
    font-size: 13px; font-weight: 500; cursor: pointer; }
  .open.live { color: var(--text); }
  .open:hover, .open[aria-expanded="true"] { background: var(--selected); color: var(--text); }
  .open:focus-visible { outline: 2px solid var(--focus); outline-offset: 0; }
  .open.phone { position: relative; justify-content: center; width: 44px; height: 44px; padding: 0; }
  .corner { position: absolute; top: 4px; right: 2px; }
  .n { font-variant-numeric: tabular-nums; }
  .sheet.full { top: 0; border: 0; border-radius: 0; padding-top: env(safe-area-inset-top); }
  .scrim { all: unset; position: fixed; inset: 0; z-index: 5; background: var(--scrim); }
  .head { flex: none; display: flex; align-items: center; gap: 8px; padding: 12px 14px 6px; }
  .head strong { font-size: 16px; }
  .sheet .head { padding: 6px 16px 8px; }
  .sheet .head strong { font-size: 18px; }
  .counts { color: var(--muted); font-size: 13px; font-variant-numeric: tabular-nums; }
  .space { flex: 1; }
  .filter { position: relative; display: flex; align-items: center; color: var(--muted); }
  .filter select { appearance: none; height: 26px; padding: 0 22px 0 8px; border: 0; border-radius: 6px; background: none; color: inherit; font: inherit; font-size: 12.5px; font-weight: 500; cursor: pointer; }
  .filter select:hover { background: var(--hover); color: var(--text); }
  .filter select:focus-visible { outline: 2px solid var(--focus); }
  .filter :global(svg) { position: absolute; right: 6px; pointer-events: none; }
  .hidden { position: absolute; width: 1px; height: 1px; overflow: hidden; clip-path: inset(50%); }
  .list { min-height: 0; overflow-y: auto; padding: 0 6px 6px; }
  .list:focus { outline: none; }
  .session { display: flex; align-items: center; gap: 6px; margin: 10px 10px 2px; color: var(--muted); font-size: 12.5px; font-weight: 600; }
  .session:first-child { margin-top: 4px; }
  .job { all: unset; box-sizing: border-box; display: flex; align-items: center; gap: 10px; width: 100%; min-height: 44px; padding: 5px 10px; border-radius: 6px;
    color: var(--muted); cursor: pointer; }
  .job:hover, .job:focus-visible { background: var(--selected); }
  .job:focus-visible { outline: 2px solid var(--focus); outline-offset: -2px; }
  .dot { flex: none; display: grid; place-items: center; width: 14px; }
  .lines { flex: 1; min-width: 0; display: grid; line-height: 1.3; }
  .title { overflow: hidden; color: var(--text); font-size: 13px; font-weight: 500; text-overflow: ellipsis; white-space: nowrap; }
  code { overflow: hidden; color: var(--muted); font: 12px/1.35 var(--mono); text-overflow: ellipsis; white-space: nowrap; }
  .state { flex: none; font-size: 12.5px; font-variant-numeric: tabular-nums; text-align: right; }
  .bad { color: var(--danger); }
  .older { all: unset; margin: 4px 10px; color: var(--focus); font-size: 12.5px; font-weight: 500; cursor: pointer; }
  .older:hover { text-decoration: underline; }
  .older:focus-visible { outline: 2px solid var(--focus); outline-offset: 2px; }
  .none { margin: 8px 10px 10px; color: var(--muted); font-size: 13px; }
</style>
