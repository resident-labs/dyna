<script lang="ts">
  // One figure: a micro label, a mono value and one detail line. A tone
  // colors the value, not the label. meter (0 to 100) draws a bar under the
  // value. loading keeps the old value in place and dims it. Put tiles in a
  // flush Card inside a .stat-grid, so they share one surface.
  import type { Snippet } from 'svelte'
  import type { Tone } from './tone.ts'
  interface Props {
    label: string
    value: string
    detail?: string
    tone?: Tone
    meter?: number | null
    icon?: Snippet
    loading?: boolean
  }
  let { label, value, detail = '', tone = 'neutral', meter = null, icon, loading = false }: Props = $props()
  let fill = $derived(meter === null ? null : Math.max(0, Math.min(100, meter)))
</script>

<div class="tile {tone}" aria-busy={loading || undefined}>
  <span class="micro-label">{#if icon}{@render icon()}{/if}{label}</span>
  <strong class="num" class:dimmed={loading}>{value}</strong>
  {#if fill !== null}<span class="meter" role="meter" aria-label={label} aria-valuemin={0} aria-valuemax={100} aria-valuenow={Math.round(fill)}><span style:width="{fill}%"></span></span>{/if}
  {#if detail}<span class="detail">{detail}</span>{/if}
</div>

<style>
  .tile { display: flex; flex-direction: column; gap: var(--s1); min-width: 0; padding: var(--s4) 20px; }
  .micro-label { display: inline-flex; align-items: center; gap: 6px; }
  strong { margin-top: var(--s1); font-size: var(--t-stat); font-weight: var(--w-strong); line-height: var(--leading-tight); letter-spacing: -0.02em; color: var(--ink); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .meter { margin-top: var(--s1); }
  .detail { font-size: var(--t-cap); color: var(--muted); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .success strong { color: var(--tone-success-fg); }
  .warning strong { color: var(--tone-warning-fg); }
  .danger strong { color: var(--tone-danger-fg); }
  .info strong { color: var(--tone-info-fg); }
  .live strong { color: var(--tone-live-fg); }
  .success .meter > span { background: var(--tone-success); }
  .warning .meter > span { background: var(--tone-warning); }
  .danger .meter > span { background: var(--tone-danger); }
  .info .meter > span { background: var(--tone-info); }
  .live .meter > span { background: var(--tone-live); }
</style>
