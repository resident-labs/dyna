<script lang="ts">
  // What a list shows when a call to fill it failed: a short title, one line
  // that says what is missing, and Retry. The raw error never reaches the
  // screen (rule r8); the same block covers an older engine and a failed call.
  interface Props {
    title: string
    hint: string
    onretry: () => void
  }
  let { title, hint, onretry }: Props = $props()
</script>
<div class="failed" role="alert">
  <div><strong>{title}</strong><p>{hint}</p></div>
  <button class="retry" onclick={onretry}>Retry</button>
</div>
<style>
  .failed { display: flex; align-items: center; gap: var(--s3); margin: var(--s3) var(--s4) 0; padding: var(--s3) var(--s4); border: 1px solid var(--line-soft); border-left: 3px solid var(--coral); border-radius: 8px; background: var(--side); }
  .failed div { flex: 1; min-width: 0; }
  strong { font-size: var(--t-ui); }
  p { margin: 2px 0 0; color: var(--muted); font-size: var(--t-sm); }
  .retry { flex: none; font-size: var(--t-sm); font-weight: 600; color: var(--ink-2); padding: 6px 11px; border-radius: 6px; background: var(--raise); transition: color var(--motion-fast) var(--ease), background-color var(--motion-fast) var(--ease); }
  @media (hover: hover) { .retry:hover { color: var(--ink); background: var(--raise-hover); } }
  .retry:active { background: var(--select); }
  @media (pointer: coarse) { .retry { min-block-size: 44px; } }
</style>
