<script lang="ts">
  let { tone, children }: { tone: "warning" | "error" | "blocked" | "failed"; children: import("svelte").Snippet } = $props();
</script>
<span class="pill" class:error={tone !== "warning"}>{@render children()}</span>
<style>
  .pill { color: var(--attention); border: 1px solid currentColor; border-radius: 3px; padding: 1px 5px; font-size: 12px; }
  .error { color: var(--danger); }
</style>
