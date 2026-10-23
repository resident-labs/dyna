<script lang="ts">
  // The activity timeline: one line per thing a service did, newest first. A
  // line opens the session behind it when there is one, and its service name
  // filters the timeline.
  import { clock } from '../ui/format.ts'
  import type { Activity } from './model.ts'

  interface Props {
    lines: Activity[]
    selected: string
    onSelect: (id: string) => void
    onOpen: (session: string) => void
  }
  let { lines, selected, onSelect, onOpen }: Props = $props()

  // A tab left open past midnight must date a line the next day, so the
  // comparison uses the day the line is drawn.
  const when = (at: number): string => {
    const d = new Date(at)
    return d.toDateString() === new Date().toDateString() ? clock(at) : d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' })
  }
</script>

{#if lines.length}
  <ol class="tl">
    {#each lines as line (line.key)}
      <li class="it {line.tone}">
        <time datetime={new Date(line.at).toISOString()}>{when(line.at)}</time>
        <span class="what">
          {#if line.session}
            <button class="open" onclick={() => onOpen(line.session)}>{line.text}</button>
          {:else}
            {line.text}
          {/if}
          {#if selected !== line.service}
            <button class="svc" onclick={() => onSelect(line.service)} aria-label="Show only {line.service}">{line.service}</button>
          {/if}
        </span>
      </li>
    {/each}
  </ol>
{:else}
  <p class="none">No activity for this filter yet.</p>
{/if}

<style>
  .tl { list-style: none; margin: 0; padding: 0 0 0 3px; border-left: 1px solid var(--line); }
  .it { position: relative; display: flex; gap: var(--s2); padding: 0 0 var(--s3) var(--s3); font-size: var(--t-ui); color: var(--ink-2); }
  .it::before { content: ''; position: absolute; left: -4px; top: 6px; width: 7px; height: 7px; border-radius: 50%; background: var(--faint); }
  .it.live::before { background: var(--gold); }
  .it.on::before { background: var(--sage); }
  .it.error::before { background: var(--coral); }
  time { flex: none; width: 38px; font-family: var(--mono); font-size: var(--t-sm); color: var(--faint); font-variant-numeric: tabular-nums; }
  .what { min-width: 0; }
  .open { text-align: left; color: var(--ink-2); transition: color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .open:hover { color: var(--ink); }
  }
  .svc { margin-left: var(--s2); font-size: var(--t-cap); letter-spacing: 0.05em; text-transform: uppercase; color: var(--faint); transition: color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .svc:hover { color: var(--muted); }
  }
  .none { margin: var(--s2) 0; color: var(--faint); font-size: var(--t-ui); }
</style>
