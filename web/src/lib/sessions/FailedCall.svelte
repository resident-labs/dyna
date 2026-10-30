<script lang="ts">
  import { copyText } from '../../api.ts'
  // What a failed call printed: its command, the tail of its output and its
  // exit code. A failed call never shows as an empty box.
  interface Props {
    command?: string
    output?: string
    exit?: number | null
  }
  let { command = '', output = '', exit = null }: Props = $props()
  const detailsId = $props.id()
  let expanded = $state(false)
  let feedback = $state('')
  let generation = 0
  $effect(() => {
    command; output; exit
    expanded = false
    feedback = ''
    generation++
  })
  function details() {
    return [
      command ? `Command:\n${command}` : 'Command: not captured',
      `Captured output:\n${output || 'No output was captured.'}`,
      `Exit status: ${exit == null ? 'unknown' : exit}`,
    ].join('\n\n')
  }
  async function copy() {
    const current = ++generation
    feedback = ''
    try {
      await copyText(details())
      if (current === generation) feedback = 'Copied'
    } catch {
      if (current === generation) feedback = 'Could not copy'
    }
  }
</script>

<div class="failure">
  <div class="actions">
    <span>Captured output</span>
    {#if command || output}<button aria-expanded={expanded} aria-controls={detailsId} onclick={() => expanded = !expanded}>{expanded ? 'Show less' : 'Show more'}</button>{/if}
    <button onclick={copy}>Copy details</button>
    <span class="feedback" class:error={feedback === 'Could not copy'} role="status">{feedback}</span>
  </div>
  <div id={detailsId} class:expanded>
    {#if command}
      <!-- svelte-ignore a11y_no_noninteractive_tabindex (Keyboard users can scroll the captured command.) -->
      <div class="cmd" role="region" aria-label="Captured command" tabindex="0"><b>$</b>{command}</div>
    {/if}
    <!-- svelte-ignore a11y_no_noninteractive_tabindex (Keyboard users can scroll the captured output.) -->
    <pre tabindex="0" role="region" aria-label="Captured failure output">{output || 'No output was captured.'}</pre>
  </div>
  <small>{exit != null ? `exit ${exit}` : 'failed'}</small>
</div>

<style>
  .actions { display: flex; align-items: center; flex-wrap: wrap; gap: var(--s2); margin-bottom: var(--s1); font: var(--t-sm)/1.5 var(--sans); color: var(--muted); }
  .actions button { padding: var(--s1) var(--s2); border-radius: var(--r-xs); background: var(--raise); color: var(--ink-2); }
  .actions button:focus-visible { outline-color: var(--iris); }
  .feedback { color: var(--sage); }
  .feedback.error { color: var(--coral); }
  .failure .expanded .cmd, .failure .expanded pre { max-height: none; overflow-y: visible; }
  @media (pointer: coarse) { .actions button { min-height: 44px; } }

  .failure { margin: 2px 0 6px 0; padding: var(--s1) var(--s3); background: var(--rail); border-radius: 6px; box-shadow: inset 2px 0 0 rgba(239, 138, 128, 0.55); font: var(--t-sm)/1.6 var(--mono, ui-monospace, monospace); color: var(--ink-2); max-width: var(--feed-reading-width); }
  .failure .cmd { color: var(--faint); white-space: pre-wrap; overflow-wrap: anywhere; max-height: 9.6em; overflow-y: auto; }
  .failure .cmd b { font-weight: 400; margin-right: 6px; }
  .failure pre { margin: 0; font: inherit; white-space: pre-wrap; overflow-wrap: anywhere; max-height: 240px; overflow-y: auto; }
  .failure small { display: block; margin-top: 2px; color: var(--coral); font: var(--t-sm)/1.5 var(--sans, system-ui, sans-serif); }
</style>
