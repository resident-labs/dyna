<script lang="ts">
  import Icon from "./Icon.svelte";
  import { describe, workSummary } from "../feed.ts";
  import type { Row } from "../feed.ts";
  // Folded work reads as one quiet sentence with a chevron after it; it opens in place to one line per event.
  let { row, name, open, ontoggle }: {
    row: Extract<Row, { kind: "work" }>; name: (session: string) => string; open: boolean; ontoggle: () => void;
  } = $props();
</script>

<div class="work">
  <button type="button" aria-expanded={open} onclick={ontoggle}>{workSummary(row.events, name)}<span class="chevron" class:open><Icon name="chevron" size={14} /></span></button>
  {#if open}{#each row.events as event (event.seq)}<p>{describe(event, name)}</p>{/each}{/if}
</div>

<style>
  .work { padding: 6px 16px 2px var(--gutter); color: var(--muted); font-size: 14px; }
  button { all: unset; display: inline-flex; align-items: center; gap: 4px; cursor: pointer; border-radius: 4px; }
  button:hover { color: var(--text); }
  button:focus-visible { outline: 2px solid var(--focus); outline-offset: 2px; }
  .chevron { display: inline-flex; transition: transform 120ms ease-out; }
  .open { transform: rotate(180deg); }
  p { margin: 2px 0 0; font-size: 13px; }
</style>
