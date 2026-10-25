<script lang="ts">
  import { remainingPercent, percent, allowanceLabel, shortLabel, windowReset, money } from './state.ts'
  import { resetNotice, type WindowTiming } from './reset.ts'
  import { deadlineClock } from './clock.svelte.ts'
  interface UsageWindow extends WindowTiming {
    label: string
    usedPercent?: number
    remainingPercent?: number
    used?: number
    limit?: number
    balance?: number
    unit?: string
    amountLabel?: string
  }
  let { window, observedAt = '', compact = false }: { window: UsageWindow; observedAt?: string; compact?: boolean } = $props()
  // The meter wakes at its own reset, so an open page reports the new window.
  const now = deadlineClock(() => Date.parse(window.resetsAt ?? ''))
  const notice = $derived(resetNotice(window, observedAt, now()))
  const remaining = $derived(remainingPercent(window))
  const amount = $derived(allowanceLabel(window))
  const balance = $derived(window.balance === undefined ? '' : money(window.balance, window.unit))
  const reset = $derived(windowReset(window))
</script>

<div class="window" class:compact>
  {#if balance}
    <div class="top"><span title={window.label}>{shortLabel(window.label)}</span><strong>{balance}</strong></div>
  {:else if notice}
    <div class="top reset"><span title={window.label}>{shortLabel(window.label)}: {notice}</span></div>
  {:else}
    <div class="top"><span title={window.label}>{shortLabel(window.label)}</span><strong>{remaining === null ? 'Not reported' : `${percent(remaining)} left`}</strong></div>
    {#if remaining !== null}<div class="track" role="meter" aria-label={window.label + ' remaining'} aria-valuenow={remaining} aria-valuemin="0" aria-valuemax="100"><div class="fill" class:low={remaining < 20} style:width={remaining + '%'}></div></div>{/if}
    {#if !compact && (amount || reset)}<p>{amount}{amount && reset ? ' · ' : ''}{reset}</p>{/if}
  {/if}
</div>

<style>
  .window { min-width: 0; }
  .top { display: flex; align-items: baseline; justify-content: space-between; gap: var(--s2); font-size: var(--t-sm); margin-bottom: 6px; color: var(--ink-2); }
  .top.reset { line-height: 1.7; margin-bottom: 0; overflow-wrap: anywhere; }
  strong { font-size: var(--t-body); font-weight: 600; color: var(--ink); white-space: nowrap; font-variant-numeric: tabular-nums; }
  .track { height: 4px; border-radius: 4px; background: var(--raise-hover); overflow: hidden; }
  .fill { height: 100%; border-radius: inherit; background: var(--sage); }
  .fill.low { background: var(--gold); }
  p { margin: 6px 0 0; font-size: var(--t-cap); color: var(--muted); line-height: 1.6; overflow-wrap: anywhere; }
</style>
