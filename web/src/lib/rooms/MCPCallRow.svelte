<script lang="ts">
  import type { EventPost, EventSurface } from './eventRenderers.ts'
  import { isMCPCallEvent } from './events.gen.ts'

  let { post, surface = 'feed' }: { post: EventPost; surface?: EventSurface } = $props()
  let call = $derived(isMCPCallEvent(post.payload) ? post.payload : undefined)
</script>

<div class:dm={surface === 'dm'} class="mcp-call" data-id={post.id}>
  {#if call}
    <span class="label">External MCP</span>
    <span>{call.server} / {call.tool}</span>
    <span>by {call.session}</span>
    <span class="outcome">{call.outcome}</span>
    <span>{call.resultBytes ?? 0} bytes · {call.durationMs ?? 0} ms</span>
  {:else}
    <span>External MCP call</span>
  {/if}
</div>

<style>
  .mcp-call { margin: 6px var(--s4) 6px var(--gutter, 56px); padding: 6px 0; border-top: 1px solid var(--hair); display: flex; flex-wrap: wrap; gap: 8px; color: var(--muted); font-size: var(--t-sm); }
  .mcp-call.dm { margin: 4px 0; }
  .label { color: var(--ink-2); font-weight: 600; }
  .outcome { color: var(--ink-2); }
</style>
