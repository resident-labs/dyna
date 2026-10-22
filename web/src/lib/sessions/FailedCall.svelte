<script lang="ts">
  // What a failed call printed: its command, the tail of its output and its
  // exit code. A failed call never shows as an empty box.
  interface Props {
    command?: string
    output?: string
    exit?: number | null
  }
  let { command = '', output = '', exit = null }: Props = $props()
</script>

<div class="failure">
  {#if command}<div class="cmd"><b>$</b>{command}</div>{/if}
  <pre>{output || 'No output was captured.'}</pre>
  <small>{exit != null ? `exit ${exit}` : 'failed'}</small>
</div>

<style>
  .failure { margin: 2px 0 6px 0; padding: var(--s1) var(--s3); background: var(--rail); border-radius: 6px; box-shadow: inset 2px 0 0 rgba(239, 138, 128, 0.55); font: var(--t-sm)/1.6 var(--mono, ui-monospace, monospace); color: var(--ink-2); max-width: 920px; }
  .failure .cmd { color: var(--faint); white-space: pre-wrap; overflow-wrap: anywhere; max-height: 9.6em; overflow-y: auto; }
  .failure .cmd b { font-weight: 400; margin-right: 6px; }
  .failure pre { margin: 0; font: inherit; white-space: pre-wrap; overflow-wrap: anywhere; max-height: 240px; overflow-y: auto; }
  .failure small { display: block; margin-top: 2px; color: var(--coral); font: var(--t-sm)/1.5 var(--sans, system-ui, sans-serif); }
</style>
