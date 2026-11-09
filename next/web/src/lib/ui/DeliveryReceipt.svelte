<script lang="ts">
 import { onMount } from 'svelte';
 import type { PostReceipt } from '../contract.gen.ts';
 import { receiptSummary } from '../receipts.ts';
 // Words only while a post waits or could not go; once a turn takes it, one check, and two once it is read, as a
 // messenger shows them. The tooltip names each recipient's state. A check that arrives while on screen draws in.
 let { receipts }: { receipts: PostReceipt[] } = $props();
 let now = $state(Date.now());
 let fresh = $state(false);
 let previous: string | undefined;
 const summary = $derived(receiptSummary(receipts, now));
 const checks = $derived(summary.label === 'Read' ? 2 : summary.label === 'Awaiting read' ? 1 : 0);
 $effect(() => {
  if (!receipts.length) return;
  const label = summary.label;
  if (previous && previous !== label && checks) fresh = true;
  previous = label;
 });
 onMount(() => {
  const timer = setInterval(() => { now = Date.now(); }, 1000);
  return () => clearInterval(timer);
 });
</script>
{#if receipts.length}
 <button type="button" class="receipt" aria-label={summary.details}>
  {#if checks}<svg class:draw={fresh} class:read={checks === 2} width="18" height="14" viewBox="0 0 20 16" fill="none" aria-hidden="true"><path d="m2 8 3 3 7-7" />{#if checks === 2}<path class="second" d="m9 11 7-7" />{/if}</svg>
  {:else}<span>{summary.label}</span>{#if summary.seconds}<span class="countdown">· in {summary.seconds} s</span>{/if}{/if}
  <span class="tooltip" role="tooltip">{summary.details}</span>
 </button>
{/if}
<style>
 .receipt { padding: 0; border: 0; background: transparent; font-family: inherit; position: relative; display: inline-flex; align-items: center; gap: 4px; height: 20px; color: var(--muted); font-size: 12px; line-height: 20px; cursor: default; border-radius: 3px; }
 .countdown { opacity: .8; font-variant-numeric: tabular-nums; }
 svg path { stroke: currentColor; stroke-width: 1.5; stroke-linecap: round; stroke-linejoin: round; }
 .draw path { stroke-dasharray: 18; animation: check-draw 200ms ease-out both; }
 .draw .second { animation-delay: 120ms; }
 @keyframes check-draw { from { stroke-dashoffset: 18; } to { stroke-dashoffset: 0; } }
 .tooltip { display: none; position: absolute; bottom: 100%; left: 0; z-index: 10; width: max-content; max-width: 280px; white-space: pre-line; padding: 7px 10px; background: var(--raised); color: var(--text); border: 1px solid var(--line); border-radius: 6px; box-shadow: 0 2px 8px var(--shadow-sm); }
 .receipt:hover .tooltip, .receipt:focus .tooltip { display: block; }
 @media (prefers-reduced-motion: reduce) { .draw path { animation: none; } }
</style>
