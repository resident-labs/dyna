<script lang="ts">
  import { dateTime, percent, providerColor, remainingPercent } from './state.ts'
  import { resetNotice } from './reset.ts'
  import { meterWake, paceOf, resetsIn } from './pace.ts'
  import { deadlineClock } from './clock.svelte.ts'
  import type { UsageWindow } from '../../api/account.ts'

  // One account's share of a pooled window: its name, its percent left, a
  // fill in the provider color, the even-pace tick when the window length is
  // known (a notch on the bottom edge, so it never crosses the text), and a
  // reset chip. active outlines the account in use; muted dims an
  // account that does not count toward the headline.
  interface Props {
    name: string
    window: UsageWindow
    observedAt: string
    provider?: string
    active?: boolean
    muted?: boolean
  }
  let { name, window, observedAt, provider = '', active = false, muted = false }: Props = $props()
  // The segment wakes each minute and at its reset, so the countdown, pace and a passed reset stay current.
  const now = deadlineClock(() => meterWake(window.resetsAt, Date.now()))
  const notice = $derived(resetNotice(window, observedAt, now()))
  const remaining = $derived(notice ? null : remainingPercent(window))
  const pace = $derived(remaining === null ? null : paceOf(window, now()))
  const until = $derived(resetsIn(window.resetsAt, now()))
  const low = $derived(remaining !== null && remaining < 20)
  const hover = $derived([`${name} · ${window.label}`, notice || (remaining === null ? 'Not reported' : `${percent(remaining)} left`), dateTime(window.resetsAt) ? `Resets ${dateTime(window.resetsAt)}` : ''].filter(Boolean).join(' · '))
  const words = $derived([name, active ? 'in use' : '', notice || (remaining === null ? 'not reported' : `${percent(remaining)} left${low ? ', low' : ''}`)].filter(Boolean).join(', '))
</script>

<div class="segment" class:active class:muted title={hover}>
  {#if remaining !== null}
    <div class="fill" style:width={remaining + '%'} style:background={providerColor(provider)}></div>
    {#if pace}<span class="tick" style:left={pace.expectedRemaining + '%'} title="Even pace: {percent(pace.expectedRemaining)} left"></span>{/if}
  {/if}
  <div class="label" role={remaining === null ? undefined : 'meter'} aria-label={words} aria-valuenow={remaining ?? undefined} aria-valuemin={remaining === null ? undefined : 0} aria-valuemax={remaining === null ? undefined : 100}>
    <span class="name">{name}</span>
    {#if remaining === null}<span class="quiet">{notice ? 'Probably reset' : 'Not reported'}</span>
    {:else}<span class="ui-num value" class:low>{percent(remaining)}</span>{/if}
    {#if until}<span class="reset" title={dateTime(window.resetsAt)}>↻ {until}</span>{/if}
  </div>
</div>

<style>
  .segment { position: relative; min-width: 0; min-height: 28px; border-radius: var(--r-xs); background: var(--meter-track); overflow: hidden; }
  .active { box-shadow: inset 0 0 0 1px var(--hair-strong); }
  .muted { opacity: 0.5; }
  .fill { position: absolute; inset: 0 auto 0 0; opacity: 0.38; }
  .tick { position: absolute; bottom: 0; height: 7px; width: 2px; margin-left: -1px; border-radius: 2px 2px 0 0; background: var(--ink); }
  .label { position: relative; display: flex; flex-wrap: wrap; align-items: center; gap: 2px 6px; min-height: 28px; padding: 3px var(--s2); font-size: var(--t-sm); color: var(--ink); }
  .name { min-width: 0; overflow-wrap: anywhere; font-weight: var(--w-medium); }
  .value { flex: none; }
  .value.low { color: var(--tone-warning-fg); }
  .quiet { flex: none; color: var(--muted); }
  .reset { flex: none; margin-left: auto; padding: 0 6px; border-radius: var(--r-badge); background: rgba(var(--glass-ink), var(--glass-pill)); font-size: var(--t-cap); font-variant-numeric: tabular-nums; color: var(--ink-2); }
</style>
