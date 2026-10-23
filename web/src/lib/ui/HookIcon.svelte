<script lang="ts">
  // A terminal window, for a hook: a command the engine ran. Drawn like the
  // Reply and Pin icons, with a 1.8 stroke and round caps on a 20 px grid, so
  // it sits with the other cards' glyphs.
  let { size = 15 }: { size?: number } = $props()
</script>

<svg viewBox="0 0 20 20" width={size} height={size} aria-hidden="true">
  <rect x="2.5" y="3.5" width="15" height="13" rx="3" fill="none" stroke="currentColor" stroke-width="1.8" />
  <path d="M6 8.2l2.4 2.3L6 12.8M11.2 12.8h3.2" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" />
</svg>
