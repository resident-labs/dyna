<script lang="ts">
  import LoadOlder from '../ui/LoadOlder.svelte'
  import EmptyState from '../ui/EmptyState.svelte'
  import InlineConfirm from '../ui/InlineConfirm.svelte'
  // Background `dyna shell` commands in the room, with the tail of the
  // selected one's log. A running job's log follows itself every two
  // seconds while it stays open. jobs is App's one room list, newest first.
  // onstop(job) stops a running job and resolves when it has ended.
  import { onMount, tick, untrack } from 'svelte'
  import { api } from '../../api.js'
  import { ago, duration } from '../ui/format.js'
  import { runningJob } from '../rooms/workViews.ts'
  import type { ShellJob, ShellLog } from '../api/terminal.ts'
  import { errorText } from '../errorText.ts'

  interface Props {
    older?: boolean
    loadingOlder?: boolean
    onolder?: () => void
    jobs?: ShellJob[] | null
    error?: string
    jobId?: string
    narrow?: boolean
    onstop?: ((job: ShellJob) => Promise<void>) | null
  }
  let { older = false, loadingOlder = false, onolder = () => {}, jobs = null, error = '', jobId = '', narrow = false, onstop = null }: Props = $props()

  let sel = $state('')
  let log = $state<ShellLog | null>(null)
  let logError = $state('')
  let logEl = $state<HTMLPreElement>()
  let follow = $state(true)
  let logLoading = $state(false)
  let logRequest = 0
  let loadingJob = ''
  const historyPageSize = 20
  let historyOpen = $state(false)
  let historyLimit = $state(historyPageSize)
  let requestedJobId = ''
  // stopAsk is the job whose Stop waits for a confirm.
  let stopAsk = $state('')
  let stopBusy = $state(false)
  let stopError = $state('')
  let handledJobId = ''

  let job = $derived((jobs ?? []).find((j) => j.id === sel))
  let running = $derived((jobs ?? []).filter(runningJob))
  let finished = $derived((jobs ?? []).filter((j) => !runningJob(j)))
  let history = $derived(finished.slice(0, historyLimit))
  let selectedOutsideHistory = $derived(job && finished.includes(job) && (!historyOpen || !history.includes(job)))
  // A wide view opens the newest active job when nothing is chosen.
  $effect(() => {
    const active = running[0]
    untrack(() => { if (active && !sel && !jobId && !narrow) pick(active) })
  })

  // Handle each link once. Polls and the mobile Back button must not reopen it.
  $effect(() => {
    const requested = jobId
    const found = (jobs ?? []).find((j) => j.id === requested)
    untrack(() => {
      if (requested !== requestedJobId) {
        requestedJobId = requested
        handledJobId = ''
        if (requested) {
          sel = ''
          log = null
          logError = ''
        }
      }
      if (found && requested !== handledJobId) {
        handledJobId = requested
        pick(found)
      }
    })
  })

  // A command is an argument list; quote the parts a shell would split.
  const cmd = (c: string[] | string | undefined) => (Array.isArray(c) ? c.map((a) => (/^[\w./:=@%+,-]+$/.test(a) ? a : `'${a.replace(/'/g, `'\\''`)}'`)).join(' ') : c || '')
  const stateText: Record<string, string> = { running: 'running', starting: 'starting', exited: 'exited', lost: 'lost' }

  async function loadLog() {
    if (!sel || (logLoading && loadingJob === sel)) return
    const id = sel
    loadingJob = id
    const request = ++logRequest
    logLoading = true
    try {
      const l = await api.shellLog(id, 400)
      if (id !== sel || request !== logRequest) return
      log = l
      logError = ''
      await tick()
      if (id === sel && request === logRequest && follow) resumeFollowing()
    } catch (e) {
      if (id !== sel || request !== logRequest) return
      logError = errorText(e)
    } finally {
      if (id === sel && request === logRequest) { logLoading = false; loadingJob = '' }
    }
  }

  function trackFollow() {
    if (logEl) follow = logEl.scrollHeight - logEl.scrollTop - logEl.clientHeight < 40
  }
  function resumeFollowing() {
    follow = true
    if (logEl) logEl.scrollTop = logEl.scrollHeight
  }

  async function stop(j: ShellJob) {
    stopBusy = true
    try {
      await onstop?.(j)
      stopAsk = ''
    } catch (e) {
      stopError = errorText(e)
    } finally {
      stopBusy = false
      loadLog()
    }
  }

  function pick(j: ShellJob) {
    sel = j.id
    stopAsk = ''
    stopError = ''
    log = null
    logError = ''
    follow = true
    loadLog()
  }

  onMount(() => {
    const t = setInterval(() => { if (job && runningJob(job)) loadLog() }, 2000)
    return () => clearInterval(t)
  })

  function codeText(j: ShellJob) {
    if (runningJob(j)) return duration(j.started, null)
    if (j.state === 'lost') return 'lost'
    return `exit ${j.code ?? '?'}`
  }
  const tone = (j: ShellJob) => (runningJob(j) ? 'live' : j.state === 'lost' || (j.code ?? 0) !== 0 ? 'bad' : 'ok')
</script>

{#snippet row(j: ShellJob)}
  <button class="job" class:sel={j.id === sel} aria-pressed={j.id === sel} onclick={() => pick(j)}>
    <span class="st {tone(j)}" aria-label={stateText[j.state] ?? j.state}></span>
    <span class="main">
      <span class="cmd mono">{cmd(j.command)}</span>
      <span class="sub">{j.id} · started {ago(j.started)}</span>
    </span>
    <span class="code {tone(j)}">{codeText(j)}</span>
  </button>
{/snippet}

<section class="jobs" class:narrow>
  {#if !narrow || !job}
    <div class="list">
      <header>
        <h1>Shell jobs</h1>
        <p>Existing managed background commands in this room. New starts are disabled.</p>
      </header>
      {#if jobId && jobs && !jobs.some((j) => j.id === jobId)}
        <p class="err">Job {jobId} is not available in this room.</p>
      {/if}
      {#if error}
        <p class="err">{error}</p>
      {:else if !jobs}
        <p class="quiet">Loading jobs…</p>
      {:else if !jobs.length}
        <EmptyState title="No shell jobs">New managed shell jobs are disabled. Run commands directly in your session.</EmptyState>
      {:else}
        <div class="rows">
          <h2>Active jobs <span class="total">{running.length}</span></h2>
          {#each running as j (j.id)}
            {@render row(j)}
          {:else}
            <p class="idle">No active jobs.</p>
          {/each}
          {#if job && selectedOutsideHistory}
            <h2>Selected job</h2>
            {@render row(job)}
          {/if}
          {#if finished.length || older}
            <button class="history-toggle" aria-expanded={historyOpen} onclick={() => { historyOpen = !historyOpen; historyLimit = historyPageSize }}>
              <span aria-hidden="true">{historyOpen ? '▾' : '▸'}</span>
              Finished history <span class="total">{finished.length}</span>
            </button>
            {#if historyOpen}
              {#each history as j (j.id)}
                {@render row(j)}
              {/each}
              {#if historyLimit < finished.length}
                <button class="older" onclick={() => historyLimit += historyPageSize}>Show older ({finished.length - history.length} remaining)</button>
              {/if}
            {/if}
          {/if}
        </div>
      {/if}
      <LoadOlder {older} loading={loadingOlder} onclick={onolder} />
    </div>
  {/if}
  {#if job}
    <div class="log">
      <header class="lh">
        {#if narrow}
          <button class="back" onclick={() => (sel = '')} aria-label="Back to the job list">
            <svg viewBox="0 0 20 20" width="20" height="20" aria-hidden="true"><path d="M12.5 4.5 7 10l5.5 5.5" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" /></svg>
          </button>
        {/if}
        <span class="st {tone(job)}"></span>
        <span class="lt">
          <span class="cmd mono">{cmd(job.command)}</span>
          <span class="sub mono" title={job.cwd}>{job.cwd}</span>
        </span>
        <span class="code {tone(job)}">{codeText(job)}</span>
        {#if onstop && runningJob(job)}
          {#if stopAsk === job.id}
            <InlineConfirm ask="Stop this job?" action="Stop" busy={stopBusy} onconfirm={() => stop(job)} oncancel={() => (stopAsk = '')} />
          {:else}
            <button class="stop" onclick={() => { stopAsk = job.id; stopError = '' }}>Stop</button>
          {/if}
        {/if}
      </header>
      {#if stopError}<p class="err" role="alert">Could not stop the job: {stopError}</p>{/if}
      {#if logError}
        <div class="log-tools"><p class="err">{logError}</p><button class="log-action" disabled={logLoading} aria-label="Retry log" onclick={loadLog}>Retry</button></div>
      {/if}
      {#if log && !follow}<div class="log-tools"><button class="log-action" onclick={resumeFollowing}>Resume following</button></div>{/if}
      {#if !log && !logError}<p class="quiet" role="status">Loading log…</p>{/if}
      {#if log}
        {#if log.truncated}<p class="cut">Showing the last 400 lines.</p>{/if}
        <!-- svelte-ignore a11y_no_noninteractive_tabindex (The log scrolls with the keyboard.) -->
        <pre bind:this={logEl} tabindex="0" role="region" aria-label="Log for {job.id}" onscroll={trackFollow}>{log.text || '(no output yet)'}</pre>
      {/if}
    </div>
  {:else if !narrow && jobs?.length}
    <div class="log none"><p class="quiet">Pick a job to see its log.</p></div>
  {/if}
</section>

<style>
  .log-tools { display: flex; flex-wrap: wrap; align-items: center; gap: var(--s2); padding: 8px 12px; }
  .log-tools .err { margin: 0; padding: 0; overflow-wrap: anywhere; }
  .log-action { min-height: 44px; padding: 8px 12px; background: var(--raise); color: var(--ink); border-radius: var(--r-xs); }
  .log-action:disabled { opacity: 0.5; }
  .jobs { flex: 1; display: flex; min-height: 0; min-width: 0; }
  .list { width: 400px; flex: none; display: flex; flex-direction: column; border-right: 1px solid var(--line-soft); min-height: 0; }
  .narrow .list { width: 100%; border: 0; }
  header { padding: 16px 16px 8px; }
  h1 { margin: 0; font-size: 18px; font-weight: 700; }
  header p { margin: 4px 0 0; color: var(--muted); font-size: 13px; }
  .rows { flex: 1; min-height: 0; overflow-y: auto; padding: 4px 8px 16px; }
  h2, .history-toggle { margin: 12px 10px 6px; font-size: 13px; font-weight: 600; color: var(--muted); }
  .total { font-size: 12px; color: var(--faint); font-variant-numeric: tabular-nums; }
  h2 .total { margin-left: 6px; }
  .history-toggle { min-height: 44px; width: calc(100% - 20px); display: flex; align-items: center; gap: 8px; text-align: left; padding: 10px 0; }
  .history-toggle .total { margin-left: auto; }
  .idle { margin: 6px 10px 16px; color: var(--faint); font-size: 13px; }
  .older { display: block; width: 100%; min-height: 44px; margin-top: 8px; color: var(--ink-2); font-size: 13px; border: 1px solid var(--line-soft); border-radius: 8px; transition: background-color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .older:hover { background: var(--hover); }
  }
  .older:active { background: var(--select); }
  .job { width: 100%; display: flex; align-items: center; gap: 12px; padding: 9px 10px; border-radius: 8px; text-align: left; transition: background-color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .job:hover { background: var(--hover); }
  }
  .job:active { background: var(--select); }
  .job.sel { background: var(--select); }
  .st { width: 9px; height: 9px; border-radius: 50%; flex: none; background: var(--faint); }
  .st.live { background: var(--gold); box-shadow: 0 0 0 3px rgba(230, 192, 123, 0.16); }
  .st.ok { background: var(--sage); }
  .st.bad { background: var(--coral); }
  .main, .lt { flex: 1; min-width: 0; display: flex; flex-direction: column; }
  .cmd { color: var(--ink); font-size: 13px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .sub { color: var(--faint); font-size: 12px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .code { flex: none; font-size: 12px; font-weight: 600; font-variant-numeric: tabular-nums; color: var(--muted); }
  .code.live { color: var(--gold); }
  .code.bad { color: var(--coral); }
  .code.ok { color: var(--sage); }
  .log { flex: 1; display: flex; flex-direction: column; min-width: 0; min-height: 0; }
  .log.none { align-items: center; justify-content: center; }
  .lh { display: flex; align-items: center; gap: 10px; padding: 12px 16px; border-bottom: 1px solid var(--line-soft); }
  .stop { flex: none; font-size: 13px; font-weight: 600; padding: 5px 12px; border-radius: 7px; color: var(--coral); background: rgba(239, 138, 128, 0.1); border: 1px solid rgba(239, 138, 128, 0.28); transition: background-color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .stop:hover { background: color-mix(in srgb, var(--coral) 16%, transparent); }
  }
  .stop:active { background: var(--select); }
  .back { width: 44px; height: 44px; margin: -8px 0 -8px -10px; display: grid; place-items: center; color: var(--muted); flex: none; }
  pre { flex: 1; margin: 0; overflow: auto; padding: 12px 16px; background: #19181a; font-family: var(--mono); font-size: 12.5px; line-height: 1.55; color: var(--ink-2); white-space: pre-wrap; overflow-wrap: anywhere; font-variant-ligatures: none; }
  .cut { margin: 0; padding: 5px 16px; font-size: 12px; color: var(--faint); background: #19181a; border-bottom: 1px solid var(--line-soft); }
  .quiet { color: var(--faint); font-size: 13.5px; padding: 12px 16px; }
  .err { color: var(--coral); padding: 12px 16px; }
  @media (max-width: 760px) {
    header { padding: 12px 14px 6px; }
    .job { min-height: 56px; }
    .stop { min-height: 44px; }
    .lh { flex-wrap: wrap; }
    pre { font-size: 12px; padding: 10px 12px; }
  }
</style>
