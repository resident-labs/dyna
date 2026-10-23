<script>
  import CardFrame from '../files/CardFrame.svelte'
  import Files from '../files/Files.svelte'
  import { cardHeight } from '../ui/rich.ts'
  let { roomId, items = [], onopen } = $props()
  let expanded = $state(false)
  let fullHeight = $state(360)
  let cards = $derived(items.filter((a) => a.render === 'html'))
</script>

{#each cards as a (a.id)}
  <div class="card">
    <CardFrame {roomId} att={a} height="{expanded ? cardHeight(fullHeight) : 320}px" onheight={(h) => fullHeight = h} />
    <div class="tools">
      <button onclick={() => expanded = !expanded}>{expanded ? 'Collapse' : 'Expand'}</button>
      <button onclick={() => onopen(a)}>Open</button>
    </div>
  </div>
{/each}
<Files {roomId} items={items.filter((a) => a.render !== 'html')} {onopen} />

<style>
  .card { overflow: hidden; border-radius: 6px; margin-top: 6px; background: var(--side); }
  .tools { display: flex; justify-content: flex-end; gap: var(--s3); padding: 5px 10px; font-size: var(--t-sm); color: var(--muted); }
  .tools button { transition: color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .tools button:hover { color: var(--ink); }
  }
</style>
