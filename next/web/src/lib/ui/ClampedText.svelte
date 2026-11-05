<script lang="ts">
 import type { Snippet } from "svelte";
 let { text, prefix }: { text: string; prefix?: Snippet } = $props();
 const id = $props.id();
 let expanded = $state(false);
 let overflowing = $state(false);
 let body: HTMLDivElement;
 $effect(() => { text; expanded = false; });
 $effect(() => {
  text;
  const measure = () => { overflowing = body.scrollHeight > body.clientHeight + 1; };
  const observer = new ResizeObserver(measure);
  observer.observe(body); measure();
  return () => observer.disconnect();
 });
</script>
<div class="value" class:clamped={!expanded} bind:this={body} {id}>{@render prefix?.()}{text}</div>
{#if overflowing || expanded}<button type="button" class="toggle" aria-expanded={expanded} aria-controls={id} onclick={() => { expanded = !expanded; }}>{expanded ? "Show less" : "Show all"}</button>{/if}
<style>
 .value { white-space: pre-wrap; overflow-wrap: anywhere; min-width: 0; }
 .clamped { display: -webkit-box; -webkit-box-orient: vertical; -webkit-line-clamp: 3; line-clamp: 3; overflow: hidden; }
 .toggle { border: 0; padding: 2px 0; color: var(--muted); background: none; font: inherit; font-size: 11px; cursor: pointer; }
 .toggle:hover { color: var(--text); text-decoration: underline; }
 .toggle:focus-visible { outline: 2px solid var(--focus); outline-offset: 2px; }
</style>
