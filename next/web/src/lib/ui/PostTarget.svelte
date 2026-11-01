<script lang="ts">
  let { to, onjump }: { to: string; onjump?: () => void } = $props();
</script>

{#if onjump}<button type="button" class="to" onclick={onjump}>→ {to}</button>{:else}<span class="to">→ {to}</span>{/if}

<style>
  .to { min-width: 0; overflow: hidden; text-overflow: ellipsis; color: var(--muted); font-size: 12.5px; font-variant-numeric: tabular-nums; white-space: nowrap; }
  button { all: unset; display: inline-block; max-width: 100%; box-sizing: border-box; cursor: pointer; }
  button:hover { text-decoration: underline; }
  button:focus-visible { outline: 2px solid var(--focus); outline-offset: 2px; }
  @media (pointer: coarse) { button { display: inline-flex; align-items: center; min-height: 40px; } }
</style>
