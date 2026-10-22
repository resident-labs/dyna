<script lang="ts">
  // The three steps of a change (CI, review, merge) as a track. steps comes
  // from statusBox in prlist.js. The tile ring, the status box and the story
  // all read the same steps.
  import type { Step } from './prlist.ts'
  let { steps }: { steps: Step[] } = $props()
  const mark = (s: Step, i: number) => (s.state === 'ok' ? '✓' : s.state === 'bad' ? '!' : i + 1)
</script>

<ol class="track" aria-label="Progress">
  {#each steps as s, i}
    <li class="node {s.state}"><span class="n" aria-hidden="true">{mark(s, i)}</span><span class="sn">{s.name}</span><span class="note">{s.note}</span></li>
    {#if i < steps.length - 1}<li class="bar" class:ok={s.state === 'ok'} aria-hidden="true"></li>{/if}
  {/each}
</ol>

<style>
  .track { list-style: none; display: flex; align-items: flex-start; padding: 0; margin: 0; }
  .node { display: flex; flex-direction: column; align-items: center; gap: 3px; min-width: 0; color: var(--muted); font-size: 12px; white-space: nowrap; }
  .n { width: 28px; height: 28px; border-radius: 50%; display: grid; place-items: center; font-weight: 700; font-size: 13px; border: 2px solid var(--faint); border-style: dashed; color: var(--faint); }
  .ok .n { background: rgba(151, 201, 149, 0.16); color: var(--sage); border: 0; }
  .run .n { background: rgba(230, 192, 123, 0.16); color: var(--gold); border: 0; box-shadow: 0 0 0 4px rgba(230, 192, 123, 0.14); animation: pulse 1.6s ease-in-out infinite; }
  .warn .n { background: rgba(230, 192, 123, 0.16); color: var(--gold); border: 0; }
  .bad .n { background: rgba(239, 138, 128, 0.16); color: var(--coral); border: 0; }
  .sn { font-weight: 600; color: var(--ink-2); }
  .note { font-size: 11.5px; color: var(--faint); max-width: 18ch; overflow: hidden; text-overflow: ellipsis; }
  .ok .note { color: var(--sage); }
  .bad .note { color: var(--coral); }
  .warn .note, .run .note { color: var(--gold); }
  .bar { flex: 1; min-width: 16px; height: 3px; border-radius: 2px; background: var(--line); margin: 13px 8px 0; }
  .bar.ok { background: var(--sage); }
  @keyframes pulse { 50% { box-shadow: 0 0 0 4px rgba(230, 192, 123, 0.04); } }
</style>
