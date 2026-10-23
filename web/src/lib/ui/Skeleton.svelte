<script lang="ts">
  // Skeleton stands in for a list of messages while it loads: an avatar and
  // two lines per row, with a slow shimmer that reduced motion turns off.
  interface Props {
    label?: string
    rows?: number
  }
  let { label = 'Loading', rows = 4 }: Props = $props()
  // Fixed widths read as text without looking random on every load.
  const widths = ['72%', '54%', '86%', '63%', '78%', '48%']
</script>

<div class="skeleton" role="status" aria-label={label}>
  {#each { length: rows } as _, i}
    <div class="sk-row">
      <span class="sk sk-avatar"></span>
      <span class="sk-lines"><span class="sk sk-name"></span><span class="sk sk-text" style:width={widths[i % widths.length]}></span></span>
    </div>
  {/each}
</div>

<style>
  .skeleton { display: flex; flex-direction: column; gap: 18px; padding: 20px var(--s4); flex: 1; width: 100%; box-sizing: border-box; }
  .sk-row { display: grid; grid-template-columns: 40px minmax(0, 1fr); gap: var(--s4); align-items: start; }
  .sk-lines { display: flex; flex-direction: column; gap: var(--s2); padding-top: var(--s1); width: min(100%, 640px); }
  .sk { display: block; border-radius: 6px; background: linear-gradient(90deg, var(--raise) 0%, var(--raise-hover) 50%, var(--raise) 100%) 0 0 / 200% 100%; animation: shimmer 1.4s ease-in-out infinite; }
  .sk-avatar { width: 40px; height: 40px; border-radius: 50%; }
  .sk-name { width: 120px; height: 12px; }
  .sk-text { height: 12px; }
  @keyframes shimmer { from { background-position: 100% 0; } to { background-position: -100% 0; } }
</style>
