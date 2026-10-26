<script lang="ts">
  // The avatar of a workflow run as a sender: a small graph of three linked
  // nodes, so a run never wears a session's initials.
  let { size = 40 }: { size?: number } = $props()
</script>

<span class="run-avatar" style="--s:{size}px" aria-hidden="true">
  <svg viewBox="0 0 20 20"><path d="M6 6.5 14 10M6 13.5 14 10" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" /><circle cx="5.5" cy="6" r="2.2" fill="currentColor" /><circle cx="5.5" cy="14" r="2.2" fill="currentColor" /><circle cx="14.5" cy="10" r="2.6" fill="currentColor" /></svg>
</span>

<style>
  .run-avatar { width: var(--s); height: var(--s); border-radius: 50%; display: inline-grid; place-items: center; flex: none; background: color-mix(in srgb, var(--gold) 18%, #1d1c1f); color: var(--gold); }
  svg { width: 56%; height: 56%; }
</style>
