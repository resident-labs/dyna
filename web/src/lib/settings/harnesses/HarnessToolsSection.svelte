<script lang="ts">
  import Switch from '../../ui/Switch.svelte'
  import EmptyState from '../../ui/EmptyState.svelte'
  import { toolEnabledForHarness, type HarnessRow } from './state.ts'
  import type { ManagedMCP, ManagedSkill } from '../../api/harness.ts'

  // The tools one harness may use, as switches. The pane saves each change.
  let {
    row,
    mcp = [],
    skills = [],
    busy = false,
    ontoggle,
  }: {
    row: HarnessRow
    mcp?: ManagedMCP[]
    skills?: ManagedSkill[]
    busy?: boolean
    ontoggle: (kind: 'mcp' | 'skill', name: string, enabled: boolean) => void
  } = $props()
</script>

<div class="sec">
  <h3 class="section-label">Tools in {row.meta.name}</h3>
  {#each mcp as entry (entry.name)}
    {@const on = toolEnabledForHarness(entry, row.id)}
    <div class="kv">
      <span>{entry.name} <span class="muted small">MCP</span></span>
      <Switch {on} label={`${entry.name} MCP in ${row.meta.name}`} disabled={busy || !row.capability?.mcp} onchange={(v: boolean) => ontoggle('mcp', entry.name, v)} />
    </div>
  {/each}
  {#each skills as entry (entry.name)}
    {@const on = toolEnabledForHarness(entry, row.id)}
    <div class="kv">
      <span>{entry.name} <span class="muted small">Skill</span></span>
      <Switch {on} label={`${entry.name} skill in ${row.meta.name}`} disabled={busy || !row.capability?.skills} onchange={(v: boolean) => ontoggle('skill', entry.name, v)} />
    </div>
  {/each}
  {#if !mcp.length && !skills.length}
    <EmptyState title="No tools yet">Add one in the MCP servers & skills tab.</EmptyState>
  {/if}
</div>

<style>
  .muted { color: var(--muted); }
  .small { font-size: var(--t-sm); }
  .sec { border-top: 1px solid var(--line); padding: var(--s4) 0; }
  .sec .section-label { margin-top: 0; }
  .kv { flex-wrap: wrap; display: flex; align-items: center; justify-content: space-between; gap: var(--s3); padding: 6px 0; font-size: var(--t-ui); }
</style>
