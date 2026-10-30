<script lang="ts">
  import type { Snippet } from 'svelte'

  interface Step { id: string; time: string; label: string }
  let { steps, row }: { steps: Step[]; row?: Snippet<[number]> } = $props()
</script>

{#if steps.length > 1}
  <details>
    <summary>Show steps ({steps.length})</summary>
    {#each steps as step, i (step.id)}
      {#if row}{@render row(i)}{:else}<div class="step"><time>{step.time}</time> · {step.label}</div>{/if}
    {/each}
  </details>
{/if}

<style>
  .step { color: var(--muted); }
</style>
