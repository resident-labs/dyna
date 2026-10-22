<script lang="ts">
  // The magnifier for find and search buttons, on a 24 px grid.
  let { size = 20 }: { size?: number } = $props()
</script>

<svg viewBox="0 0 24 24" width={size} height={size} aria-hidden="true"><circle cx="10.5" cy="10.5" r="6" fill="none" stroke="currentColor" stroke-width="1.9" /><path d="M15 15l5 5" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" /></svg>
