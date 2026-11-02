<script lang="ts">
  import { wrapParts } from "../wrap.ts";
  // Text an agent wrote, with its long tokens free to wrap at their seams instead of mid-word.
  let { text }: { text: string } = $props();
</script>

{#each wrapParts(text) as part, i}{#if i}<wbr />{/if}{part}{/each}
