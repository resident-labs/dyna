<script lang="ts">
  // A state that needs the owner, drawn as T3 Code draws thread status: a small dot and a short label in the tone's
  // color, with no fill or border. In a dense row, dot shows a larger dot alone; its label moves to the tooltip.
  let { tone, label, title = label, dot = false }: {
    tone: "warning" | "error" | "blocked" | "failed"; label: string; title?: string; dot?: boolean;
  } = $props();
</script>
<span class="status" class:dot class:error={tone !== "warning"} {title} role={dot ? "img" : undefined} aria-label={dot ? title : undefined}><i></i>{#if !dot}{label}{/if}</span>
<style>
  .status { display: inline-flex; align-items: center; gap: 5px; flex: none; color: var(--attention); opacity: 0.9;
    font-size: 12.5px; font-weight: 500; line-height: 1.25; white-space: nowrap; }
  .error { color: var(--danger); }
  i { width: 6px; height: 6px; border-radius: 50%; background: currentColor; }
  .dot { width: 14px; height: 14px; justify-content: center; }
  .dot i { width: 9px; height: 9px; }
</style>
