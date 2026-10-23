<script lang="ts">
  // One tab's results, newest first. An entry opens the session behind it when
  // there is one. A trial lists its outcomes under its name.
  import { clock } from '../ui/format.ts'
  import type { Entry } from './tabs.ts'

  interface Props {
    entries: Entry[]
    empty: string
    onOpen: (session: string) => void
  }
  let { entries, empty, onOpen }: Props = $props()

  // A tab left open past midnight must date a line the next day, so the
  // comparison uses the day the line is drawn.
  const when = (at: number): string => {
    const d = new Date(at)
    return d.toDateString() === new Date().toDateString() ? clock(at) : d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' })
  }
</script>

{#if entries.length}
  <ol class="tl">
    {#each entries as entry (entry.key)}
      <li class="it {entry.tone}">
        <time datetime={new Date(entry.at).toISOString()}>{when(entry.at)}</time>
        <span class="what">
          {#if entry.session}
            <button class="open" onclick={() => onOpen(entry.session)}>{entry.text}</button>
          {:else}
            {entry.text}
          {/if}
          {#each entry.steps as step}
            <span class="step {step.tone}">{step.text}</span>
          {/each}
        </span>
      </li>
    {/each}
  </ol>
{:else}
  <p class="none">{empty}</p>
{/if}

<style>
  .tl { list-style: none; margin: 0; padding: 0 0 0 3px; border-left: 1px solid var(--line); }
  .it { position: relative; display: flex; gap: var(--s2); padding: 0 0 var(--s3) var(--s3); font-size: var(--t-ui); color: var(--ink-2); }
  .it::before { content: ''; position: absolute; left: -4px; top: 6px; width: 7px; height: 7px; border-radius: 50%; background: var(--faint); }
  .it.live::before { background: var(--gold); }
  .it.on::before { background: var(--sage); }
  .it.error::before { background: var(--coral); }
  time { flex: none; width: 50px; white-space: nowrap; font-family: var(--mono); font-size: var(--t-sm); color: var(--faint); font-variant-numeric: tabular-nums; }
  .what { min-width: 0; }
  .open { text-align: left; color: var(--ink-2); transition: color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .open:hover { color: var(--ink); }
  }
  .step { display: block; font-size: var(--t-sm); color: var(--muted); }
  .step.error { color: var(--coral); }
  .none { margin: var(--s2) 0; color: var(--faint); font-size: var(--t-ui); }
</style>
