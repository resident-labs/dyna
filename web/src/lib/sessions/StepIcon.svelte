<script lang="ts">
  // The icon of one session step: what the agent did, not which tool it used.
  // A kind outside the set draws the neutral tool glyph, so an older item still
  // gets a row.
  let { kind = '', size = 14 }: { kind?: string; size?: number } = $props()
</script>

<span class="ic" style="--s:{size}px" aria-hidden="true">
  <svg viewBox="0 0 20 20" width={size} height={size} fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round">
    {#if kind === 'read'}
      <path d="M5 3.5h6.5L15 7v9.5H5z" /><path d="M11.5 3.5V7H15" />
    {:else if kind === 'edit'}
      <path d="M4 16h3l8.5-8.5-3-3L4 13z" /><path d="M12 5.5l2.5 2.5" />
    {:else if kind === 'write'}
      <path d="M5 3.5h10v13H5z" /><path d="M8 7h4M8 10h4M8 13h2" />
    {:else if kind === 'search'}
      <circle cx="9" cy="9" r="4.5" /><path d="M12.5 12.5L16 16" />
    {:else if kind === 'list'}
      <path d="M7 6h9M7 10h9M7 14h9" /><path d="M4 6h.01M4 10h.01M4 14h.01" />
    {:else if kind === 'run'}
      <rect x="3" y="4.5" width="14" height="11" rx="2" /><path d="M6.5 8.5l2.5 2-2.5 2M11 12.5h3" />
    {:else if kind === 'fetch'}
      <circle cx="10" cy="10" r="6.5" /><path d="M3.5 10h13M10 3.5c2 2 2 11 0 13M10 3.5c-2 2-2 11 0 13" />
    {:else if kind === 'delegate'}
      <circle cx="7" cy="7" r="2.5" /><path d="M3 16c0-2.5 1.8-4.5 4-4.5s4 2 4 4.5" /><path d="M13 6.5h4M15 4.5l2 2-2 2" />
    {:else if kind === 'plan'}
      <path d="M4 6h3M9 6h7M4 10h3M9 10h7M4 14h3M9 14h7" />
    {:else if kind === 'monitor'}
      <path d="M3 12l3-4 3 3 3-6 4 8" /><path d="M3 16h14" />
    {:else}
      <rect x="4" y="4" width="12" height="12" rx="2" /><path d="M8 8h4M8 12h4" />
    {/if}
  </svg>
</span>

<style>
  .ic { display: inline-grid; place-items: center; flex: none; width: var(--s); height: var(--s); color: var(--muted); }
</style>
