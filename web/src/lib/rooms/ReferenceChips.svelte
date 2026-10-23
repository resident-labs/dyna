<script lang="ts">
  import { referenceKey, referenceLabel, type Reference } from './references.ts'
  let { references = [], onremove, disabled = false }: { references?: Reference[], onremove?: (reference: Reference) => void, disabled?: boolean } = $props()
</script>

{#if references.length}
  <div class="references" aria-label="PR and issue references">
    {#each references as reference (referenceKey(reference))}
      <span class="reference">
        <a href={reference.url} title={`${reference.title} (${reference.state})`}>{referenceLabel(reference)}: {reference.title}</a>
        {#if onremove}<button type="button" {disabled} onclick={() => onremove?.(reference)} aria-label={`Remove ${referenceLabel(reference)}`}>×</button>{/if}
      </span>
    {/each}
  </div>
{/if}

<style>
  .references { display: flex; flex-wrap: wrap; gap: 6px; padding: 8px 10px; }
  .reference { display: inline-flex; align-items: center; max-width: 100%; border: 1px solid var(--line); border-radius: 6px; background: var(--side); font-size: var(--t-sm); }
  a { color: var(--iris); padding: 5px 7px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  button { color: var(--muted); padding: 5px 8px; min-width: 30px; }
  @media (max-width: 760px) { button { min-width: 44px; min-height: 44px; } }
</style>
