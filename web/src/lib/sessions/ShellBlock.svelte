<script lang="ts">
  // One Dyna shell command in a DM: a quiet title line, the muted command, and
  // a dark output well that shows while the job runs, when it fails, or when
  // the reader opens it. The job is the snapshot of the transcript item with
  // the live row laid over it.
  import { api } from '../../api.js'
  import { blockState, bytesOf, commandOf, finishedText, isLogChunk, isOpen, jumpOf, metaOf, plainLog, sizeOf, tailLines, titleOf } from './shellBlock.ts'
  import type { Finished, ShellJob } from './shellBlock.ts'

  interface Props {
    job: ShellJob
    onjob?: ((job: ShellJob) => void) | null
    // How the job's end notice reached the agent, when it folds into this block.
    finished?: Finished | null
  }
  let { job, onjob = null, finished = null }: Props = $props()

  const marks = { running: '●', background: '◐', done: '✓', failed: '✕', stopped: '■' }
  let kind = $derived(blockState(job))
  let live = $derived(isOpen(job))
  let command = $derived(commandOf(job))
  let open = $state(false)
  let shown = $derived(live || kind === 'failed' || open)
  let now = $state(Date.now())
  $effect(() => {
    if (!live) return
    const timer = setInterval(() => (now = Date.now()), 1000)
    return () => clearInterval(timer)
  })

  let full = $state<string | null>(null)
  let next = $state(0)
  let size = $state(0)
  let loading = $state(false)
  let failure = $state('')
  let gone = $state(false)
  let pruned = $derived(job.logPruned === true || gone)
  // An old job's row carries no tail, so opening it reads the log.
  function toggle() {
    open = !open
    if (open && !live && !job.tail && full === null && !pruned) void showMore()
  }
  const jump = (target: string): void => {
    const calm = matchMedia('(prefers-reduced-motion: reduce)').matches
    document.getElementById(target)?.scrollIntoView({ block: 'center', behavior: calm ? 'auto' : 'smooth' })
  }
  async function showMore() {
    loading = true
    failure = ''
    try {
      const chunk: unknown = await api.shellLogRange(job.id, next, 0)
      if (!isLogChunk(chunk)) throw new Error('unexpected log reply')
      full = (full ?? '') + chunk.text
      next = chunk.next
      size = chunk.size
    } catch (e) {
      const message = e instanceof Error ? e.message : String(e)
      if (/no such file|not exist/i.test(message)) gone = true
      else failure = message
    } finally {
      loading = false
    }
  }
  let copied = $state(false)
  async function copy() {
    await navigator.clipboard.writeText(command)
    copied = true
    setTimeout(() => (copied = false), 1500)
  }
</script>

<div class="blk {kind}" class:live id="shell-{job.id}">
  <div class="hd">
    <button class="title" aria-expanded={shown} disabled={live} onclick={toggle} title={command}>
      <span class="st {kind}" aria-hidden="true">{marks[kind]}</span>
      <span class="desc">{titleOf(job)}</span>
      <span class="meta" class:gold={live} class:coral={kind === 'failed'}>{metaOf(job, now, !!finished)}</span>
    </button>
    <span class="acts">
      <button onclick={copy} title="Copy the command" aria-label="Copy the command">⧉<span class="lbl">{copied ? ' Copied' : ' Copy'}</span></button>
      {#if onjob}<button onclick={() => onjob?.(job)} title="Open the log" aria-label="Open the log">▤<span class="lbl"> Log</span></button>{/if}
    </span>
  </div>
  <div class="cmd"><b>$</b>{command}</div>
  {#if finished}
    {@const where = jumpOf(job, finished)}
    <div class="fin"><span class:coral={kind === 'failed'}>{finishedText(job, finished)}</span> · <button class="lnk" onclick={() => jump(where.target)}>{where.text}</button></div>
  {/if}
  {#if shown}
    <div class="well">
      {#if full !== null}
        <pre class="full">{plainLog(full)}</pre>
      {:else}
        <pre class="tail" class:cur={live}>{tailLines(job).join('\n')}</pre>
      {/if}
      <div class="foot">
        <span>{pruned ? 'log pruned' : full !== null && size ? `${bytesOf(next)} of ${bytesOf(size)}` : sizeOf(job)}</span>
        {#if !live && !pruned && (full === null || next < size)}<button class="lnk" onclick={showMore} disabled={loading}>{full === null ? 'Show all' : 'Show more'}</button>{/if}
        {#if failure}<span class="coral" role="status">{failure}</span>{/if}
        <span class="note">May contain secrets.</span>
      </div>
    </div>
  {/if}
</div>

<style>
  .blk { max-width: 920px; margin: 2px 0; font-size: var(--t-ui); color: var(--ink-2); }
  .hd { display: flex; align-items: baseline; gap: var(--s2); min-width: 0; }
  .title { display: flex; align-items: baseline; gap: var(--s2); min-width: 0; flex: 0 1 auto; padding: 2px 0; background: none; border: 0; color: inherit; font: inherit; text-align: left; cursor: pointer; }
  .title:disabled { cursor: default; opacity: 1; }
  .st { flex: none; width: 14px; text-align: center; font-size: var(--t-cap); color: var(--faint); }
  .st.running { color: var(--gold); animation: pulse 1.6s ease-in-out infinite; }
  .st.background { color: var(--gold); }
  .st.failed { color: var(--coral); }
  .desc { min-width: 0; overflow: hidden; white-space: nowrap; text-overflow: ellipsis; }
  .meta { flex: none; color: var(--faint); font-size: var(--t-sm); font-variant-numeric: tabular-nums; white-space: nowrap; }
  .meta.gold { color: var(--gold); }
  .meta.coral { color: var(--coral); }
  .acts { margin-left: auto; flex: none; display: flex; gap: 12px; font-size: var(--t-sm); color: var(--faint); opacity: 0; transition: opacity var(--motion-fast); }
  .acts button { background: none; border: 0; padding: 0; color: inherit; font: inherit; cursor: pointer; transition: color var(--motion-fast); }
  .acts button:hover, .acts button:focus-visible { color: var(--ink); }
  .blk:hover .acts, .blk:focus-within .acts, .blk.live .acts { opacity: 1; }
  .cmd { font: var(--t-sm)/1.6 var(--mono, ui-monospace, monospace); color: var(--faint); padding-left: 22px; white-space: pre-wrap; overflow-wrap: anywhere; max-height: 9.6em; overflow-y: auto; }
  .fin { padding-left: 22px; font-size: var(--t-sm); color: var(--faint); }
  .fin .coral { color: var(--coral); }
  .fin .lnk { background: none; border: 0; padding: 0; color: var(--iris); font: inherit; cursor: pointer; }
  .fin .lnk:hover { text-decoration: underline; }
  .cmd b { font-weight: 400; color: #6f6a63; margin-right: 6px; }
  .well { margin: 4px 0 4px 22px; background: var(--rail); border-radius: 6px; padding: 7px 12px 6px; font: var(--t-sm)/1.6 var(--mono, ui-monospace, monospace); color: var(--ink-2); }
  .blk.failed .well { box-shadow: inset 2px 0 0 rgba(239, 138, 128, 0.55); }
  .blk.background .well { color: var(--muted); }
  pre { margin: 0; font: inherit; white-space: pre-wrap; overflow-wrap: anywhere; max-height: 360px; overflow-y: auto; }
  pre.cur::after { content: '▍'; color: var(--faint); animation: blink 1.1s steps(1) infinite; }
  .foot { display: flex; flex-wrap: wrap; gap: 14px; margin-top: 4px; font: var(--t-sm)/1.5 var(--sans, system-ui, sans-serif); color: var(--faint); }
  .foot .lnk { background: none; border: 0; padding: 0; color: var(--iris); font: inherit; cursor: pointer; }
  .foot .lnk:hover:not(:disabled) { text-decoration: underline; }
  .foot .coral { color: var(--coral); }
  .foot .note { margin-left: auto; }
  @keyframes pulse { 50% { opacity: 0.35; } }
  @keyframes blink { 50% { opacity: 0; } }
  @media (prefers-reduced-motion: reduce) { .st.running, pre.cur::after { animation: none; } }
  @media (max-width: 760px) {
    .hd { flex-wrap: wrap; row-gap: 0; }
    .title { flex-wrap: wrap; flex: 1 1 0; }
    .meta { order: 3; flex-basis: 100%; padding-left: 22px; }
    .acts { opacity: 1; gap: 14px; }
    .acts .lbl { display: none; }
    .well { margin-left: 0; }
    .foot .note { margin-left: 0; }
  }
</style>
