<script lang="ts">
  import EmptyState from '../ui/EmptyState.svelte'
  import ActivityTimeline from './ActivityTimeline.svelte'
  import type { ShellJob } from '../api/terminal.ts'
  import type { TimelineRow } from './dm.ts'

  let { jobs, onjob = null }: { jobs: ShellJob[]; onjob?: ((job: ShellJob) => void) | null } = $props()
  let row = $derived<TimelineRow>({ kind: 'timeline', key: 'running-shells', steps: jobs.map((job) => ({ kind: 'shell', key: job.id, jobId: job.id, job, waits: 0, posts: [] })) })
</script>

{#if jobs.length}
  <div class="shells"><ActivityTimeline {row} {jobs} {onjob} /></div>
{:else}<EmptyState title="No running shells" />{/if}

<style>
  .shells { padding: var(--s3) var(--s4); overflow-y: auto; min-width: 0; }
</style>
