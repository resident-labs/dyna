<!-- Ported from T3 Code (apps/web/src/components/chat/TimelineUserMessageRow.tsx), MIT License, Copyright (c) 2026 T3 Tools Inc. See next/third_party/t3code/LICENSE. -->
<script lang="ts">
  import type { Snippet } from "svelte";
  // Dyna: Measured rather than counted, so rendered Markdown folds by what it shows. Text a little over the limit shows whole.
  const limit = 132, slack = 44;
  let { children }: { children: Snippet } = $props();
  let height = $state(0), open = $state(false);
  let long = $derived(height > limit + slack);
</script>

<div class="body" class:folded={long && !open} style:--limit="{limit}px"><div bind:clientHeight={height}>{@render children()}</div></div>
{#if long}<button aria-expanded={open} onclick={() => { open = !open; }}>{open ? "Show less" : "Show more"}</button>{/if}

<style>
  .body { min-width: 0; }
  /* A mask reads only alpha, so any opaque token serves. */
  .folded { max-height: var(--limit); overflow: hidden; mask-image: linear-gradient(to bottom, var(--text) calc(100% - 1.75rem), transparent); }
  button { display: block; margin-top: 4px; padding: 0; border: 0; background: none; color: var(--muted); font: inherit; font-size: 12.5px; cursor: pointer; }
  button:hover { color: var(--text); }
  button:focus-visible { outline: 2px solid var(--focus); }
  @media (pointer: coarse) { button { min-height: 40px; } }
</style>
