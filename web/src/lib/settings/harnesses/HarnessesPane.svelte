<script lang="ts">
  import { onMount, onDestroy, tick } from 'svelte'
  import { fly } from 'svelte/transition'
  import { BASE, motion } from '../../ui/motion.ts'
  import Loading from '../Loading.svelte'
  import { api } from '../../../api.ts'
  import HarnessIcon from './HarnessIcon.svelte'
  import HarnessList from './HarnessList.svelte'
  import HarnessTabs from './HarnessTabs.svelte'
  import InstallControls from './InstallControls.svelte'
  import SettingsImportSection from './SettingsImportSection.svelte'
  import HarnessToolsSection from './HarnessToolsSection.svelte'
  import ToolsTab from './ToolsTab.svelte'
  import Confirm from '../../ui/Confirm.svelte'
  import TabHeader from '../../rooms/TabHeader.svelte'
  import { HARNESS_ORDER, exactVersion, failText, harnessRowState, recommendedVersion } from './state.ts'
  import type { Confirmation, HarnessRow } from './state.ts'
  import type { HarnessAccount, HarnessConfig, HarnessSnapshot, PreferencePreview } from '../../api/harness.ts'

  // Dyna's own copies of the coding agents: install, update, uninstall, the
  // tools they may use and the safe preferences Dyna can import. Sign-ins and
  // usage belong to the Accounts pane.
  let { onmodal = () => {} }: { onmodal?: (open: boolean) => void } = $props()

  let snapshot = $state<HarnessSnapshot | null>(null)
  let catalog = $state<HarnessConfig | null>(null)
  let loading = $state(true)
  let busy = $state(false)
  let error = $state('')
  let notice = $state('')
  let tab = $state('harnesses')
  let selectedId = $state('claude-code')

  let versionInput = $state('')
  let installing = $state('') // the harness this screen is installing now
  let chooseVersion = $state(false) // the exact-version field is open
  let removingTool = $state<{ kind: string; name: string } | null>(null) // {kind, name} waiting for an inline confirm
  let removing = $state('') // 'copy' while uninstall waits for an inline confirm
  let preview = $state<PreferencePreview | null>(null)
  let confirm = $state<Confirmation | null>(null)
  let addToolHint = $state('')
  let detailPanel: HTMLElement | undefined

  let stopped = false
  let pollTimer: ReturnType<typeof setTimeout> | undefined

  const rows = $derived(HARNESS_ORDER.map((h) => {
    const row = harnessRowState(h.id, snapshot)
    return installing === row.id ? { ...row, installing: true } : row
  }))
  const selectedRow = $derived(rows.find((r) => r.id === selectedId) ?? rows[0])
  const anySwitchPending = $derived(rows.some((r) => r.switchPending || r.installing))
  const mcpRows = $derived(catalog?.mcp ?? [])
  const allowedPreviewCount = $derived((preview?.keys ?? []).filter((k) => k.status === 'allowed').length)

  $effect(() => {
    onmodal(!!confirm)
  })

  $effect(() => {
    clearTimeout(pollTimer)
    if (!stopped && anySwitchPending) {
      pollTimer = setTimeout(pollPending, 1500)
    }
  })

  async function pollPending() {
    if (stopped) return
    try {
      const next = await api.privateRuntimes()
      if (!stopped) snapshot = next
    } catch {}
    if (!stopped && rows.some((r) => r.switchPending || r.installing)) {
      pollTimer = setTimeout(pollPending, 1500)
    }
  }

  async function load(fresh = false) {
    if (stopped) return
    loading = true
    error = ''
    try {
      const [runtimes, cfg] = await Promise.all([fresh ? api.refreshPrivateRuntimes() : api.privateRuntimes(), api.harnessConfig()])
      if (stopped) return
      snapshot = runtimes
      catalog = cfg
    } catch (e) {
      if (!stopped) error = failText(e, 'Could not load harnesses.')
    } finally {
      if (!stopped) loading = false
    }
  }

  onMount(load)
  onDestroy(() => {
    stopped = true
    clearTimeout(pollTimer)
    onmodal(false)
  })

  async function selectHarness(id: string) {
    if (id !== selectedId) {
      selectedId = id
      preview = null
      removing = ''
      versionInput = ''
      chooseVersion = false
    }
    await tick()
    // Narrow screens open the panel under the row: keep that row in view.
    const split = detailPanel?.parentElement
    if (split && split.clientWidth <= 860) split.querySelector('.row.sel')?.scrollIntoView({ block: 'start', behavior: motion(BASE) ? 'smooth' : 'auto' })
  }

  // install puts Dyna's copy in place: the recommended version, or the exact
  // version the user typed when Dyna knows none. Update uses it too.
  function install(row: HarnessRow, pin = '') {
    // A typed version belongs to the selected harness only.
    const typed = row.id === selectedId && (chooseVersion || !recommendedVersion(row)) ? versionInput.trim() : ''
    selectHarness(row.id)
    pin = (pin || typed || recommendedVersion(row)).trim()
    if (!exactVersion(pin)) { chooseVersion = true; error = 'Enter a version number, for example 1.0.0.'; return }
    return installVersion(row, pin)
  }

  async function installVersion(row: HarnessRow, pin: string) {
    if (busy || stopped) return
    busy = true
    installing = row.id
    error = ''
    notice = ''
    try {
      const next = await api.installPrivateRuntime(row.id, pin)
      if (!stopped) { snapshot = next; chooseVersion = false }
    } catch {
      if (!stopped) error = 'Could not install that version. Check the version string and try again.'
    } finally {
      if (!stopped) { busy = false; installing = '' }
    }
  }

  async function uninstall(row: HarnessRow) {
    if (busy || stopped) return
    busy = true
    error = ''
    notice = ''
    try {
      const next = await api.uninstallPrivateRuntime(row.id)
      if (!stopped) {
        snapshot = next
        removing = ''
      }
    } catch (e) {
      if (!stopped) { removing = ''; error = failText(e, `Could not uninstall ${row.meta.name}.`) }
    } finally {
      if (!stopped) busy = false
    }
  }

  async function loadPreview(account: HarnessAccount | null) {
    if (busy || stopped || !account?.id) return
    busy = true
    error = ''
    notice = ''
    try {
      const next = await api.previewPrivateConfig(account.id)
      if (!stopped) preview = next
    } catch {
      if (!stopped) error = 'Could not preview settings for this account.'
    } finally {
      if (!stopped) busy = false
    }
  }

  async function confirmImport(account: HarnessAccount | null) {
    if (busy || stopped || !account?.id || !preview?.hash || preview.accountId !== account.id || preview.harness !== account.harness) return
    const hash = preview.hash
    busy = true
    error = ''
    notice = ''
    try {
      const next = await api.importPrivateConfig(account.id, hash)
      if (!stopped) {
        snapshot = next
        preview = null
        notice = 'Safe preferences imported into Dyna.'
      }
    } catch {
      if (!stopped) error = 'Could not import preferences. Preview again if your settings file changed.'
    } finally {
      if (!stopped) busy = false
    }
  }

  async function toggleTool(kind: string, name: string, harnessId: string, enabled: boolean) {
    if (busy || stopped) return
    busy = true
    error = ''
    notice = ''
    try {
      const next = await api.setHarnessConfigEnabled(kind, name, harnessId, enabled)
      if (!stopped) {
        catalog = next
        notice = 'Tool selection saved for future turns.'
      }
    } catch {
      if (!stopped) error = 'Could not update tool selection. Try again.'
    } finally {
      if (!stopped) busy = false
    }
  }

  async function removeTool() {
    if (!removingTool) return
    const { kind, name } = removingTool
    busy = true; error = ''
    try {
      catalog = await api.removeHarnessConfig(kind, name)
      if (!stopped) { removingTool = null; notice = 'Tool removed.' }
    } catch (e) { if (!stopped) error = failText(e, 'Could not remove this tool.') }
    finally { if (!stopped) busy = false }
  }
</script>

<section class="harnesses" aria-busy={loading || busy}>
  <TabHeader flush title="Harnesses" blurb={tab === 'harnesses' ? 'Dyna installs and keeps its own copy of each agent. Pick one to manage it.' : 'Dyna’s own list of tools, and which agent can use each one.'}>
    {#snippet action()}<button class="btn quiet" onclick={() => load(true)} disabled={loading || busy}>{loading ? 'Refreshing…' : 'Refresh'}</button>{/snippet}
  </TabHeader>

  <HarnessTabs bind:tab />

  {#if tab === 'tools' || !snapshot}
    {#if error}<p class="error" role="alert">{error}</p>{/if}
    {#if notice}<p class="notice" role="status">{notice}</p>{/if}
  {/if}

  {#if loading && !snapshot}
    <Loading>Loading harnesses…</Loading>
  {:else if tab === 'harnesses'}
    <div id="harnesses-panel" role="tabpanel" aria-labelledby="harnesses-harnesses-tab" class="split">
      <HarnessList
        {rows}
        selected={selectedRow?.id ?? ''}
        {busy}
        onselect={selectHarness}
        oninstall={install}
      />

      {#if selectedRow}
        {@const r = selectedRow}
        <aside class="panel" bind:this={detailPanel} aria-label={r.meta.name + ' details'} style:order={rows.indexOf(r) * 2 + 1}>
          {#key r.id}
          <div in:fly={{ y: 6, duration: motion(BASE) }}>
          <div class="ph">
            <HarnessIcon id={r.meta.iconId} />
            <div>
              <div class="name panel-title">{r.meta.name}</div>
              <div class="ver">{r.installing ? 'Installing…' : r.version || 'Not installed'}</div>
            </div>
          </div>

          <InstallControls
            row={r}
            {busy}
            bind:chooseVersion
            bind:versionInput
            bind:removing
            oninstall={(pin) => install(r, pin)}
            onuninstall={() => uninstall(r)}
          />
          {#if error}<p class="error" role="alert">{error}</p>{/if}
          {#if notice}<p class="notice" role="status">{notice}</p>{/if}

          {#if !r.needsInstall && !r.installing}
          <SettingsImportSection
            row={r}
            preview={preview && preview.accountId === r.activeAccount?.id ? preview : null}
            allowedCount={allowedPreviewCount}
            {busy}
            onpreview={() => loadPreview(r.activeAccount)}
            onimport={() => confirmImport(r.activeAccount)}
          />

          <HarnessToolsSection
            row={r}
            mcp={mcpRows}
            {busy}
            ontoggle={(kind, name, enabled) => toggleTool(kind, name, r.id, enabled)}
          />
          {/if}
          </div>
          {/key}
        </aside>
      {/if}
    </div>
  {:else}
    <ToolsTab
      mcp={mcpRows}
      {rows}
      {busy}
      bind:addToolHint
      bind:removingTool
      onadded={(next) => { catalog = next; addToolHint = ''; notice = 'MCP server added. Turn it on for each harness that needs it.' }}
      ontoggle={toggleTool}
      onremovetool={removeTool}
    />
  {/if}
</section>

{#if confirm}
  {@const asked = confirm}
  <Confirm
    title={confirm.title}
    lines={confirm.lines}
    action={confirm.action}
    danger={confirm.danger}
    {busy}
    {error}
    alternative={asked.alternative}
    onalternative={() => asked.runAlternative?.()}
    onconfirm={() => asked.run()}
    oncancel={() => { if (!busy) confirm = null }}
  />
{/if}

<style>
  .harnesses { container-type: inline-size; }
  .ver { font-family: var(--mono); font-size: var(--t-cap); color: var(--muted); }
  .name { font-size: var(--t-body); font-weight: 500; }
  .panel-title { font-size: var(--t-head); }
  .split { display: grid; grid-template-columns: minmax(0, 1fr) 380px; gap: var(--s5); margin-top: 20px; align-items: start; }
  .panel { background: var(--side); border: 1px solid var(--line); border-radius: 10px; padding: 20px; }
  .ph { display: flex; align-items: center; gap: var(--s3); margin-bottom: var(--s4); }
  .panel :global(.sec:last-child) { padding-bottom: 0; }
  .error { color: var(--coral); font-size: var(--t-ui); margin: var(--s3) 0 0; }
  .notice { color: var(--muted); font-size: var(--t-sm); margin: var(--s3) 0 0; }
  .panel .error, .panel .notice { margin: 0 0 var(--s3); }
  /* Narrow: the detail panel opens under the selected row. Rows and panel
     share one column, ordered by their inline order values. */
  @container (max-width: 860px) {
    .split { display: flex; flex-direction: column; align-items: stretch; gap: 0; }
    .split :global(.list) { display: contents; }
    .panel { margin: var(--s2) 0 var(--s3); padding: var(--s4); }
    .ph { display: none; }
  }
</style>
