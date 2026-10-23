<script lang="ts">
  // One small "Mention in chat" button for a PR or issue row and its detail
  // page. The caller builds the reference; this keeps one label and one look.
  let { onmention }: { onmention: () => void } = $props()
</script>

<button type="button" class="mention" onclick={onmention}>Mention in chat</button>

<style>
  .mention { flex: none; font-size: 12.5px; font-weight: 600; color: var(--ink-2); background: var(--raise); border-radius: 6px; padding: 5px 12px; white-space: nowrap; }
  .mention:hover { color: var(--ink); background: var(--raise-hover); }
  .mention:active { background: var(--select); }
  @media (max-width: 760px) { .mention { min-height: 44px; padding: 0 14px; } }
</style>
