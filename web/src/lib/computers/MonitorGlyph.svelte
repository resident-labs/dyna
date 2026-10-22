<script>
  // The small monitor that marks a session whose computer runs.
  let { size = 14, live = true } = $props()
</script>

<svg viewBox="0 0 20 20" width={size} height={size} aria-hidden="true" class:live><rect x="2.5" y="3.5" width="15" height="10" rx="1.6" fill={live ? 'currentColor' : 'none'} fill-opacity="0.22" stroke="currentColor" stroke-width="1.6" /><path d="M7 17h6M10 13.5V17" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" /></svg>

<style>
  svg { color: var(--faint); flex: none; }
  svg.live { color: var(--gold); }
</style>
