<script lang="ts">
  import EmptyState from '../../ui/EmptyState.svelte'
  import { SLOTS, cacheRate, costText, dayBars, durationText, metricText, modelRows, roleRows, tokenTotal } from './activity.ts'
  import { percent } from './state.ts'
  import type { ActivityRollup, UsageRow } from './activity.ts'
  type Bar = ReturnType<typeof dayBars>['days'][number]
  let { snapshot }: { snapshot: ActivityRollup } = $props()
  const totals = $derived(snapshot.totals)
  const noData = $derived(totals.input.unknownTurns)
  const costTurns = $derived(totals.cost.completeTurns + totals.cost.partialTurns)
  const bars = $derived(dayBars(snapshot.byDayModel, snapshot.from, snapshot.to))
  const top = $derived(Math.max(1, Math.ceil(bars.max / 5e6) * 5e6))
  const models = $derived(modelRows(snapshot.byRoleModel))
  const roles = $derived(roleRows(snapshot.byRoleModel))
  const slotName: Record<number, string> = Object.fromEntries(SLOTS.map(([label, slot]) => [slot, label]))
  const turnsText = (n: number) => n.toLocaleString('en-US')
  const split = (known: number, unknown: number) => (known + unknown > 0 ? (100 * known) / (known + unknown) : 100)
  const dayLabel = (day: string) => new Date(day + 'T00:00:00Z').toLocaleDateString(undefined, { month: 'short', day: 'numeric', timeZone: 'UTC' })
  const dayTip = (d: Bar) => `${dayLabel(d.day)}: ${metricText({ tokens: d.total, completeTurns: 0, partialTurns: d.partial ? 1 : 0, unknownTurns: d.unknownTurns })} tokens · ${d.turns} turns · ${d.unknownTurns} turns with no usage data`
</script>

{#snippet bar(known: number, unknown: number, label: string)}
  <div class="split" role="img" aria-label="{label}: {known} with data, {unknown} without"><i style:width="{split(known, unknown)}%"></i><b></b></div>
{/snippet}
{#snippet unknownCount(row: UsageRow)}{#if row.input.unknownTurns}<small class:none={row.input.unknownTurns === row.turns}>{row.input.unknownTurns === row.turns ? 'none report' : `${turnsText(row.input.unknownTurns)} no data`}</small>{/if}{/snippet}

{#if totals.turns === 0}
  <EmptyState title="No session activity">No session activity in this period.</EmptyState>
{:else}
  <div class="tiles">
    <div class="tile"><span>Turns</span><strong>{turnsText(totals.turns)}</strong>{@render bar(totals.turns - noData, noData, 'Turns')}<p>{turnsText(totals.turns - noData)} with usage · {#if noData}<em>{turnsText(noData)} no data</em>{:else}none without data{/if}</p></div>
    <div class="tile"><span>Input tokens</span><strong>{metricText(totals.input)}</strong>{@render bar(totals.input.completeTurns + totals.input.partialTurns, noData, 'Input')}<p>includes cached input</p></div>
    <div class="tile"><span>Cached input</span><strong>{metricText(totals.cached)}</strong>{@render bar(totals.cached.completeTurns + totals.cached.partialTurns, totals.cached.unknownTurns, 'Cached input')}<p>{@render rate(totals)}</p></div>
    <div class="tile"><span>Output tokens</span><strong>{metricText(totals.output)}</strong>{@render bar(totals.output.completeTurns + totals.output.partialTurns, totals.output.unknownTurns, 'Output')}<p>{turnsText(totals.output.unknownTurns)} turns unreported</p></div>
    <div class="tile"><span>Cost, known only</span><strong>{costText(totals.cost)}</strong>{@render bar(costTurns, totals.cost.unknownTurns, 'Cost')}<p>{turnsText(costTurns)} of {turnsText(totals.turns)} turns report cost</p></div>
    <div class="tile"><span>Wall time</span><strong>{durationText(totals.wallMs)}</strong><p>turn start to end</p></div>
  </div>

  <h3>Tokens per day by model</h3>
  <div class="legend">{#each bars.series as s (s.slot)}<span><i class="sw c{s.slot}"></i>{s.label}</span>{/each}<span><i class="sw hatch"></i>no data</span></div>
  <figure class="chart">
    <div class="plot">
      <div class="ygrid">{#each [3, 2, 1, 0] as i (i)}<span><b>{i ? metricText({ tokens: (top * i) / 3, completeTurns: 1, partialTurns: 0, unknownTurns: 0 }) : '0'}</b></span>{/each}</div>
      <div class="cols">
        {#each bars.days as d (d.day)}
          <div class="col" title={dayTip(d)}>
            <svg class="stk" viewBox="0 0 10 100" preserveAspectRatio="none" aria-hidden="true">
              {#each d.segments as seg, i (seg.slot)}
                {@const below = d.segments.slice(0, i).reduce((a, s) => a + s.tokens, 0)}
                <rect class="c{seg.slot}" x="0" width="10" y={100 - (100 * (below + seg.tokens)) / top} height={(100 * seg.tokens) / top} />
              {/each}
            </svg>
            <div class="nodata" style:--a={Math.min(1, d.unknownTurns / 80)}></div>
          </div>
        {/each}
      </div>
    </div>
    <div class="xaxis"><span>{dayLabel(bars.days[0]?.day ?? '')}</span><span>{dayLabel(bars.days.at(-1)?.day ?? '')}</span></div>
    <figcaption>Known input + output tokens per UTC day. The strip under the bars counts turns with no usage data; darker means more.</figcaption>
  </figure>

  <h3>By model</h3>
  <div class="table" role="table" aria-label="Usage by model">
    <div class="th" role="row"><span>Model</span><span>Turns</span><span>Input</span><span>Cached</span><span>Output</span><span>Cost</span><span>Time</span></div>
    {#each models as m (m.model)}
      <div class="tr" role="row">
        <div class="who"><i class="sw c{m.slot}"></i><div><strong>{m.model || 'Not recorded'}</strong><p>{m.model ? slotName[m.slot] : 'older turns, before models were saved'}</p></div></div>
        <div class="num" data-l="Turns">{turnsText(m.turns)}{@render unknownCount(m)}</div>
        <div class="num" data-l="Input">{@render value(metricText(m.input))}</div>
        <div class="num" data-l="Cached">{@render value(metricText(m.cached))}{#if cacheRate(m) !== null}<small class="rate">{percent(cacheRate(m))} cache rate</small>{/if}</div>
        <div class="num" data-l="Output">{@render value(metricText(m.output))}</div>
        <div class="num" data-l="Cost">{@render value(costText(m.cost))}</div>
        <div class="num" data-l="Time">{durationText(m.wallMs)}</div>
      </div>
    {/each}
  </div>

  <h3>By agent role</h3>
  <div class="table roles" role="table" aria-label="Usage by agent role">
    <div class="th" role="row"><span>Role</span><span>Turns</span><span>Tokens</span><span>Cost</span><span>Time</span><span>Model mix</span></div>
    {#each roles as r (r.label)}
      <div class="tr" role="row">
        <div class="who"><div><strong>{r.label}</strong><p>{r.note}</p></div></div>
        <div class="num" data-l="Turns">{turnsText(r.turns)}{@render unknownCount(r)}</div>
        <div class="num" data-l="Tokens">{#if r.turns === 0}—{:else}{@render value(metricText(tokenTotal(r.input, r.output)))}{/if}</div>
        <div class="num" data-l="Cost">{#if r.turns === 0}—{:else}{@render value(costText(r.cost))}{/if}</div>
        <div class="num" data-l="Time">{r.turns === 0 ? '—' : durationText(r.wallMs)}</div>
        <div class="mix" data-l="Models">{#each r.mix as part (part.slot)}<i class="c{part.slot}" title={slotName[part.slot]} style:flex-grow={part.tokens}></i>{/each}</div>
      </div>
    {/each}
    <div class="tr workflows" role="row"><div class="who"><div><strong>Workflows</strong><p>workflow agent calls</p></div></div><div class="num"><span class="unk">not recorded</span></div></div>
  </div>
  {#if noData}<p class="note"><b>{turnsText(noData)} of {turnsText(totals.turns)} turns</b> report no usage, for example failed or stopped turns. Totals marked <b>≥</b> are lower bounds. Cost is known only for harnesses that report it.</p>{/if}
{/if}
{#snippet rate(row: UsageRow)}{#if cacheRate(row) === null}cache rate unknown{:else}{percent(cacheRate(row))} cache rate · {turnsText(row.cachePaired.turns)} turns with both{/if}{/snippet}
{#snippet value(text: string)}{#if text === 'unknown'}<span class="unk">unknown</span>{:else}{text}{/if}{/snippet}

<style>
  /* Keep model families distinct within a provider in the chart legend. */
  .c0 { background: var(--provider-unknown); } .c1 { background: var(--provider-anthropic); } .c2 { background: #f2b59f; } .c3 { background: var(--provider-chatgpt); } .c4 { background: #c98500; } .c5 { background: var(--provider-xai); }
  svg .c0 { fill: var(--provider-unknown); } svg .c1 { fill: var(--provider-anthropic); } svg .c2 { fill: #f2b59f; } svg .c3 { fill: var(--provider-chatgpt); } svg .c4 { fill: #c98500; } svg .c5 { fill: var(--provider-xai); }
  rect { stroke: var(--main); stroke-width: 2px; vector-effect: non-scaling-stroke; }
  .sw { display: inline-block; width: 10px; height: 10px; border-radius: 3px; flex: none; }
  .hatch { background: repeating-linear-gradient(135deg, var(--faint) 0 1.5px, transparent 1.5px 4px); box-shadow: inset 0 0 0 1px var(--faint); }
  .unk { color: var(--faint); font-style: italic; } em { font-style: normal; color: var(--gold); }
  h3 { margin: var(--s5) 0 var(--s2); font-size: var(--t-title); font-weight: 500; }
  .tiles { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: var(--s3); margin-top: var(--s5); }
  .tile { background: var(--side); border-radius: var(--r-sm); box-shadow: inset 0 0 0 1px var(--hair); padding: var(--s3) var(--s4); display: flex; flex-direction: column; gap: 6px; min-width: 0; }
  .tile span { font-size: var(--t-sm); color: var(--muted); } .tile p { margin: 0; font-size: var(--t-cap); color: var(--muted); }
  .tile strong { font-size: var(--t-display); font-weight: 600; font-variant-numeric: tabular-nums; line-height: 1.1; overflow-wrap: anywhere; }
  .split { display: flex; height: 6px; gap: 2px; border-radius: 3px; overflow: hidden; } .split i { background: var(--iris); border-radius: 3px 0 0 3px; }
  .split b { flex: 1; background: repeating-linear-gradient(135deg, var(--faint) 0 1.5px, transparent 1.5px 4px); box-shadow: inset 0 0 0 1px var(--line); border-radius: 0 3px 3px 0; }
  .legend { display: flex; flex-wrap: wrap; gap: var(--s1) var(--s4); font-size: var(--t-sm); color: var(--ink-2); margin-bottom: var(--s3); } .legend span { display: inline-flex; align-items: center; gap: 6px; }
  .chart { margin: 0; } .plot { position: relative; height: 170px; }
  .ygrid { position: absolute; inset: 0 0 14px; display: flex; flex-direction: column; justify-content: space-between; }
  .ygrid span { display: block; height: 0; border-top: 1px solid var(--line-soft); position: relative; }
  .ygrid b { position: absolute; top: -8px; left: 0; font-size: var(--t-cap); font-weight: 500; color: var(--faint); background: var(--main); padding-right: 4px; }
  .cols { position: absolute; inset: 0 0 0 38px; display: flex; gap: 3px; } .col { flex: 1; min-width: 0; display: flex; flex-direction: column; }
  .stk { flex: 1; width: 100%; min-height: 0; margin-bottom: 2px; }
  .nodata { height: 12px; border-radius: 2px; background: repeating-linear-gradient(135deg, var(--faint) 0 1.5px, transparent 1.5px 4px); opacity: calc(.25 + .75 * var(--a)); }
  .xaxis { display: flex; justify-content: space-between; margin: 6px 0 0 38px; font-size: var(--t-cap); color: var(--faint); } figcaption { font-size: var(--t-cap); color: var(--muted); margin-top: var(--s2); }
  .table { display: flex; flex-direction: column; }
  .th, .tr { display: grid; grid-template-columns: minmax(190px, 2.2fr) repeat(6, minmax(64px, 1fr)); gap: var(--s3); align-items: center; }
  .th { font-size: var(--t-cap); color: var(--muted); padding: var(--s2) var(--s3); border-bottom: 1px solid var(--line); } .th span:not(:first-child), .num { text-align: right; }
  .tr { padding: var(--s3); font-size: var(--t-sm); border-bottom: 1px solid var(--line-soft); min-height: 48px; } .num { font-variant-numeric: tabular-nums; white-space: nowrap; }
  .num small { display: block; font-size: var(--t-cap); color: var(--gold); } .num small.rate { color: var(--muted); } .num small.none { color: var(--faint); }
  .who { display: flex; align-items: center; gap: 10px; min-width: 0; } .who strong { font-weight: 500; overflow-wrap: anywhere; } .who p { margin: 1px 0 0; font-size: var(--t-cap); color: var(--muted); }
  .roles .th, .roles .tr { grid-template-columns: minmax(150px, 1.5fr) repeat(4, minmax(60px, 1fr)) minmax(110px, 1.4fr); }
  .mix { display: flex; gap: 2px; height: 10px; } .mix i { display: block; height: 100%; border-radius: 2px; min-width: 2px; flex-basis: 0; }
  .workflows { grid-template-columns: minmax(150px, 1.5fr) 1fr !important; } .workflows .num { text-align: left; }
  .note { margin: var(--s4) 0 0; padding: var(--s3) var(--s4); background: var(--gold-wash); color: var(--gold); border-radius: 6px; font-size: var(--t-sm); line-height: 1.6; }
  @media (max-width: 760px) {
    .tiles { grid-template-columns: repeat(2, minmax(0, 1fr)); gap: var(--s2); } .tile { padding: var(--s3); } .tile strong { font-size: var(--t-head); }
    .th { display: none; }
    .tr, .roles .tr { grid-template-columns: repeat(3, 1fr); gap: var(--s2) var(--s3); } .roles .tr { grid-template-columns: repeat(4, 1fr); }
    .tr .who, .tr .mix, .workflows .num { grid-column: 1 / -1; } .num { text-align: left; } .num::before { content: attr(data-l); display: block; font-size: var(--t-cap); color: var(--muted); }
    .workflows { grid-template-columns: 1fr !important; } .workflows .num::before { content: none; }
    .cols { gap: 2px; }
  }
</style>
