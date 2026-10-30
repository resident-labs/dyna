<script lang="ts">
  import StatusPill from '../ui/StatusPill.svelte'
  import DoingLine from './DoingLine.svelte'
  import FailedCall from './FailedCall.svelte'
  import ShellOutput from './ShellOutput.svelte'
  import ShellResultRow from '../rooms/ShellResultRow.svelte'
  import StepIcon from './StepIcon.svelte'
  import { blockState, mergeJob, metaOf, titleOf } from './shell.ts'
  import { toolCallLabel } from './toolCallLabel.ts'
  import { stamp } from '../ui/format.ts'
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
</script>

<section class="timeline" aria-label={row.turn ? `Turn ${row.turn.n} activity` : 'Shell activity'}>
  <header>
    <span>{row.steps.length} step{row.steps.length === 1 ? '' : 's'}</span>
  </header>
  <ol>
    {#each row.steps as step (step.key)}
      {@const job = step.kind === 'shell' ? (step.job ? mergeJob(step.job, jobs.find((j) => j.id === step.jobId)) : jobs.find((j) => j.id === step.jobId)) : undefined}
      {@const activeTool = step.kind === 'tool' && !step.failed && !step.done && row.turn?.status === 'running'}
      {@const state = job ? blockState(job) : step.kind === 'child' ? (step.member.state === 'live' ? 'running' : step.member.status === 'error' ? 'failed' : 'done') : step.failed ? 'failed' : activeTool ? 'running' : 'done'}
      {@const expanded = opened[step.key] ?? state === 'failed'}
      {@const call = toolCallLabel(step)}
      <li class={state} id={step.kind === 'shell' ? `shell-${step.jobId}` : undefined}>
        <details open={expanded} ontoggle={(event) => opened[step.key] = event.currentTarget.open}>
          <summary>
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
              <span class="label" title={call.paths.join(', ') || step.target || call.title}>{call.title}</span>
              {#if stepTime(step.durationMs)}<span class="meta">{stepTime(step.durationMs)}</span>{/if}
            {/if}
            {#if state === 'running'}<StatusPill label="running" variant="dot" pulse />{:else if state === 'failed'}<StatusPill label="failed" tone="danger" />{/if}
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
                <!-- The harness tool name stays here when it is not the kind
                     word. The title already says the kind once. -->
                {#if call.detail}<p class="muted">{call.detail}</p>{/if}
                {#if call.paths.length}<pre class="full-target">{call.paths.join('\n')}</pre>{/if}
                {#if step.failed}<FailedCall command={step.command ?? step.target} output={step.output} exit={step.exit} />
                {:else if step.command || step.output}<pre>{[step.command, step.output].filter(Boolean).join('\n\n')}</pre>
                {:else if call.paths.length === 0 && !call.detail}<p class="muted">No more details were captured.</p>{/if}
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
  .verb { flex: none; color: var(--faint); }
  .label { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--ink-2); }
  .target { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .meta { margin-left: auto; flex: none; color: var(--faint); font: var(--t-cap)/1.5 var(--sans); font-variant-numeric: tabular-nums; }
  .failed .meta { color: var(--coral); }
  .chevron { flex: none; color: var(--faint); }
  details[open] .chevron { transform: rotate(90deg); }
  .detail { padding: var(--s1) 0 var(--s3); min-width: 0; }
  pre { margin: 0 0 var(--s2); padding: var(--s2) var(--s3); border-radius: var(--r-xs); background: var(--rail); font: var(--t-sm)/1.6 var(--mono); white-space: pre-wrap; overflow-wrap: anywhere; max-height: 360px; overflow-y: auto; user-select: text; }
  .muted { margin: var(--s1) 0; color: var(--muted); }
  .link { padding: var(--s1) 0; color: var(--iris); text-align: left; }
  .live { padding: var(--s2) 0 0 21px; }
  @media (max-width: 760px) {
    summary { min-height: 44px; gap: var(--s2); }
    .meta { overflow: hidden; text-overflow: ellipsis; }
    .chevron { margin-left: auto; }
    .link { min-height: 44px; }
  }
</style>
