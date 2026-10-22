<script lang="ts">
  // One worker's detail on the run page: its full result, then its journal
  // (the dyna journal entries it wrote), newest first. value is RunResult's
  // answer: {result, truncatedKB, journal}.
  import type { RunResultView } from '../api/workflow.ts'
  import JsonView from '../ui/JsonView.svelte'
  let { value }: { value?: RunResultView } = $props()
  const parse = (text: string): unknown => { try { return JSON.parse(text) } catch { return text } }
  const clock = (ts: number) => new Date(ts).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' })
  let journal = $derived(value?.journal ?? [])
</script>

{#if value?.result}
  <JsonView value={parse(value.result)} />
  {#if value.truncatedKB}<p class="quiet">The result is truncated at {value.truncatedKB} KB; this shows its start.</p>{/if}
{/if}
{#if journal.length}
  <h3 class="jhead">Journal<span>{journal.length}</span></h3>
  <ol class="journal">
    {#each journal as e (e.ts + e.kind + e.message)}
      <li>
        <time>{clock(e.ts)}</time>
        <span class="kind">{e.kind}</span>
        <div class="msg"><JsonView value={e.message} />{#if e.next}<p class="next">Next: {e.next}</p>{/if}</div>
      </li>
    {/each}
  </ol>
{/if}

<style>
  .quiet { color: var(--faint); font-size: var(--t-sm); }
  .jhead { display: flex; gap: 8px; align-items: baseline; margin: 14px 0 6px; font-size: var(--t-sm); font-weight: 600; color: var(--muted); }
  .jhead span { color: var(--faint); font-weight: 500; }
  .journal { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 8px; }
  .journal li { display: grid; grid-template-columns: auto auto 1fr; gap: 10px; align-items: baseline; }
  time { color: var(--faint); font-size: var(--t-cap); font-variant-numeric: tabular-nums; }
  .kind { font-size: var(--t-cap); color: var(--muted); background: var(--raise); border-radius: 4px; padding: 0 6px; }
  .msg { min-width: 0; }
  .next { margin: 2px 0 0; color: var(--muted); font-size: var(--t-sm); }
  @media (max-width: 600px) { .journal li { grid-template-columns: auto 1fr; } .msg { grid-column: 1 / -1; } }
</style>
