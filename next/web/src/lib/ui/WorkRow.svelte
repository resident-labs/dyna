<script lang="ts">
  import { describe, workSummary } from "../feed.ts";
  import type { Row } from "../feed.ts";
  let { row, name, open, ontoggle }: {
    row: Extract<Row, { kind: "work" }>; name: (session: string) => string; open: boolean; ontoggle: () => void;
  } = $props();
</script>

<div class="work">
  <button type="button" aria-expanded={open} onclick={ontoggle}><span class="chevron" class:open aria-hidden="true">›</span>{workSummary(row.events, name)}</button>
  {#if open}{#each row.events as event (event.seq)}<p>{describe(event, name)}</p>{/each}{/if}
</div>

<style>
  .work { padding: 12px 16px 0 var(--gutter); color: var(--muted); font-size: 12px; }
  button { all: unset; cursor: pointer; border-radius: 4px; }
  button:hover { color: var(--text); }
  button:focus-visible { outline: 2px solid var(--focus); outline-offset: 2px; }
  .chevron { display: inline-block; width: 12px; transition: transform 120ms ease-out; }
  .open { transform: rotate(90deg); }
  p { margin: 2px 0 0 12px; }
</style>
