<script lang="ts">
  // The GitHub state icon of a pull request or an issue: open, draft,
  // merged or closed. it is a GitHub item or a change row; both have state.
  import type { GitHubItem } from '../api/github.ts'
  import { itemState } from './github.ts'
  let { kind = 'pr', it, size = 16 }: { kind?: string; it?: Pick<Partial<GitHubItem>, 'state' | 'mergedAt' | 'isDraft' | 'stateReason'> | null; size?: number } = $props()
  let st = $derived(itemState(kind, it))
</script>

<svg class="glyph {st.tone}" viewBox="0 0 16 16" width={size} height={size} aria-label={st.label}>
  {#if kind === 'pr' && st.id === 'merged'}
    <circle cx="4.5" cy="3.5" r="1.6" /><circle cx="4.5" cy="12.5" r="1.6" /><circle cx="11.5" cy="9" r="1.6" /><path d="M4.5 5.1v5.8M4.5 5.2c0 2.6 2.6 3.8 5.4 3.8" />
  {:else if kind === 'pr'}
    <circle cx="4.5" cy="3.5" r="1.6" /><circle cx="4.5" cy="12.5" r="1.6" /><circle cx="11.5" cy="12.5" r="1.6" /><path d="M4.5 5.1v5.8M11.5 10.9V6.5a2 2 0 0 0-2-2H7.5M9 3 7.5 4.5 9 6" />
  {:else if st.id === 'open'}
    <circle cx="8" cy="8" r="6" /><circle cx="8" cy="8" r="1.2" />
  {:else if st.id === 'done'}
    <circle cx="8" cy="8" r="6" /><path d="m5.5 8.2 1.8 1.8 3.4-3.6" />
  {:else}
    <circle cx="8" cy="8" r="6" /><path d="m4 12 8-8" />
  {/if}
</svg>

<style>
  .glyph { flex: none; fill: none; stroke: currentColor; stroke-width: 1.4; stroke-linecap: round; stroke-linejoin: round; margin-top: 2px; }
  .glyph.sage { color: var(--sage); }
  .glyph.iris { color: var(--iris); }
  .glyph.coral { color: var(--coral); }
  .glyph.faint { color: var(--faint); }
</style>
