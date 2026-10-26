<script lang="ts">
  import HarnessIcon from './HarnessIcon.svelte'
  import ToolEditor from './ToolEditor.svelte'
  import Switch from '../../ui/Switch.svelte'
  import EmptyState from '../../ui/EmptyState.svelte'
  import InlineConfirm from '../../ui/InlineConfirm.svelte'
  import { HARNESS_ORDER, toolEnabledForHarness, toolSummary, type HarnessRow } from './state.ts'
  import type { HarnessConfig, ManagedMCP } from '../../api/harness.ts'

  // The MCP server catalog, with one switch per harness. The pane owns the
  // catalog and the inline confirm, so every change goes back to it. Skills
  // have their own global pane.
  let {
    mcp = [],
    rows = [],
    busy = false,
    addToolHint = $bindable(''),
    removingTool = $bindable<{ kind: string; name: string } | null>(null),
    onadded,
    ontoggle,
    onremovetool,
  }: {
    mcp?: ManagedMCP[]
    rows?: HarnessRow[]
    busy?: boolean
    addToolHint?: string
    removingTool?: { kind: string; name: string } | null
    onadded: (catalog: HarnessConfig) => void
    ontoggle: (kind: 'mcp', name: string, harnessId: string, enabled: boolean) => void
    onremovetool: () => void
  } = $props()
</script>

<div id="tools-panel" role="tabpanel" aria-labelledby="harnesses-tools-tab">
  <div class="tools-bar">
    <p class="muted small" style="margin:0;max-width:520px">
      Choose which harness can use each MCP server in Dyna’s catalog.
    </p>
    <span class="btn-group">
      <button class="btn" onclick={() => (addToolHint = addToolHint === 'mcp' ? '' : 'mcp')}>Add MCP server</button>
    </span>
  </div>

  {#if addToolHint}
    {#key addToolHint}
      <ToolEditor oncancel={() => addToolHint = ''} onsave={onadded} />
    {/key}
  {/if}

  <div class="thead">
    <span></span>
    {#each HARNESS_ORDER as h (h.id)}
      <span class="hlabel" title={h.name}><HarnessIcon id={h.iconId} /><span>{h.short}</span></span>
    {/each}
  </div>

  {#each mcp as entry (entry.name)}
    <div class="trow">
      <div>
        <div class="name">{entry.name} <span class="tag">MCP</span> {#if removingTool?.kind === 'mcp' && removingTool.name === entry.name}<InlineConfirm {busy} onconfirm={onremovetool} oncancel={() => (removingTool = null)} />{:else}<button class="remove-tool" aria-label={`Remove ${entry.name} MCP server`} disabled={busy} onclick={() => (removingTool = { kind: 'mcp', name: entry.name })}>Remove</button>{/if}</div>
        <div class="muted small">{toolSummary(entry)}</div>
      </div>
      {#each HARNESS_ORDER as h (h.id)}
        {@const on = toolEnabledForHarness(entry, h.id)}
        <Switch {on} label={`${entry.name} enabled for ${h.name}`} disabled={busy || !rows.find((r) => r.id === h.id)?.capability?.mcp} onchange={(v: boolean) => ontoggle('mcp', entry.name, h.id, v)} />
      {/each}
    </div>
  {/each}

  {#if !mcp.length}
    <EmptyState title="No MCP servers yet">Add an MCP server above, and pick which harnesses can use it.</EmptyState>
  {/if}
</div>

<style>
  .btn-group { display: flex; flex-wrap: wrap; align-items: center; gap: var(--s2); }
  .muted { color: var(--muted); }
  .small { font-size: var(--t-sm); }
  .name { font-size: var(--t-body); font-weight: 500; }
  .remove-tool { color: var(--muted); font-size: var(--t-cap); margin-left: var(--s2); text-decoration: underline; }
  .tag { font-size: var(--t-cap); color: var(--muted); border: 1px solid var(--line-soft); border-radius: 4px; padding: 1px 6px; }
  .tools-bar { display: flex; justify-content: space-between; align-items: center; flex-wrap: wrap; gap: var(--s3); margin: 18px 0 6px; }
  #tools-panel { overflow-x: auto; }
  .thead, .trow { min-width: 720px; display: grid; grid-template-columns: minmax(0, 1fr) repeat(8, 56px); align-items: center; justify-items: center; gap: 0; text-align: center; padding: 11px 0; border-bottom: 1px solid var(--line-soft); }
  .thead { font-size: var(--t-cap); color: var(--muted); text-transform: none; letter-spacing: 0; }
  .hlabel { display: flex; flex-direction: column; align-items: center; gap: 6px; font-size: var(--t-cap); }
  .trow > div:first-child { text-align: left; min-width: 0; padding-right: var(--s2); overflow-wrap: anywhere; }
  .trow > div:first-child { justify-self: stretch; }
  @media (max-width: 720px) {
    .thead, .trow { grid-template-columns: minmax(120px, 1fr) repeat(8, 56px); font-size: var(--t-cap); }
  }
</style>
