<script lang="ts">
  // The Expedited badge. Callers show it only for an expedited goal, the
  // agents that hold it and their children, or a PR linked to it.
  import Chip from '../ui/Chip.svelte'
  let { short = false }: { short?: boolean } = $props()
</script>

<Chip tone="gold" title="Expedited: this work goes first in review and merge">{short ? '⚡' : '⚡ Expedited'}</Chip>
