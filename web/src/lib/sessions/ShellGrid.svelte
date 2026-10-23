<script lang="ts">
  // The Shell tab: one card for each shell the session runs now. A card is the
  // same block a DM shows, always open. Text wraps; the grid and each output well can scroll.
  import EmptyState from '../ui/EmptyState.svelte'
  import ShellBlock from './ShellBlock.svelte'
  import type { ShellJob } from './shellBlock.ts'

  interface Props {
    jobs: ShellJob[]
    onjob?: ((job: ShellJob) => void) | null
  }
  let { jobs, onjob = null }: Props = $props()
</script>

{#if jobs.length}
  <div class="grid">
    {#each jobs as job (job.id)}
      <div class="card"><ShellBlock {job} {onjob} /></div>
    {/each}
  </div>
{:else}
  <EmptyState title="No running shells" />
{/if}

<style>
  .grid { flex: 1; min-height: 0; overflow-y: auto; overflow-x: hidden; padding: var(--s5); display: grid; grid-template-columns: repeat(auto-fill, minmax(min(420px, 100%), 1fr)); gap: var(--s4); align-content: start; }
  .card { min-width: 0; background: var(--side); border: 1px solid var(--line-soft); border-radius: var(--r-sm); padding: var(--s3); }
  .card :global(.blk) { max-width: none; margin: 0; }
  .card :global(.well) { margin-left: 0; }
  .card :global(.cmd) { max-height: none; overflow: visible; }
  /* At the output boundary, a continued gesture can scroll the grid. */
  .card :global(pre) { max-height: 190px; overscroll-behavior-y: auto; }
  @media (max-width: 760px) { .grid { padding: var(--s3); gap: var(--s3); } }
</style>
