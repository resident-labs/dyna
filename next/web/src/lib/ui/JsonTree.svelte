<script lang="ts">
 import type { JSONValue } from "../contract.gen.ts";
 import JsonTree from "./JsonTree.svelte";
 let { value, label, root = false }: { value: JSONValue; label?: string; root?: boolean } = $props();
 // The root opens one level when the data chip opens; descendants always start folded.
 // svelte-ignore state_referenced_locally
 let open = $state(root);
 let limit = $state(50);
 const page = 50;
 let array = $derived(Array.isArray(value));
 let collection = $derived(value !== null && typeof value === "object");
 let keys = $derived(collection ? Object.keys(value!) : []);
 let count = $derived(keys.length);
 // No child components exist until opened. At most one page is mounted per expansion.
 let entries = $derived(open && collection ? keys.slice(0, limit).map(key => [key, (value as Record<string, JSONValue>)[key]] as const) : []);
 let countText = $derived(`${count} ${array ? "items" : "keys"}`);
</script>
{#if collection}
 <details {open} ontoggle={event => { open = event.currentTarget.open; }} class:root>
  <summary>{#if label !== undefined}<span class="key">{JSON.stringify(label)}</span>: {/if}<span class="bracket">{array ? "[" : "{"}</span> <span class="count">{countText}</span> <span class="bracket">{array ? "]" : "}"}</span></summary>
  {#if open}<div class="children">{#each entries as [key, child] (key)}<JsonTree value={child} label={key} />{/each}
   {#if count > limit}<button type="button" onclick={() => { limit += page; }}>Show next {Math.min(page, count - limit)} · {count - limit} remaining</button>{/if}
  </div>{/if}
 </details>
{:else}
 <div class="leaf">{#if label !== undefined}<span class="key">{JSON.stringify(label)}</span>: {/if}<span>{JSON.stringify(value)}</span></div>
{/if}
<style>
 details, .leaf { font: 12px/1.7 var(--mono); overflow-wrap: anywhere; min-width: 0; }
 summary { cursor: pointer; border-radius: 3px; }
 summary:hover { background: var(--hover); }
 summary:focus-visible, button:focus-visible { outline: 2px solid var(--focus); outline-offset: 2px; }
 .children { padding-left: 14px; border-left: 1px solid var(--line); margin-left: 4px; }
 .key { color: var(--focus); } .count, .bracket { color: var(--muted); }
 button { color: var(--focus); background: none; border: 0; font: inherit; cursor: pointer; padding: 4px; text-align: left; }
 .leaf { white-space: pre-wrap; }
 @media (max-width: 640px) { .children { padding-left: 8px; margin-left: 2px; } }
</style>
