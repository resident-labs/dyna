<script lang="ts">
  import type { EngineRestarted } from "../contract.gen.ts";
  import { engineRestartParts } from "../engine-restarted.ts";

  let { event }: { event: EngineRestarted } = $props();
</script>

{#each engineRestartParts(event) as part}{#if part.href}<a href={part.href} target="_blank" rel="noopener noreferrer">{part.text}</a>{:else}{part.text}{/if}{/each}

<style>
  a { color: inherit; text-decoration: underline; text-underline-offset: 2px; }
</style>
