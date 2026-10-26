<script lang="ts">
  // The body of a workflow script's post: prose as markdown, and each JSON
  // object or array as a closed key and value view.
  import { markdown } from '../ui/format.ts'
  import { splitPost } from './dataBlocks.ts'
  import DataView from './DataView.svelte'

  let { text, id }: { text: string; id?: string } = $props()
  let segments = $derived(splitPost(text))
</script>

<div {id} class="workflow-body">
  {#each segments as seg, i (i)}
    {#if seg.kind === 'text'}<div class="prose">{@html markdown(seg.text)}</div>
    {:else}<DataView value={seg.value} />{/if}
  {/each}
</div>

<style>
  .workflow-body { min-width: 0; margin-top: 1px; }
</style>
