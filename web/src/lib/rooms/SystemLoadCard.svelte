<script>
  import Card from '../ui/Card.svelte'
  import Row from '../ui/Row.svelte'
  import Chip from '../ui/Chip.svelte'
  import Avatar from '../ui/Avatar.svelte'
  import { loadView } from './systemLoad.ts'
  // load: one SystemLoad answer. onagent opens a top row's session.
  // title names the card. rows=false leaves the busy sessions out, for a page
  // that lists the sessions that run there itself.
  let { load, onagent = undefined, title = 'This computer', rows = true } = $props()
  let v = $derived(loadView(load))
</script>

<Card {title}>
  <div class="head">
    <span class="cpu"><span class="what">CPU</span><span class="big">{v.cpu === null ? '–' : `${v.cpu}%`}</span></span>
    <span class="load" class:hot={v.hot}>{v.loadText}</span>
  </div>
  {#if v.known}
    <div class="bar" role="img" aria-label="CPU split">
      {#each v.split as s (s.key)}<span class="seg {s.key}" style="width: {s.pct}%"></span>{/each}
    </div>
    <div class="legend">
      {#each v.split as s (s.key)}<span><i class="dot {s.key}"></i>{s.label} {s.text}</span>{/each}
    </div>
  {:else}
    <p class="note">Dyna's share is not available on this system.</p>
  {/if}
  <div class="bar" role="img" aria-label="Memory">
    {#each v.mem as s (s.key)}<span class="seg m-{s.key}" style="width: {s.pct}%"></span>{/each}
  </div>
  <div class="legend">
    {#each v.mem as s (s.key)}<span><i class="dot m-{s.key}"></i>{s.label} {s.text}</span>{/each}
    <span class="total">{v.memText}</span>
  </div>
  {#if rows}
    {#each v.top as t (t.id)}
      <Row dense text={t.name} onclick={t.address ? () => onagent?.(t) : null}>
        {#snippet lead()}<Avatar name={t.name} hue={t.hue} size={24} />{/snippet}
        {#snippet trail()}{#if t.room}<Chip>{t.room}</Chip>{/if}<span class="cores mono">{t.cores}</span>{/snippet}
      </Row>
    {/each}
  {/if}
</Card>

<style>
  .head { display: flex; align-items: baseline; gap: var(--s3); }
  .cpu { display: inline-flex; align-items: baseline; gap: 6px; }
  .what { font-size: var(--t-sm); font-weight: 600; color: var(--muted); }
  .big { font-size: var(--t-display); font-weight: 600; color: var(--ink); line-height: 1; }
  .load { font-size: var(--t-sm); color: var(--muted); }
  .load.hot { color: var(--gold); }
  .bar { display: flex; height: 8px; border-radius: 4px; overflow: hidden; background: var(--raise); }
  .seg { display: block; height: 100%; }
  .legend { display: flex; flex-wrap: wrap; gap: var(--s1) var(--s3); font-size: var(--t-cap); color: var(--muted); }
  .legend span { display: inline-flex; align-items: center; gap: 5px; }
  .total { margin-left: auto; color: var(--faint); }
  .dot { width: 8px; height: 8px; border-radius: 50%; display: inline-block; }
  .engine { background: var(--iris); }
  .agents { background: var(--hue-3); }
  .computers { background: var(--hue-2); }
  .other, .m-other { background: var(--faint); }
  .m-dyna { background: var(--hue-4); }
  .m-free { background: transparent; }
  .dot.m-free { box-shadow: inset 0 0 0 1px var(--faint); }
  .note { margin: 0; font-size: var(--t-sm); color: var(--faint); }
  .cores { flex: none; font-size: var(--t-sm); color: var(--muted); }
</style>
