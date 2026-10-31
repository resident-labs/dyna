<!-- The pulse beside a session whose turn runs; the global reduced-motion rule stops it. -->
<script lang="ts">
  // label names the state where no text beside the dot says it.
  let { label }: { label?: string } = $props();
</script>
<span class="dot" role={label ? "img" : "presentation"} aria-label={label}></span>

<style>
  .dot { flex: none; width: 7px; height: 7px; border-radius: 50%; background: var(--success); animation: pulse 2s ease-in-out infinite; }
  @keyframes pulse { 50% { opacity: 0.35; } }
</style>
