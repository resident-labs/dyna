<script lang="ts">
  // One compact summary row for every registered system event. The typed
  // renderer supplies the facts; this component owns the shared layout and
  // disclosure behavior. Rich details stay available without a boxed card.
  import type { Snippet } from 'svelte'
  import Row from '../ui/Row.svelte'
  import { clock } from '../ui/format.ts'

  interface EventChip {
    label: string
    failure?: boolean
    tone?: string
    outline?: boolean
    dot?: boolean
  }
  interface Props {
    id?: string
    ts?: string | number
    title: string
    summary?: string
    chips?: readonly EventChip[]
    surface?: 'feed' | 'dm'
    icon?: Snippet
    headline?: Snippet
    extra?: Snippet
    summaryExtra?: Snippet
    detail?: Snippet
    open?: boolean
    ontoggle?: (open: boolean) => void
    onclick?: ((event: MouseEvent) => void) | null
  }
  let {
    id = undefined,
    ts = undefined,
    title,
    summary = '',
    chips = [],
    surface = 'feed',
    icon = undefined,
    headline = undefined,
    extra = undefined,
    summaryExtra = undefined,
    detail = undefined,
    open = undefined,
    ontoggle = undefined,
    onclick = null,
  }: Props = $props()
</script>

{#snippet summaryRow()}
  <Row dense onclick={detail ? null : onclick}>
    {#snippet lead()}
      <span class="glyph" aria-hidden="true">{#if icon}{@render icon()}{:else}•{/if}</span>
    {/snippet}
    {#snippet children()}
      <span class="headline">{#if headline}{@render headline()}{:else}<strong>{title}</strong>{/if}{#each chips as chip, i (i)}<span class="state" class:failed={chip.failure === true}>{chip.label}</span>{/each}{#if extra}{@render extra()}{/if}</span>
      {#if summary || summaryExtra}<span class="summary">{summary}{#if summary && summaryExtra}{' · '}{/if}{#if summaryExtra}{@render summaryExtra()}{/if}</span>{/if}
    {/snippet}
    {#snippet trail()}
      <time title={new Date(ts ?? 0).toLocaleString()}>{clock(ts)}</time>
      {#if detail || onclick}<span class="chevron" aria-hidden="true">›</span>{/if}
    {/snippet}
  </Row>
{/snippet}

{#if detail}
  <details class="event {surface}" data-id={id} {open} ontoggle={(event) => ontoggle?.(event.currentTarget.open)}>
    <summary>{@render summaryRow()}</summary>
    <div class="detail">{@render detail()}</div>
  </details>
{:else}
  <div class="event {surface}" data-id={id}>{@render summaryRow()}</div>
{/if}

<style>
  .event { min-width: 0; }
  .event :global(.row) { background: transparent; box-shadow: none; transition: background-color var(--motion-fast) var(--ease); }
  .event :global(.row:hover), .event :global(.row:active), .event :global(.row.sel) { background: transparent; box-shadow: none; }
  .event > summary { list-style: none; cursor: pointer; }
  .event > summary::-webkit-details-marker { display: none; }
  .event[open] > summary .chevron { transform: rotate(90deg); }
  .glyph { width: 24px; height: 24px; display: grid; place-items: center; color: var(--muted); font-size: var(--t-title); }
  .headline { display: flex; align-items: center; flex-wrap: wrap; gap: var(--s1) var(--s2); font-size: var(--t-body); }
  strong { font-weight: 600; }
  .state { color: var(--muted); font-size: var(--t-body); font-weight: 400; }
  .state.failed { color: var(--coral); }
  .headline :global(a), .summary :global(a) { color: var(--muted); text-decoration: none; transition: color var(--motion-fast) var(--ease); }
  .headline :global(a:hover), .summary :global(a:hover) { color: var(--ink); text-decoration: underline; }
  .summary { display: block; margin-top: 2px; color: var(--muted); font-size: var(--t-sm); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  time, .chevron { flex: none; color: var(--faint); font-size: var(--t-cap); }
  .chevron { font-size: var(--t-title); }
  .detail { padding: var(--s2) var(--s2) var(--s3) 44px; color: var(--ink-2); font-size: var(--t-sm); overflow-wrap: anywhere; }
  .event.dm .detail { padding: var(--s2) 0 0; }
  .detail :global(.btn), .detail :global(.link) {
    min-height: 0;
    padding: 0;
    border: 0;
    background: none;
    color: var(--iris);
    font: inherit;
    font-size: var(--t-sm);
    cursor: pointer;
    transition: color var(--motion-fast) var(--ease), background-color var(--motion-fast) var(--ease);
  }
  .detail :global(.btn:hover), .detail :global(.link:hover) { background: none; color: var(--iris); text-decoration: underline; }
  .detail :global(.btn.danger) { color: var(--coral); }
  .detail :global(.btn.quiet) { color: var(--muted); }
  @media (max-width: 760px) { .detail { padding-left: var(--s2); } }
</style>
