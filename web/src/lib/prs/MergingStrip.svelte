<script lang="ts">
  import Chip from '../ui/Chip.svelte'
  import type { MergeControlStatus } from '../api/change.ts'
  import { trialProgress, waitingReason } from './roomPRs.ts'
  let { status, error = '', onpick }: { status: MergeControlStatus | null; error?: string; onpick: (id: string) => void } = $props()
  let ready = $derived(status?.queue?.filter((q) => q.state === 'ready' && !q.trial) ?? [])
  let blocked = $derived(status?.queue?.filter((q) => !q.trial && q.state !== 'ready') ?? [])
  let reason = $derived(status ? waitingReason(status) : '')
</script>
<section class="merging" aria-label="Merging">
  <div class="heading"><h2>Merging</h2>{#if status?.paused}<Chip tone="gold">Paused</Chip>{/if}</div>
  {#if error}<p class="error">Merge status unavailable: {error}</p>
  {:else if !status}<p class="muted">Loading merge status…</p>
  {:else}
    {#each status.trials ?? [] as trial (trial.branch)}
      {@const progress = trialProgress(trial, status)}
      <div class="trial">
        <div class="trial-head"><strong>{progress.label}</strong><Chip tone={progress.tone}>{trial.branch.split('/').at(-1) ? `Trial ${trial.branch.split('/').at(-1)}` : 'Trial'}</Chip></div>
        <div class="batch">{#each trial.batch ?? [] as id}<button onclick={() => onpick(id)}>{id.includes('#') ? `#${id.split('#').at(-1)}` : id}</button>{/each}</div>
        {#if progress.total}
          <p class="checks">{progress.nodes ? `${progress.nodes} · ` : ''}{progress.completed}/{progress.total} checks complete · {progress.passed} passed{#if progress.failed} · <span class="error">{progress.failed} failed</span>{/if}</p>
          <progress max={progress.total} value={progress.completed} aria-label="Trial checks complete"></progress>
        {/if}
        {#if trial.reason}<p class="muted">{trial.reason}</p>{/if}
      </div>
    {:else}<p class="muted">No merge running.</p>{/each}
    <div class="line"><b>Next</b><span>{#if ready.length}{#each ready as q}<button class="next" onclick={() => onpick(q.changeId)}>#{q.changeId.split('#').at(-1)}</button>{/each}{:else}No other PR is ready.{/if}</span></div>
    {#if reason}<div class="line"><b class="gold">Waiting</b><span>{reason}</span></div>{/if}
    {#if blocked.length}<details><summary>{blocked.length} blocked or waiting</summary>{#each blocked as q}<p><button onclick={() => onpick(q.changeId)}>#{q.changeId.split('#').at(-1)}</button> <span class="muted">{q.reason || q.state}</span></p>{/each}</details>{/if}
  {/if}
</section>
<style>
  .merging { margin: 0 var(--s5) var(--s4); padding: var(--s4); background: var(--raise); border-radius: var(--r-sm); font-size: var(--t-ui); }
  .heading, .trial-head { display:flex; align-items:center; justify-content:space-between; gap:var(--s2); }
  h2 { margin:0; font-size:var(--t-cap); font-weight:600; text-transform:uppercase; letter-spacing:.07em; color:var(--muted); }
  .trial { margin-top:var(--s3); } strong { font-size:var(--t-body); font-weight:600; }
  p { margin:var(--s2) 0; overflow-wrap:anywhere; } .muted,.checks { color:var(--muted); } .checks { font-size:var(--t-sm); }
  .batch { display:flex; flex-wrap:wrap; gap:var(--s2); margin-top:var(--s2); }
  button { color:var(--ink); font-size:inherit; text-decoration:underline; text-underline-offset:3px; }
  progress { display:block; width:100%; height:4px; border:0; border-radius:2px; accent-color:var(--sage); margin:var(--s2) 0; }
  progress::-webkit-progress-bar { background:var(--select); } progress::-webkit-progress-value { background:var(--sage); }
  .line { display:grid; grid-template-columns:55px 1fr; gap:var(--s2); margin-top:var(--s3); } .line b { font-weight:600; }
  .next { margin-right:var(--s2); }.gold { color:var(--gold); } .error { color:var(--coral); } details { margin-top:var(--s3); } summary { color:var(--muted); cursor:pointer; }
  @media (max-width:760px) { .merging { margin-left:var(--s4); margin-right:var(--s4); } }
</style>
