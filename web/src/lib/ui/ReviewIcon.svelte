<script lang="ts">
  // The Review icon: a document with a check, on a 24 px grid. Review opens a
  // full screen on a phone and a panel on a desktop, so the glyph says what
  // the screen holds and never suggests a layout.
  let { size = 22 }: { size?: number } = $props()
</script>

<svg viewBox="0 0 24 24" width={size} height={size} aria-hidden="true">
  <path d="M13.5 3.5H7A1.5 1.5 0 0 0 5.5 5v14A1.5 1.5 0 0 0 7 20.5h10a1.5 1.5 0 0 0 1.5-1.5V8.5Z" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linejoin="round" />
  <path d="M13.5 3.5v5h5" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linejoin="round" />
  <path d="m8.8 14.2 2.2 2.2 4.2-4.6" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" />
</svg>
