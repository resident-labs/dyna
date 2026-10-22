<script lang="ts">
  // What a full panel shows when it has nothing to list: a bold title and an
  // optional muted hint line that says what to do about it.
  import type { Snippet } from 'svelte'
  import { fade } from 'svelte/transition'
  import { motion, FAST } from './motion.js'
  interface Props {
    title: string
    children?: Snippet | null
  }
  let { title, children = null }: Props = $props()
</script>
<div class="empty" in:fade={{ duration: motion(FAST) }}>
  <strong>{title}</strong>
  {#if children}<p>{@render children()}</p>{/if}
</div>
<style>
  .empty { margin: var(--s5) var(--s2) 0; background: var(--side); border: 1px dashed var(--line); border-radius: 12px; padding: var(--s5); text-align: center; }
  .empty strong { font-size: var(--t-title); }
  .empty p { margin: var(--s2) 0 0; color: var(--muted); font-size: var(--t-body); }
</style>
