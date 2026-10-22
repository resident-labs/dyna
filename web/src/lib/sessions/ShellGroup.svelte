<script lang="ts">
  // A run of quiet commands as one line: "Ran 6 commands · all exit 0 · 0.7s".
  // Opening it lists each command as its own closed block.
  import ShellBlock from './ShellBlock.svelte'
  import { groupMeta } from './shellBlock.ts'
  import type { ShellJob } from './shellBlock.ts'

  interface Props {
    jobs: ShellJob[]
    onjob?: ((job: ShellJob) => void) | null
  }
  let { jobs, onjob = null }: Props = $props()
  let open = $state(false)
</script>

<div class="grp">
  <button class="hd" aria-expanded={open} onclick={() => (open = !open)}>
    <span class="st" aria-hidden="true">{open ? '▾' : '›'}</span>
    <span class="desc">Ran {jobs.length} commands</span>
    <span class="meta">{groupMeta(jobs)}</span>
  </button>
  {#if open}
    <div class="list">
      {#each jobs as job (job.id)}<ShellBlock {job} {onjob} />{/each}
    </div>
  {/if}
</div>

<style>
  .grp { max-width: 920px; margin: 2px 0; font-size: var(--t-ui); }
  .hd { display: flex; align-items: baseline; gap: var(--s2); min-width: 0; padding: 2px 0; background: none; border: 0; color: var(--muted); font: inherit; text-align: left; cursor: pointer; }
  .st { flex: none; width: 14px; text-align: center; font-size: var(--t-cap); color: var(--faint); }
  .desc { min-width: 0; overflow: hidden; white-space: nowrap; text-overflow: ellipsis; }
  .meta { flex: none; color: var(--faint); font-size: var(--t-sm); font-variant-numeric: tabular-nums; white-space: nowrap; }
  .list { margin-top: 2px; }
  @media (max-width: 760px) {
    .hd { flex-wrap: wrap; row-gap: 0; }
    .meta { flex-basis: 100%; padding-left: 22px; }
  }
</style>
