<script lang="ts">
  import { onMount, onDestroy } from 'svelte'
  import { api } from '../../../api.ts'
  import TabHeader from '../../rooms/TabHeader.svelte'
  import Loading from '../Loading.svelte'
  import Switch from '../../ui/Switch.svelte'
  import EmptyState from '../../ui/EmptyState.svelte'
  import InlineConfirm from '../../ui/InlineConfirm.svelte'
  import HarnessIcon from '../harnesses/HarnessIcon.svelte'
  import McpForm from './McpForm.svelte'
  import { HARNESS_ORDER, failText, toolEnabledForHarness, toolSummary } from '../harnesses/state.ts'
  import type { HarnessConfig, HarnessSnapshot, ManagedMCP } from '../../api/harness.ts'

  // Dyna's MCP catalog. Each harness loads its own native MCP config, so a
  // server has one switch per harness that supports MCP.
  let catalog = $state<HarnessConfig | null>(null)
  let snapshot = $state<HarnessSnapshot | null>(null)
  let loading = $state(true)
  let busy = $state(false)
  let error = $state('')
  let notice = $state('')
  let adding = $state(false) // the add form is open
  let removing = $state('') // the server waiting for an inline confirm

  let stopped = false
  const servers = $derived(catalog?.mcp ?? [])

  // supports says whether one harness can load MCP servers at all.
  function supports(harnessId: string): boolean {
    return (snapshot?.capabilities ?? []).find((c) => c.harness === harnessId)?.mcp ?? false
  }

  async function load() {
    if (stopped) return
    loading = true
    error = ''
    const [runtimes, config] = await Promise.allSettled([api.privateRuntimes(), api.harnessConfig()])
    if (stopped) return
    // The catalog decides the rows; a failed runtime read only disables the switches.
    if (config.status === 'fulfilled') catalog = config.value
    else error = failText(config.reason, 'Could not load MCP servers.')
    if (runtimes.status === 'fulfilled') snapshot = runtimes.value
    loading = false
  }

  onMount(load)
  onDestroy(() => { stopped = true })

  function added(next: HarnessConfig) {
    if (stopped) return
    catalog = next
    adding = false
    error = ''
    notice = 'MCP server added. Turn it on for each harness that should load it.'
  }

  async function setEnabled(entry: ManagedMCP, harnessId: string, enabled: boolean) {
    if (busy || stopped) return
    busy = true
    error = ''
    notice = ''
    try {
      const next = await api.setHarnessConfigEnabled('mcp', entry.name, harnessId, enabled)
      if (!stopped) {
        catalog = next
        notice = 'Saved. Future turns use the new choice.'
      }
    } catch (e) {
      if (!stopped) error = failText(e, 'Could not change this server. Try again.')
    } finally {
      if (!stopped) busy = false
    }
  }

  async function removeServer() {
    if (busy || stopped || !removing) return
    const name = removing
    busy = true
    error = ''
    notice = ''
    try {
      const next = await api.removeHarnessConfig('mcp', name)
      if (!stopped) {
        catalog = next
        removing = ''
        notice = 'MCP server removed.'
      }
    } catch (e) {
      if (!stopped) error = failText(e, 'Could not remove this server.')
    } finally {
      if (!stopped) busy = false
    }
  }
</script>

<section class="mcp" aria-busy={loading || busy}>
  <TabHeader flush title="MCP servers" blurb="Dyna owns this catalog. Each harness loads its own native MCP config, so changes apply to future turns.">
    {#snippet action()}<button class="btn" onclick={() => (adding = !adding)}>{adding ? 'Close' : 'Add MCP server'}</button>{/snippet}
  </TabHeader>

  {#if error}<p class="error" role="alert">{error}</p>{/if}
  {#if notice}<p class="notice" role="status">{notice}</p>{/if}

  {#if adding}
    <McpForm oncancel={() => (adding = false)} onsave={added} />
  {/if}

  {#if loading && !catalog}
    <Loading>Loading MCP servers…</Loading>
  {:else if !servers.length}
    <EmptyState title="No MCP servers yet">Add a server with a local command or a remote URL. Then turn it on for each harness that should load it.</EmptyState>
  {:else}
    <div class="table">
      <div class="thead">
        <span>MCP server</span>
        {#each HARNESS_ORDER as h (h.id)}
          <span class="hlabel" title={supports(h.id) ? h.name : h.name + ' does not load MCP servers'}><HarnessIcon id={h.iconId} /><span>{h.short}</span></span>
        {/each}
        <span></span>
      </div>
      {#each servers as entry (entry.name)}
        {@const line = toolSummary(entry)}
        <div class="mrow">
          <div class="what">
            <div class="name">{entry.name}</div>
            <div class="mono summary" title={line}>{line}</div>
          </div>
          {#each HARNESS_ORDER as h (h.id)}
            {@const on = toolEnabledForHarness(entry, h.id)}
            <Switch {on} label={`${entry.name} enabled for ${h.name}`} disabled={busy || !supports(h.id)} onchange={(v: boolean) => setEnabled(entry, h.id, v)} />
          {/each}
          <div class="act">
            {#if removing !== entry.name}
              <button class="remove" aria-label={`Remove ${entry.name} MCP server`} disabled={busy} onclick={() => (removing = entry.name)}>Remove</button>
            {/if}
          </div>
        </div>
        {#if removing === entry.name}
          <div class="ask"><InlineConfirm ask="Remove server?" {busy} onconfirm={removeServer} oncancel={() => (removing = '')} /></div>
        {/if}
      {/each}
    </div>
  {/if}
</section>

<style>
  .mcp { container-type: inline-size; }
  .error { color: var(--coral); font-size: var(--t-ui); margin: var(--s3) 0 0; }
  .notice { color: var(--muted); font-size: var(--t-sm); margin: var(--s3) 0 0; }
  /* The table fits a narrow settings pane: two 56 px harness columns and a
     compact Remove leave the name and summary most of the width. It scrolls
     sideways only when the pane is below 380 px. */
  .table { overflow-x: auto; }
  .thead, .mrow { min-width: 380px; display: grid; grid-template-columns: minmax(0, 1fr) repeat(2, 56px) 64px; align-items: center; gap: 0; text-align: center; padding: 11px 0; border-bottom: 1px solid var(--line-soft); }
  .thead { font-size: var(--t-cap); color: var(--muted); }
  .thead > span:first-child { text-align: left; }
  .hlabel { display: flex; flex-direction: column; align-items: center; gap: 6px; font-size: var(--t-cap); }
  .mrow > .what { text-align: left; min-width: 0; padding-right: var(--s2); justify-self: stretch; }
  .name { font-size: var(--t-body); font-weight: 500; overflow-wrap: anywhere; }
  .summary { color: var(--muted); font-size: var(--t-cap); overflow-wrap: anywhere; }
  .mrow > :global(.switch) { justify-self: center; }
  .act { justify-self: end; padding-right: var(--s1); }
  .remove { color: var(--muted); font-size: var(--t-cap); text-decoration: underline; }
  /* The question needs more room than a column has, so it takes its own line. */
  .ask { padding: 0 0 var(--s3); border-bottom: 1px solid var(--line-soft); }
  /* On a phone pane the Remove action drops under the server, so the two
     harness switches still fit without a sideways scroll. */
  @container (max-width: 400px) {
    .thead, .mrow { min-width: 0; grid-template-columns: minmax(0, 1fr) repeat(2, 56px); }
    .thead > span:last-child { display: none; }
    .mrow > .act { grid-column: 1 / -1; justify-self: start; padding: var(--s2) 0 0; }
  }
</style>
