<script lang="ts">
  // A telephone handset, for the owner's answer to a call request. Drawn like
  // the Hook and Pin icons: a 1.8 stroke with round caps on a 20 px grid, so
  // it sits with the other rows' glyphs.
  let { size = 15 }: { size?: number } = $props()
</script>

<svg viewBox="0 0 20 20" width={size} height={size} aria-hidden="true">
  <path d="M5.2 3.5h2.6l1.3 3.2-1.7 1.4a8.4 8.4 0 0 0 4.5 4.5l1.4-1.7 3.2 1.3v2.6a1.6 1.6 0 0 1-1.8 1.6A13.3 13.3 0 0 1 3.6 5.3a1.6 1.6 0 0 1 1.6-1.8Z" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" />
</svg>
