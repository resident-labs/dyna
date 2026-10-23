<script lang="ts">
  import DoingLine from './DoingLine.svelte'
  import FailedCall from './FailedCall.svelte'
  import ShellOutput from './ShellOutput.svelte'
  import ShellResultRow from '../rooms/ShellResultRow.svelte'
  import StepIcon from './StepIcon.svelte'
  import { blockState, mergeJob, metaOf, titleOf } from './shell.ts'
  import { elapsed, stamp } from '../ui/format.ts'
  import type { Doing } from '../api/room.ts'
  import type { ShellJob } from '../api/terminal.ts'
  import type { TimelineRow } from './dm.ts'

  interface Props {
    row: TimelineRow
    jobs?: ShellJob[]
    doing?: Doing
    onjob?: ((job: ShellJob) => void) | null
    onthread?: ((key: string) => void) | null
  }
  let { row, jobs = [], doing, onjob = null, onthread = null }: Props = $props()

  // stepWord names a step that has no label of its own, so a row never shows a
  // raw tool ID.
  const stepWords: Record<string, string> = {
    read: 'Read', edit: 'Edit', write: 'Write', search: 'Search', list: 'List', run: 'Command',
    fetch: 'Fetch', delegate: 'Delegate', plan: 'Plan', monitor: 'Monitor', tool: 'Tool',
  }
  const stepWord = (kind?: string) => stepWords[kind ?? ''] ?? 'Tool'
  // stepTime reads a step's own duration. A call under a second reads in
  // milliseconds, so a quick step does not look like a clock that failed to move.
  const stepTime = (ms?: number): string => {
    if (!ms || ms <= 0) return ''
    return ms < 1000 ? `${ms} ms` : `${(ms / 1000).toFixed(1)} s`
  }
  let opened = $state<Record<string, boolean>>({})
  let now = $state(Date.now())
  let running = $derived(row.turn?.status === 'running' || !!doing || row.steps.some((s) => s.kind === 'shell' && ['starting', 'running'].includes((jobs.find((j) => j.id === s.jobId) ?? s.job)?.state ?? '')))
  $effect(() => {
    if (!running) return
    const timer = setInterval(() => now = Date.now(), 1000)
    return () => clearInterval(timer)
  })
  let span = $derived(row.turn ? elapsed(Math.max(0, (row.turn.status === 'running' ? now : row.turn.ended ? Date.parse(row.turn.ended) : new Date(row.steps.at(-1)?.ts ?? row.turn.started).getTime()) - Date.parse(row.turn.started))) : '')
</script>

<section class="timeline" aria-label={row.turn ? `Turn ${row.turn.n} activity` : 'Shell activity'}>
  <header>
    <span>{row.turn ? `Turn ${row.turn.n}` : 'Shell activity'}</span>
    <span>· {row.steps.length} step{row.steps.length === 1 ? '' : 's'}</span>
    {#if span}<span>· {span}</span>{/if}
  </header>
  <ol>
    {#each row.steps as step (step.key)}
      {@const job = step.kind === 'shell' ? (step.job ? mergeJob(step.job, jobs.find((j) => j.id === step.jobId)) : jobs.find((j) => j.id === step.jobId)) : undefined}
      {@const activeTool = step.kind === 'tool' && !step.failed && !step.done && row.turn?.status === 'running'}
      {@const state = job ? blockState(job) : step.kind === 'child' ? (step.member.state === 'live' ? 'running' : step.member.status === 'error' ? 'failed' : 'done') : step.failed ? 'failed' : activeTool ? 'running' : 'done'}
      {@const expanded = opened[step.key] ?? state === 'failed'}
      <li class={state} id={step.kind === 'shell' ? `shell-${step.jobId}` : undefined}>
        <details open={expanded} ontoggle={(event) => opened[step.key] = event.currentTarget.open}>
          <summary>
            <span class="dot" aria-label={state}></span>
            {#if step.kind === 'shell'}
              <span class="verb">Shell command</span>
              <span class="target" title={job ? titleOf(job) : undefined}>{job ? titleOf(job) : 'outside this transcript'}</span>
              {#if job}<span class="meta">{metaOf(job, now)}</span>{/if}
            {:else if step.kind === 'child'}
              <span class="verb">child</span>
              <span class="target" title={step.member.name}>{step.member.name}</span>
              <span class="meta">{step.member.state === 'live' ? 'running' : step.member.status}</span>
            {:else}
              <StepIcon kind={step.step} />
              <span class="label" title={step.target || step.label}>{step.label || step.target || stepWord(step.step)}</span>
              <span class="chip {state}">{state === 'running' ? 'running' : state === 'failed' ? 'failed' : 'done'}</span>
              {#if stepTime(step.durationMs)}<span class="meta">{stepTime(step.durationMs)}</span>{/if}
            {/if}
            <span class="chevron" aria-hidden="true">›</span>
          </summary>
          {#if expanded}
            <div class="detail">
              {#if step.kind === 'shell'}
                {#if job}<ShellOutput {job} {onjob} />
                {:else}{#each step.posts as post (post.id)}{#if post.kind === 'shell'}<ShellResultRow post={post} surface="dm" />{:else}<p class="muted">{post.text || 'The shell command is outside this transcript page.'}</p>{/if}{/each}{/if}
                {#if step.finished}<p class="muted">Finished at {stamp(step.finished.at)}{#if step.finished.woke && step.finished.turn} · woke turn {step.finished.turn}{:else} · told the agent during the turn{/if}</p>{/if}
              {:else if step.kind === 'child'}
                <p class="muted">{step.member.profile || 'Session'} · created {stamp(step.member.created)}</p>
                {#each step.posts as post (post.id)}<pre>{post.text}</pre>{/each}
                {#if onthread}<button class="link" onclick={() => onthread?.(step.member.address)}>Open {step.member.name} thread</button>{/if}
              {:else}
                <!-- The raw tool name, the full target and the exit code stay
                     here: the row above shows the designed step only. -->
                <p class="muted">{stepWord(step.step)}{#if step.toolName} · {step.toolName}{/if}{#if step.tool && step.tool !== step.label} · {step.tool}{/if}</p>
                {#if step.target}<pre class="full-target">{step.target}</pre>{/if}
                {#if step.failed}<FailedCall command={step.command ?? step.target} output={step.output} exit={step.exit} />
                {:else if step.command || step.output}<pre>{[step.command, step.output].filter(Boolean).join('\n\n')}</pre>
                {:else if !step.target}<p class="muted">No more details were captured.</p>{/if}
              {/if}
            </div>
          {/if}
        </details>
      </li>
    {/each}
  </ol>
  {#if doing?.text}<div class="live" role="status"><DoingLine {doing} {now} /></div>{/if}
</section>

<style>
  .timeline { max-width: 920px; margin: var(--s1) 0; color: var(--ink-2); font-size: var(--t-sm); }
  header { display: flex; flex-wrap: wrap; gap: 0 5px; padding: 0 0 var(--s2); color: var(--muted); font-size: var(--t-cap); font-variant-numeric: tabular-nums; }
  ol { list-style: none; padding: 0; margin: 0 0 0 5px; border-left: 1px solid var(--line); }
  li { position: relative; padding-left: 16px; }
  summary { display: flex; align-items: baseline; gap: var(--s2); min-width: 0; padding: 7px 0; cursor: pointer; font-family: var(--mono); list-style: none; transition: color var(--motion-fast); }
  summary::-webkit-details-marker { display: none; }
  summary:hover { color: var(--ink); }
  summary:focus-visible { outline: 2px solid var(--iris); outline-offset: 2px; border-radius: var(--r-xs); }
  .dot { position: absolute; left: -4px; top: 13px; width: 7px; height: 7px; border-radius: 50%; background: var(--sage); box-shadow: 0 0 0 3px var(--main); }
  .running .dot, .background .dot { background: var(--iris); }
  .failed .dot { background: var(--coral); }
  .stopped .dot { background: var(--gold); }
  .verb { flex: none; color: var(--faint); }
  .label { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--ink-2); }
  .chip { flex: none; padding: 0 6px; border-radius: var(--r-xs); background: var(--raise); color: var(--muted); font: var(--t-cap)/1.7 var(--sans); }
  .chip.failed { background: var(--coral-wash, var(--raise)); color: var(--coral); }
  .chip.running { color: var(--iris); }
  .target { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .meta { margin-left: auto; flex: none; color: var(--faint); font: var(--t-cap)/1.5 var(--sans); font-variant-numeric: tabular-nums; }
  .failed .meta { color: var(--coral); }
  .running .meta, .background .meta { color: var(--iris); }
  .chevron { flex: none; color: var(--faint); }
  details[open] .chevron { transform: rotate(90deg); }
  .detail { padding: var(--s1) 0 var(--s3); min-width: 0; }
  pre { margin: 0 0 var(--s2); padding: var(--s2) var(--s3); border-radius: var(--r-xs); background: var(--rail); font: var(--t-sm)/1.6 var(--mono); white-space: pre-wrap; overflow-wrap: anywhere; max-height: 360px; overflow-y: auto; user-select: text; }
  .muted { margin: var(--s1) 0; color: var(--muted); }
  .link { padding: var(--s1) 0; color: var(--iris); text-align: left; }
  .live { padding: var(--s2) 0 0 21px; }
  @media (max-width: 760px) {
    summary { flex-wrap: wrap; min-height: 44px; gap: 2px var(--s2); }
    .dot { top: 18px; }
    .target { flex: 1; }
    .meta { order: 3; flex-basis: 100%; margin-left: 0; }
    .chevron { margin-left: auto; }
    .link { min-height: 44px; }
  }
</style>
