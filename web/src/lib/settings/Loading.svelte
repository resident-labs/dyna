<script lang="ts">
  // The one loading line for a settings pane: muted, no colour meaning.
  import type { Snippet } from 'svelte'
  let { children }: { children?: Snippet } = $props()
</script>
<p role="status">{@render children?.()}</p>
<style>
  p { margin: var(--s3) 0 0; color: var(--faint); font-size: var(--t-ui); }
</style>
