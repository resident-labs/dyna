<script lang="ts">
  import Icon from "./Icon.svelte";
  import { describe, workSummary } from "../feed.ts";
  import type { Row } from "../feed.ts";
  // Folded work reads as one quiet line that starts with a chevron, as T3 Code's "Worked for 20s";
  // it opens in place to one line per event.
  let { row, name, open, ontoggle }: {
    row: Extract<Row, { kind: "work" }>; name: (session: string) => string; open: boolean; ontoggle: () => void;
  } = $props();
</script>

<div class="work">
  <button type="button" aria-expanded={open} onclick={ontoggle}><span class="chevron" class:open><Icon name="chevron" size={14} /></span>{workSummary(row.events, name, row.until)}</button>
  {#if open}<ul>{#each row.events as event (event.seq)}{@const line = describe(event, name)}{#if line}<li>{line}</li>{/if}{/each}</ul>{/if}
</div>

<style>
  /* The chevron sits left of the post text's edge, so the words line up with the posts above and below. */
  .work { padding: 2px 16px 2px calc(var(--gutter) - 18px); color: var(--muted); font-size: 12.5px; font-variant-numeric: tabular-nums; }
  button { all: unset; display: inline-flex; align-items: center; gap: 4px; min-height: 24px; cursor: pointer; border-radius: 6px; }
  button:hover { color: var(--text); }
  button:focus-visible { outline: 2px solid var(--focus); outline-offset: 2px; }
  @media (hover: none) { button { min-height: 40px; } }
  .chevron { display: inline-flex; transform: rotate(-90deg); transition: transform 150ms ease-out; }
  .open { transform: none; }
  ul { margin: 2px 0 4px 18px; padding: 0 0 0 12px; border-left: 1px solid var(--line); list-style: none; }
  li { padding: 1px 0; }
</style>
