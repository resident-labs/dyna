<script lang="ts">
  // The icons at the right of a session header for what the session uses now:
  // its running shells, with a count, and its running computer. Each shows only
  // while in use, so none is ever grey.
  interface Props {
    shells: number
    computer: boolean
    tab: string
    onselect: (tab: string) => void
  }
  let { shells, computer, tab, onselect }: Props = $props()
</script>

{#if shells}
  <button class="use" class:on={tab === 'shell'} title="Shells · {shells} running" aria-label="Shells, {shells} running" onclick={() => onselect('shell')}>
    <svg viewBox="0 0 24 24" width="21" height="21" aria-hidden="true"><rect x="3" y="4.5" width="18" height="15" rx="2.5" fill="none" stroke="currentColor" stroke-width="1.8" /><path d="m7 9.5 3 2.5-3 2.5M12.5 15h4.5" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" /></svg>
    <span class="count">{shells}</span>
  </button>
{/if}
{#if computer}
  <button class="use" class:on={tab === 'computer'} title="Computer · live" aria-label="Computer, live" onclick={() => onselect('computer')}>
    <svg viewBox="0 0 24 24" width="21" height="21" aria-hidden="true"><rect x="3" y="4" width="18" height="12" rx="2" fill="none" stroke="currentColor" stroke-width="1.8" /><path d="M9 20h6M12 16v4" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" /></svg>
    <span class="dot"></span>
  </button>
{/if}

<style>
  .use { position: relative; flex: none; display: grid; place-items: center; width: 32px; height: 32px; border-radius: 6px; color: var(--ink-2); transition: color var(--motion-fast) var(--ease), background-color var(--motion-fast) var(--ease); }
  .use.on { color: var(--ink); background: var(--raise); }
  @media (hover: hover) { .use:hover { color: var(--ink); background: var(--hover); } }
  .use:active { background: var(--select); }
  .count { position: absolute; top: 1px; right: -1px; min-width: 16px; height: 16px; padding: 0 4px; border-radius: 8px; background: var(--gold); color: var(--rail); font-size: var(--t-cap); font-weight: 700; line-height: 16px; text-align: center; font-variant-numeric: tabular-nums; box-shadow: 0 0 0 2px var(--main); }
  .dot { position: absolute; top: 5px; right: 4px; width: 7px; height: 7px; border-radius: 50%; background: var(--gold); box-shadow: 0 0 0 2px var(--main); }
  @media (max-width: 760px) { .use { width: 44px; height: 44px; } }
</style>
