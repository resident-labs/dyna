<script lang="ts">
  import { onMount } from 'svelte'
  import { api, copyText } from '../../api.js'
  import { bytesOf, commandOf, isLogChunk, isOpen, plainLog, sizeOf } from './shell.ts'
  import type { ShellJob } from '../api/terminal.ts'

  let { job, onjob = null }: { job: ShellJob; onjob?: ((job: ShellJob) => void) | null } = $props()
  let full = $state<string | null>(null)
  let next = $state(0)
  let size = $state(0)
  let loading = $state(false)
  let failure = $state('')
  let gone = $state(false)
  let feedback = $state('')
  let pruned = $derived(job.logPruned === true || gone)
  async function showMore() {
    if (loading) return
    loading = true
    failure = ''
    try {
      const chunk: unknown = await api.shellLogRange(job.id, next, 0)
      if (!isLogChunk(chunk)) throw new Error('Unexpected log reply')
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
  onMount(() => {
    if (!isOpen(job) && !job.tail && !pruned) void showMore()
  })
  async function copy() {
    try { await copyText(commandOf(job)); feedback = 'Copied' }
    catch { feedback = 'Could not copy' }
  }
</script>

<div class="output">
  <div class="actions">
    <button onclick={copy}>Copy command</button>
    {#if onjob}<button onclick={() => onjob?.(job)}>Open log</button>{/if}
    <span role="status">{feedback}</span>
  </div>
  <pre class="command">{commandOf(job)}</pre>
  <pre aria-label="Shell output">{plainLog(full ?? job.tail ?? (pruned ? 'The log was pruned.' : loading ? 'Loading output…' : 'No output was captured.'))}</pre>
  <div class="foot">
    <span>{pruned ? 'Log pruned' : full !== null && size ? `${bytesOf(next)} of ${bytesOf(size)}` : sizeOf(job)}</span>
    {#if !isOpen(job) && !pruned && (full === null || next < size)}<button onclick={showMore} disabled={loading}>{loading ? 'Loading…' : full === null ? 'Show all output' : 'Show more output'}</button>{/if}
    {#if failure}<span class="error" role="status">{failure}</span>{/if}
  </div>
</div>

<style>
  .output { background: var(--rail); border-radius: 6px; padding: var(--s2) var(--s3); }
  .actions, .foot { display: flex; flex-wrap: wrap; align-items: baseline; gap: var(--s2) var(--s3); color: var(--muted); font-size: var(--t-cap); }
  button { padding: var(--s1) 0; color: var(--iris); }
  .actions [role="status"] { color: var(--sage); }
  pre { margin: var(--s1) 0; font: var(--t-sm)/1.6 var(--mono); white-space: pre-wrap; overflow-wrap: anywhere; max-height: 360px; overflow-y: auto; user-select: text; }
  .command { color: var(--muted); max-height: 10em; }
  .error { color: var(--coral); }
  @media (pointer: coarse) { button { min-height: 44px; } }
</style>
