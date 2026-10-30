<script lang="ts">
  import { remainingPercent, percent, allowanceLabel, shortLabel, windowReset, money, dateTime, providerColor } from './state.ts'
  import { resetNotice, type WindowTiming } from './reset.ts'
  import { meterWake, paceOf, resetsIn, type PaceStatus } from './pace.ts'
  import { deadlineClock } from './clock.svelte.ts'
  interface UsageWindow extends WindowTiming {
    label: string
    period?: string
    periodStart?: string
    usedPercent?: number
    remainingPercent?: number
    used?: number
    limit?: number
    balance?: number
    unit?: string
    amountLabel?: string
  }
  let { window, observedAt = '', compact = false, provider = '' }: { window: UsageWindow; observedAt?: string; compact?: boolean; provider?: string } = $props()
  // The meter wakes each minute and at its reset, so an open page keeps the countdown, pace and new window current.
  const now = deadlineClock(() => meterWake(window.resetsAt, Date.now()))
  const notice = $derived(resetNotice(window, observedAt, now()))
  const remaining = $derived(remainingPercent(window))
  const amount = $derived(allowanceLabel(window))
  const balance = $derived(window.balance === undefined ? '' : money(window.balance, window.unit))
  const reset = $derived(windowReset(window))
  const pace = $derived(paceOf(window, now()))
  const until = $derived(resetsIn(window.resetsAt, now()))
  const paceWords: Record<PaceStatus, string> = { ahead: 'Ahead of pace', on: 'On pace', under: 'Under pace' }
  const paceGlyph: Record<PaceStatus, string> = { ahead: '▲', on: '●', under: '▼' }
</script>

<div class="window" class:compact>
  {#if balance}
    <div class="top"><span title={window.label}>{shortLabel(window.label)}</span><strong>{balance}</strong></div>
  {:else if notice}
    <div class="top reset"><span title={window.label}>{shortLabel(window.label)}: {notice}</span></div>
  {:else}
    <div class="meter-row">
      <div class="name">
        <span title={window.label}>{shortLabel(window.label)}</span>
        <strong class:low={remaining !== null && remaining < 20}>{remaining === null ? 'Not reported' : `${percent(remaining)} left${remaining < 20 ? ' · low' : ''}`}</strong>
        {#if pace}<span class="pace {pace.status}" role="img" title={paceWords[pace.status]} aria-label={paceWords[pace.status]}>{paceGlyph[pace.status]}</span>{/if}
        {#if !compact && amount}<p>{amount}</p>{/if}
      </div>
      <div class="bar">
        {#if remaining !== null}<div class="track" role="meter" aria-label={window.label + ' remaining'} aria-valuenow={remaining} aria-valuemin="0" aria-valuemax="100"><div class="fill" style:background={providerColor(provider)} style:width={remaining + '%'}></div></div>{/if}
        {#if pace && remaining !== null}<span class="tick" style:left={pace.expectedRemaining + '%'} title="Even pace: {percent(pace.expectedRemaining)} left"></span>{/if}
      </div>
      <div class="when" title={dateTime(window.resetsAt)}>{until ? `resets in ${until}` : reset}</div>
    </div>
  {/if}
</div>

<style>
  .window { min-width: 0; container-type: inline-size; }
  .top { display: flex; align-items: baseline; justify-content: space-between; gap: var(--s2); font-size: var(--t-sm); margin-bottom: 6px; color: var(--ink-2); }
  .top.reset { line-height: 1.7; margin-bottom: 0; overflow-wrap: anywhere; }
  .meter-row { display: grid; grid-template-columns: minmax(0, 12rem) minmax(0, 1fr) 9rem; gap: 4px var(--s3); align-items: center; font-size: var(--t-sm); }
  .name { min-width: 0; display: flex; flex-wrap: wrap; align-items: baseline; gap: 0 6px; color: var(--ink-2); }
  .name > p { flex-basis: 100%; }
  .bar { position: relative; height: 16px; display: flex; align-items: center; }
  .when { min-width: 0; color: var(--muted); font-size: var(--t-cap); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; font-variant-numeric: tabular-nums; }
  strong { font-size: var(--t-body); font-weight: 600; color: var(--ink); white-space: nowrap; font-variant-numeric: tabular-nums; }
  .track { width: 100%; height: 8px; border-radius: 4px; background: var(--raise-hover); overflow: hidden; }
  .fill { height: 100%; border-radius: inherit; background: var(--sage); }
  strong.low { color: var(--gold); }
  .tick { position: absolute; top: 0; bottom: 0; width: 2px; margin-left: -1px; border-radius: 2px; background: var(--ink); }
  .pace { font-size: var(--t-cap); }
  .pace.ahead { color: var(--gold); }
  .pace.on { color: var(--sage); }
  .pace.under { color: var(--muted); }
  @container (max-width: 480px) {
    .meter-row { grid-template-columns: minmax(0, 1fr) auto; }
    .bar { grid-column: 1 / -1; grid-row: 2; }
    .when { grid-column: 2; grid-row: 1; }
  }
  p { margin: 6px 0 0; font-size: var(--t-cap); color: var(--muted); line-height: 1.6; overflow-wrap: anywhere; }
</style>
