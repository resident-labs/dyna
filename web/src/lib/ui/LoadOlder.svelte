<script lang="ts">
  // The control that loads an older page. With `auto` the end of the list
  // loads it when it scrolls into view and shows only a muted "Loading older…"
  // while a request runs; it re-checks after each page, so a short page keeps
  // loading until the end leaves the screen. After a failed load (`failed`) it
  // stops and shows a small Retry button. Without `auto` it is the plain button.
  interface Props {
    older?: boolean
    loading?: boolean
    auto?: boolean
    failed?: boolean
    onclick: () => void
  }
  let { older = false, loading = false, auto = false, failed = false, onclick }: Props = $props()
  let end: HTMLDivElement | undefined = $state()
  let watching = $derived(auto && !failed)
  $effect(() => {
    if (!watching || !older || loading || !end) return
    const seen = new IntersectionObserver((entries) => entries.some((e) => e.isIntersecting) && onclick())
    seen.observe(end)
    return () => seen.disconnect()
  })
</script>
{#if older}
  {#if watching}
    <div class="end" bind:this={end}>{#if loading}Loading older…{/if}</div>
  {:else}
    <button class="load-older" class:retry={failed} disabled={loading} {onclick}>{loading ? 'Loading…' : failed ? 'Could not load older. Retry' : 'Load older'}</button>
  {/if}
{/if}
<style>
  .load-older { margin: var(--s2) var(--s4); padding: 10px 14px; color: var(--iris); background: var(--raise); border-radius: 6px; }
  .load-older.retry { background: none; padding: 6px var(--s2); font-size: var(--t-sm); color: var(--muted); text-decoration: underline; }
  .load-older:disabled { opacity: 0.6; }
  .end { min-height: 24px; padding: 0 var(--s2); font-size: var(--t-cap); color: var(--faint); }
</style>
