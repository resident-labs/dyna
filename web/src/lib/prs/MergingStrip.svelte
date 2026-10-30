<script lang="ts">
  import Chip from '../ui/Chip.svelte'
  import { toneOf } from '../ui/tone.ts'
  import type { MergeControlStatus } from '../api/change.ts'
  import { shortSha } from './pr.ts'
  import { trialProgress, waitingReason, waitingWords } from './roomPRs.ts'
  // failed says the merge status call failed; onretry asks again. The raw error stays off the screen (rule r8).
  let { status, failed = false, onretry = () => {}, onpick, onresume, resuming = false, resumeError = '' }: { status: MergeControlStatus | null; failed?: boolean; onretry?: () => void; onpick: (id: string) => void; onresume: () => void; resuming?: boolean; resumeError?: string } = $props()
  let ready = $derived(status?.queue?.filter((q) => q.state === 'ready' && !q.trial) ?? [])
  let reason = $derived(status ? waitingReason(status) : null)
</script>
<section class="merging" aria-label="Merging">
  <div class="heading"><h2>Merging</h2>{#if status?.paused}<Chip tone="warning">Paused</Chip>{/if}</div>
  {#if status?.paused}
    <div class="pause" role="status">
      <div><strong>Merging is paused</strong><p>{status.reason || 'No reason was recorded.'}</p></div>
      <button class="resume" onclick={onresume} disabled={resuming}>{resuming ? 'Resuming…' : 'Resume'}</button>
    </div>
    {#if resumeError}<p class="error" role="alert">Could not resume merging: {resumeError}</p>{/if}
  {/if}
  {#if failed}
    <div class="pause" role="alert">
      <div><strong>Merge status did not load</strong><p>The merge queue and trials are missing.</p></div>
      <button class="resume" onclick={onretry}>Retry</button>
    </div>
  {:else if !status}<p class="muted">Loading merge status…</p>
  {:else}
    <div class="line now"><b>Now</b><div>
    {#each status.trials ?? [] as trial (trial.branch)}
      {@const progress = trialProgress(trial, status)}
      <div class="trial">
        <div class="trial-head"><strong>{progress.label}</strong><Chip tone={toneOf(progress.tone)}>{trial.branch.split('/').at(-1) ? `Trial ${trial.branch.split('/').at(-1)}` : 'Trial'}</Chip></div>
        <div class="batch">{#each trial.batch ?? [] as id}<button onclick={() => onpick(id)}>{id.includes('#') ? `#${id.split('#').at(-1)}` : id}</button>{/each}</div>
        {#if progress.total}
          <p class="checks">{progress.nodes ? `${progress.nodes} · ` : ''}{progress.completed}/{progress.total} checks complete · {progress.passed} passed{#if progress.failed} · <span class="error">{progress.failed} failed</span>{/if}</p>
          <progress max={progress.total} value={progress.completed} aria-label="Trial checks complete"></progress>
        {/if}
        {#if trial.reason}<p class="muted">{trial.reason}</p>{/if}
      </div>
    {:else}<p class="muted">No merge running.</p>{/each}
    </div></div>
    <div class="line"><b>Next</b><span>{#if ready.length}{#each ready as q}<button class="next" onclick={() => onpick(q.changeId)}>#{q.changeId.split('#').at(-1)}</button>{/each}{:else}No other PR is ready.{/if}</span></div>
    {#if reason}
      <div class="line"><b class="gold">Waiting</b><span class="wait">
        <span>{waitingWords(reason)}{#if reason.base}{' '}on {reason.base}{/if}{#if reason.sha}{' '}at <code>{shortSha(reason.sha)}</code>{/if}</span>
        {#if reason.prs.length}<span class="wait-prs">{#each reason.prs as n}<span class="pr">#{n}</span>{/each}</span>{/if}
      </span></div>
    {/if}
  {/if}
</section>
<style>
  .merging { margin: 0 var(--s5) var(--s4); padding: var(--s4); background: var(--raise); border-radius: var(--r-sm); font-size: var(--t-ui); }
  .heading, .trial-head { display:flex; align-items:center; justify-content:space-between; gap:var(--s2); }
  h2 { margin:0; font-size:var(--t-cap); font-weight:600; text-transform:uppercase; letter-spacing:.07em; color:var(--muted); }
  .pause { display:flex; align-items:center; justify-content:space-between; gap:var(--s3); margin-top:var(--s3); padding:var(--s3); border:1px solid var(--gold); border-radius:var(--r-sm); background:var(--side); }
  .pause strong { color:var(--gold); } .pause p { margin-bottom:0; } .pause > div { min-width:0; }
  .resume { flex-shrink:0; min-height:44px; padding:var(--s2) var(--s3); border:1px solid var(--gold); border-radius:var(--r-sm); font-weight:600; text-decoration:none; }
  .resume:disabled { opacity:.6; cursor:wait; }
  .now > div { min-width:0; } .now .trial:first-child { margin-top:0; } .now > div > p { margin-top:0; }
  .trial { margin-top:var(--s3); } strong { font-size:var(--t-body); font-weight:600; }
  p { margin:var(--s2) 0; overflow-wrap:anywhere; } .muted,.checks { color:var(--muted); } .checks { font-size:var(--t-sm); }
  .batch { display:flex; flex-wrap:wrap; gap:var(--s2); margin-top:var(--s2); }
  button { color:var(--ink); font-size:inherit; text-decoration:underline; text-underline-offset:3px; }
  progress { display:block; width:100%; height:4px; border:0; border-radius:2px; accent-color:var(--sage); margin:var(--s2) 0; }
  progress::-webkit-progress-bar { background:var(--select); } progress::-webkit-progress-value { background:var(--sage); }
  .line { display:grid; grid-template-columns:55px 1fr; gap:var(--s2); margin-top:var(--s3); } .line b { font-weight:600; } .line > span { min-width:0; overflow-wrap:anywhere; }
  .next { margin-right:var(--s2); }.gold { color:var(--gold); } .error { color:var(--coral); }
  .wait-prs { display:inline-flex; flex-wrap:wrap; gap:var(--s2); margin-left:var(--s2); }
  .wait-prs .pr { color:var(--ink-2); font-weight:600; }
  .wait code { padding:1px 5px; border-radius:4px; background:var(--select); color:var(--ink-2); font-family:var(--mono); font-size:var(--t-sm); }
  @media (max-width:760px) { .merging { margin-left:var(--s4); margin-right:var(--s4); } }
</style>
