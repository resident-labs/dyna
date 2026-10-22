<script module lang="ts">
  import type { PrivateLogin } from '../../api/harness.ts'
  // A sign-in terminal that a harness account is waiting on.
  export type LoginFlow = PrivateLogin & { harness: string }
  // Keep a running terminal reachable when Settings closes or changes panes.
  let pendingLogin: LoginFlow | null = null
</script>

<script lang="ts">
  import { onMount, onDestroy, tick } from 'svelte'
  import { errorText } from '../../errorText.ts'
  import { fade, fly, slide } from 'svelte/transition'
  import { BASE, motion } from '../../ui/motion.js'
  import Loading from '../Loading.svelte'
  import { api } from '../../../api.js'
  import HarnessIcon from './HarnessIcon.svelte'
  import ToolEditor from './ToolEditor.svelte'
  import TermPane from '../../sessions/TermPane.svelte'
  import Confirm from '../../ui/Confirm.svelte'
  import TabHeader from '../../rooms/TabHeader.svelte'
  import Switch from '../../ui/Switch.svelte'
  import EmptyState from '../../ui/EmptyState.svelte'
  import InlineConfirm from '../../ui/InlineConfirm.svelte'
  import ProviderKeyDialog from './ProviderKeyDialog.svelte'
  import {
    HARNESS_ORDER,
    accountNeedsSignIn,
    exactVersion,
    harnessRowState,
    hasSubscriptionLogin,
    personalLoginChoice,
    preferenceStatusLabel,
    recommendedVersion,
    recoveredLogins,
    rowLine,
    takesProviderKeys,
    toolEnabledForHarness,
    toolSummary,
    type HarnessRow,
  } from './state.ts'
  import type { HarnessAccount, HarnessConfig, HarnessSnapshot, PersonalLogin, PreferencePreview, ProviderKey } from '../../api/harness.ts'

  // A question the user answers before a sign-in or switch goes ahead.
  interface Confirmation {
    kind: string
    title: string
    lines: string[]
    action: string
    danger: boolean
    alternative?: string
    run: () => void
    runAlternative?: () => void
  }

  let { onmodal = () => {} }: { onmodal?: (open: boolean) => void } = $props()

  // failText is the thrown value's message, or the fallback when it has none.
  const failText = (e: unknown, fallback: string): string => (e ? errorText(e) : fallback)

  let snapshot = $state<HarnessSnapshot | null>(null)
  let catalog = $state<HarnessConfig | null>(null)
  let loading = $state(true)
  let busy = $state(false)
  let error = $state('')
  let notice = $state('')
  let tab = $state('harnesses')
  let selectedId = $state(pendingLogin?.harness || 'claude-code')

  let versionInput = $state('')
  let labelInput = $state('')
  let showSignInForm = $state(false)
  let scanning = $state('')
  let installing = $state('') // the harness this screen is installing now
  let chooseVersion = $state(false) // the exact-version field is open
  let removingTool = $state<{ kind: string; name: string } | null>(null) // {kind, name} waiting for an inline confirm
  let scoping = $state('') // the account ID whose repo list is open for editing
  let scopeText = $state('') // one repo per line
  let removing = $state('') // the account ID, or 'copy', waiting for an inline confirm
  let login = $state<LoginFlow | null>(pendingLogin)
  let loginFlows = $state<LoginFlow[]>(pendingLogin ? [pendingLogin] : [])
  let preview = $state<PreferencePreview | null>(null)
  let confirm = $state<Confirmation | null>(null)
  let providers = $state<Record<string, ProviderKey[]>>({}) // harness ID -> providers that hold an API key (names only)
  let addingProvider = $state(false)
  let removingProvider = $state('')
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
  const skillRows = $derived(catalog?.skills ?? [])
  const allowedPreviewCount = $derived((preview?.keys ?? []).filter((k) => k.status === 'allowed').length)

  $effect(() => {
    onmodal(!!confirm || addingProvider)
  })

  $effect(() => {
    clearTimeout(pollTimer)
    if (!stopped && anySwitchPending) {
      pollTimer = setTimeout(pollPending, 1500)
    }
  })

  $effect(() => {
    const r = selectedRow
    if (r && takesProviderKeys(r.id) && !r.needsInstall && !r.installing && !providers[r.id]) loadProviders(r.id)
  })

  async function loadProviders(id: string) {
    try {
      providers[id] = (await api.harnessProviderKeys(id, 'list')).providers ?? []
    } catch (e) {
      if (!stopped) error = failText(e, 'Could not read the providers.')
    }
  }

  async function removeProvider(id: string, provider: string) {
    busy = true
    error = ''
    try {
      providers[id] = (await api.harnessProviderKeys(id, 'remove', provider)).providers ?? []
      removingProvider = ''
    } catch (e) {
      error = failText(e, 'Could not remove the provider.')
    } finally {
      busy = false
    }
  }

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
      // Selected versions and login recovery APIs ship in the same backend update.
      if (runtimes.selectedVersions) {
        const flows = await api.privateRuntimeLogins()
        if (stopped) return
        loginFlows = recoveredLogins(flows, runtimes.accounts)
        login = loginFlows.find((flow) => flow.accountId === pendingLogin?.accountId) ?? loginFlows[0] ?? null
        pendingLogin = login
      }
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

  async function selectHarness(id: string, openSignIn = false) {
    if (id !== selectedId) {
      selectedId = id
      preview = null
      removing = ''
      versionInput = ''
      chooseVersion = false
    }
    showSignInForm = openSignIn
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

  // Every Sign in button starts here. A saved account signs in again; a new
  // account first offers the user's personal login when Dyna finds one.
  function signIn(row: HarnessRow, add = false) {
    return !add && row?.activeAccount ? requestRestartLogin(row, row.activeAccount) : findPersonalLogin(row)
  }

  function signInVersion(row: HarnessRow): string {
    // A missing private copy is installed on first use before sign-in starts.
    const pin = (row?.version || recommendedVersion(row) || versionInput || '').trim()
    if (exactVersion(pin)) return pin
    showSignInForm = true
    error = 'Enter an exact version (for example 1.0.0) before signing in.'
    return ''
  }

  async function findPersonalLogin(row: HarnessRow) {
    if (busy || stopped || login || row?.capability?.accountBlocked) return
    const pin = signInVersion(row)
    if (!pin) return
    busy = true
    scanning = row.id
    error = ''
    notice = `Looking for your ${row.meta.name} login…`
    let found: PersonalLogin | null
    try {
      found = await api.findPersonalLogin(row.id, pin)
    } catch (e) {
      const message = e == null ? '' : errorText(e)
      if (!stopped) error = message.startsWith('cannot install') ? message : 'Could not start sign-in. Check the runtime version and try again.'
      return
    } finally {
      if (!stopped) { busy = false; scanning = ''; notice = '' }
    }
    if (stopped) return
    if (!found) return requestBeginLogin(row)
    confirm = {
      kind: 'personal-login',
      ...personalLoginChoice(row.meta.name, found),
      danger: false,
      run: found.saved ? () => requestBeginLogin(row) : () => usePersonalLogin(row.id),
      runAlternative: () => requestBeginLogin(row),
    }
  }

  async function usePersonalLogin(harness: string) {
    if (busy || stopped) return
    busy = true
    error = ''
    notice = ''
    try {
      const next = await api.usePersonalLogin(harness)
      if (!stopped) {
        snapshot = next
        confirm = null
        notice = 'Dyna copied your personal login. You are signed in.'
      }
    } catch (e) {
      if (!stopped) error = failText(e, 'Could not copy your personal login.')
    } finally {
      if (!stopped) busy = false
    }
  }

  function requestBeginLogin(row: HarnessRow, confirmed = false) {
    if (busy || stopped || login || row?.capability?.accountBlocked) return
    const pin = signInVersion(row)
    if (!pin) return
    if (row.sharedPersonalLogin && !confirmed) {
      confirm = {
        kind: 'begin-login',
        title: `Sign in to ${row.meta.name}?`,
        lines: [`Signing in to Dyna’s ${row.meta.name} account also changes the login of your own ${row.meta.name} CLI.`],
        action: 'Continue sign-in',
        danger: false,
        run: () => performBeginLogin(row.id, pin, labelInput.trim(), true),
      }
      return
    }
    return performBeginLogin(row.id, pin, labelInput.trim(), confirmed)
  }

  async function performBeginLogin(harness: string, pin: string, label: string, confirmed: boolean) {
    if (busy || stopped || login) return
    busy = true
    error = ''
    notice = ''
    try {
      const result = await api.beginPrivateRuntimeLogin(harness, pin, label, confirmed)
      const flow: LoginFlow = { ...result, harness }
      pendingLogin = flow
      if (!stopped) {
        login = flow
        loginFlows = [...loginFlows.filter((other) => other.accountId !== flow.accountId), flow]
        confirm = null
        showSignInForm = false
        notice = 'Complete sign-in in the terminal below, then click Finish sign-in.'
      }
    } catch (e) {
      const message = e == null ? '' : errorText(e)
      if (!stopped) error = message.startsWith('cannot install') ? message : 'Could not start sign-in. Check the runtime version and try again.'
    } finally {
      if (!stopped) busy = false
    }
  }

  function requestRestartLogin(row: HarnessRow, account: HarnessAccount | null, confirmed = false) {
    if (busy || stopped || login || !account?.id || row?.capability?.accountBlocked) return
    if (row.sharedPersonalLogin && !confirmed) {
      confirm = {
        kind: 'restart-login',
        title: `Sign in to ${account.label || row.meta.name} again?`,
        lines: [`Signing in also changes the login of your own ${row.meta.name} CLI.`],
        action: 'Continue sign-in',
        danger: false,
        run: () => performRestartLogin(row.id, account.id, true),
      }
      return
    }
    return performRestartLogin(row.id, account.id, confirmed)
  }

  async function performRestartLogin(harness: string, accountId: string, confirmed: boolean) {
    if (busy || stopped || login) return
    busy = true
    error = ''
    notice = ''
    try {
      const result = await api.restartPrivateRuntimeLogin(accountId, confirmed)
      const flow: LoginFlow = { ...result, harness }
      pendingLogin = flow
      if (!stopped) {
        login = flow
        loginFlows = [...loginFlows.filter((other) => other.accountId !== flow.accountId), flow]
        confirm = null
        notice = 'Complete sign-in in the terminal below, then click Finish sign-in.'
      }
    } catch {
      if (!stopped) error = 'Could not restart sign-in. Try again.'
    } finally {
      if (!stopped) busy = false
    }
  }

  async function finishLogin() {
    if (busy || stopped || !login?.accountId) return
    busy = true
    error = ''
    notice = ''
    const accountId = login.accountId
    try {
      const next = await api.finishPrivateRuntimeLogin(accountId)
      pendingLogin = null
      if (!stopped) {
        snapshot = next
        loginFlows = loginFlows.filter((flow) => flow.accountId !== accountId)
        login = loginFlows[0] ?? null
        pendingLogin = login
        labelInput = ''
        notice = 'Sign-in finished.'
      }
    } catch (e) {
      // The engine names the missing step, so keep its words.
      if (!stopped) error = failText(e, 'Sign-in is not complete yet. Finish signing in inside the terminal first.')
    } finally {
      if (!stopped) busy = false
    }
  }

  function openLogin(flow: LoginFlow) {
    pendingLogin = flow
    login = flow
    tab = 'harnesses'
    selectHarness(flow.harness)
  }

  async function cancelLogin() {
    if (busy || stopped || !login?.accountId) return
    const accountId = login.accountId
    busy = true; error = ''
    try {
      await api.cancelPrivateRuntimeLogin(accountId)
      pendingLogin = null
      if (!stopped) {
        loginFlows = loginFlows.filter((flow) => flow.accountId !== accountId)
        login = loginFlows[0] ?? null
        pendingLogin = login
        notice = 'Sign-in cancelled. The saved account remains.'
        await load()
      }
    } catch (e) {
      if (!stopped) error = failText(e, 'Could not cancel sign-in.')
    } finally { if (!stopped) busy = false }
  }

  function requestSwitchAccount(row: HarnessRow, account: HarnessAccount, confirmed = false) {
    if (busy || stopped || login || !account?.id || account.active) return
    if (row.sharedPersonalLogin && !confirmed) {
      confirm = {
        kind: 'switch-account',
        title: `Switch ${row.meta.name} to ${account.label || account.id}?`,
        lines: [`Switching Dyna’s ${row.meta.name} account also changes the login of your own ${row.meta.name} CLI.`],
        action: 'Switch account',
        danger: false,
        run: () => performSwitchAccount(account.id, true),
      }
      return
    }
    performSwitchAccount(account.id, confirmed)
  }

  async function performSwitchAccount(accountId: string, confirmed: boolean) {
    if (busy || stopped || login) return
    busy = true
    error = ''
    notice = ''
    const poll = setInterval(async () => {
      try {
        const next = await api.privateRuntimes()
        if (!stopped && busy) snapshot = next
      } catch {}
    }, 1000)
    try {
      const next = await api.switchPrivateRuntimeAccount(accountId, confirmed)
      if (!stopped) {
        snapshot = next
        confirm = null
        preview = null
        notice = 'Active account switched.'
      }
    } catch {
      if (!stopped) error = 'Could not switch account. Try again.'
    } finally {
      clearInterval(poll)
      if (!stopped) busy = false
    }
  }

  function scopeLabel(acct: HarnessAccount) {
    const n = acct.repos?.length ?? 0
    return n ? `${n} ${n === 1 ? 'repo' : 'repos'} only` : 'All repos'
  }

  function openScope(acct: HarnessAccount) {
    scoping = acct.id
    scopeText = (acct.repos ?? []).join('\n')
  }

  async function saveScope(accountId: string) {
    if (busy || stopped) return
    busy = true
    error = ''
    notice = ''
    try {
      const repos = scopeText.split('\n').map((line) => line.trim()).filter(Boolean)
      const next = await api.setAccountRepos(accountId, repos)
      if (!stopped) {
        snapshot = next
        scoping = ''
        notice = repos.length ? 'Account limited to the listed repos.' : 'Account may use any repo that no other account names.'
      }
    } catch (e) {
      if (!stopped) error = String(Reflect.get(Object(e), 'message') || 'Could not save the repo list. Use a remote URL or host/org/name.')
    } finally {
      if (!stopped) busy = false
    }
  }

  async function removeAccount(accountId: string) {
    if (busy || stopped) return
    busy = true
    error = ''
    notice = ''
    try {
      const next = await api.removePrivateRuntimeAccount(accountId, true)
      if (!stopped) {
        snapshot = next
        removing = ''
        if (preview?.accountId === accountId) preview = null
        notice = 'Account removed.'
      }
    } catch {
      if (!stopped) error = 'Could not remove account. Close any active session using it first.'
    } finally {
      if (!stopped) busy = false
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

  function tabKey(e: KeyboardEvent) {
    if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(e.key)) return
    e.preventDefault()
    tab = e.key === 'Home' ? 'harnesses' : e.key === 'End' ? 'tools' : tab === 'harnesses' ? 'tools' : 'harnesses'
    document.getElementById('harnesses-' + tab + '-tab')?.focus()
  }
</script>

<section class="harnesses" aria-busy={loading || busy}>
  <TabHeader flush title="Harnesses" blurb={tab === 'harnesses' ? 'Dyna installs and keeps its own copy of each agent. Pick one to manage it.' : 'Dyna’s own list of tools, and which agent can use each one.'}>
    {#snippet action()}<button class="btn quiet" onclick={() => load(true)} disabled={loading || busy}>{loading ? 'Refreshing…' : 'Refresh'}</button>{/snippet}
  </TabHeader>

  <div class="tabs" role="tablist" tabindex="-1" aria-label="Harness views" onkeydown={tabKey}>
    <button
      id="harnesses-harnesses-tab"
      role="tab"
      class:on={tab === 'harnesses'}
      aria-selected={tab === 'harnesses'}
      aria-controls="harnesses-panel"
      tabindex={tab === 'harnesses' ? 0 : -1}
      onclick={() => (tab = 'harnesses')}
    >
      Harnesses
    </button>
    <button
      id="harnesses-tools-tab"
      role="tab"
      class:on={tab === 'tools'}
      aria-selected={tab === 'tools'}
      aria-controls="tools-panel"
      tabindex={tab === 'tools' ? 0 : -1}
      onclick={() => (tab = 'tools')}
    >
      MCP servers &amp; skills
    </button>
  </div>

  {#if tab === 'tools' || !snapshot}
    {#if error}<p class="error" role="alert">{error}</p>{/if}
    {#if notice}<p class="notice" role="status">{notice}</p>{/if}
  {/if}
  {#if loginFlows.length}
    <div class="btn-group" style="margin-top:var(--s3)" aria-label="Pending sign-ins">
      {#each loginFlows as flow (flow.accountId)}
        <button class="btn" disabled={busy} onclick={() => openLogin(flow)}>Continue {HARNESS_ORDER.find((h) => h.id === flow.harness)?.name || flow.harness} sign-in</button>
      {/each}
    </div>
  {/if}

  {#if loading && !snapshot}
    <Loading>Loading harnesses…</Loading>
  {:else if tab === 'harnesses'}
    <div id="harnesses-panel" role="tabpanel" aria-labelledby="harnesses-harnesses-tab" class="split">
      <div class="list" aria-label="Harnesses">
        {#each rows as r, i (r.id)}
          <div class="row" class:sel={r.id === selectedRow?.id} style:order={i * 2}>
            <button class="pick" aria-pressed={r.id === selectedRow?.id} onclick={() => selectHarness(r.id)}>
              <HarnessIcon id={r.meta.iconId} />
              <span class="who">
                <span class="name">{r.meta.name}</span>
                <span class="ver">{rowLine(r)}</span>
              </span>
            </button>
            {#if r.installing}
              <span class="progress" role="progressbar" aria-label={`Installing ${r.meta.name}`} in:fade={{ duration: motion(BASE) }}></span>
            {:else if r.needsInstall}
              <button class="btn pri" disabled={busy || !!login || !r.capability?.installSupported} onclick={() => install(r)}>Install</button>
            {:else if r.needsSignIn && !r.capability?.accountBlocked}
              <button class="btn pri" disabled={busy || !!login} onclick={() => { selectHarness(r.id); signIn(r) }}>Sign in</button>
            {:else if r.update}
              <button class="btn" disabled={busy || !!login} title={`Update to ${r.update}`} onclick={() => install(r, r.update)}><span class="up" aria-hidden="true">↑</span>Update</button>
            {/if}
          </div>
        {/each}
      </div>

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

          {#if r.installing}
            <div class="installing toolbar" role="status" in:fade={{ duration: motion(BASE) }}>
              <span class="muted small">Installing Dyna’s copy. This can take a minute.</span>
              <span class="progress wide" aria-hidden="true"></span>
            </div>
          {:else if r.needsInstall}
            <div class="toolbar install">
              {#if chooseVersion || !recommendedVersion(r)}
                <label class="ver-field" transition:slide={{ duration: motion(BASE) }}>
                  <span class="muted small">{recommendedVersion(r) ? 'Version to install' : 'Dyna can’t look up the latest version right now. Enter the version number to install.'}</span>
                  <input bind:value={versionInput} aria-label={r.meta.name + ' version'} placeholder={recommendedVersion(r) || 'e.g. 1.0.0'} disabled={busy} />
                </label>
              {/if}
              <span class="btn-group">
                <button class="btn pri" disabled={busy || !!login || !r.capability?.installSupported} onclick={() => install(r)}>Install</button>
                {#if recommendedVersion(r) && !chooseVersion}<button class="link" disabled={busy} onclick={() => (chooseVersion = true)}>Choose version</button>{/if}
              </span>
            </div>
          {:else if r.update || r.version}
            <div class="btn-group toolbar">
              {#if removing === 'copy'}
                <InlineConfirm ask="Uninstall Dyna’s copy?" action="Uninstall" {busy} onconfirm={() => uninstall(r)} oncancel={() => (removing = '')} />
              {:else}
                {#if r.update}<button class="btn" disabled={busy || !!login} onclick={() => install(r, r.update)}><span class="up" aria-hidden="true">↑</span>Update to {r.update}</button>{/if}
                {#if r.version}<button class="btn quiet" disabled={busy || !!login} onclick={() => (removing = 'copy')}>Uninstall</button>{/if}
              {/if}
            </div>
          {/if}
          {#if error}<p class="error" role="alert">{error}</p>{/if}
          {#if notice}<p class="notice" role="status">{notice}</p>{/if}

          {#if hasSubscriptionLogin(r.id) || (login && login.harness === r.id)}
            <div class="sec account">
              <h3 class="section-label">Accounts</h3>
              {#if login && login.harness === r.id}
                <div class="login-box">
                  {#if login.warning}<p class="muted small" style="margin:0 0 var(--s2)">{login.warning}</p>{/if}
                  <p class="muted small" style="margin:0 0 var(--s2)">Complete sign-in in the terminal below, then click Finish sign-in.</p>
                  <div class="terminal">{#key login.terminalId}<TermPane id={login.terminalId} narrow />{/key}</div>
                  <div class="btn-group" style="margin-top:10px">
                    <button class="btn" disabled={busy} onclick={finishLogin}>Finish sign-in</button>
                    {#if snapshot?.selectedVersions}<button class="btn quiet" disabled={busy} onclick={cancelLogin}>Cancel sign-in</button>{/if}
                  </div>
                </div>
              {:else if r.needsInstall || r.installing}
                <p class="muted small" style="margin:0">Install Dyna’s copy to sign in.{r.accounts.length ? ' Saved accounts stay.' : ''}</p>
              {:else if r.capability?.accountBlocked}
                <p class="muted small" style="margin:0">{r.capability.accountBlocked}</p>
              {:else if !r.accounts.length}
                <div class="signin-card">
                  <p>Sign in to start using {r.meta.name}.</p>
                  <p class="muted small">If you already use {r.meta.name}, Dyna can copy that login once.</p>
                  <button class="btn pri" disabled={busy || !!login} onclick={() => signIn(r)}>{scanning === r.id ? 'Looking for your login…' : 'Sign in'}</button>
                </div>
              {:else}
                {#each r.accounts as acct (acct.id)}
                  <div class="acct" transition:slide={{ duration: motion(BASE) }}>
                    <span class="acct-name">
                      <span class="dot" class:on={acct.active} aria-hidden="true"></span>
                      <span class="label">{acct.label || r.meta.name}</span>
                      {#if accountNeedsSignIn(acct)}<span class="muted small">Signed out</span>{:else if acct.active}<span class="in-use small">In use</span>{/if}
                      <span class="scope small" class:limited={acct.repos?.length}>{scopeLabel(acct)}</span>
                    </span>
                    <span class="btn-group">
                      {#if removing === acct.id}
                        <InlineConfirm {busy} onconfirm={() => removeAccount(acct.id)} oncancel={() => (removing = '')} />
                      {:else}
                        {#if accountNeedsSignIn(acct)}
                          <button class="btn pri" disabled={busy || !!login} onclick={() => requestRestartLogin(r, acct)}>Sign in</button>
                        {:else if !acct.active}
                          <button class="btn quiet" disabled={busy || !!login} onclick={() => requestSwitchAccount(r, acct)}>Use</button>
                        {/if}
                        <button class="btn quiet" disabled={busy || !!login} onclick={() => (scoping === acct.id ? (scoping = '') : openScope(acct))}>Repos</button>
                        <button class="btn quiet" disabled={busy || !!login} onclick={() => (removing = acct.id)}>Remove</button>
                      {/if}
                    </span>
                  </div>
                  {#if scoping === acct.id}
                    <form class="signin-form" transition:slide={{ duration: motion(BASE) }} onsubmit={(e) => { e.preventDefault(); saveScope(acct.id) }}>
                      <label>
                        <span class="muted small">Only use in these repos (one per line, empty for any)</span>
                        <textarea bind:value={scopeText} rows="3" placeholder="github.com/org/name" disabled={busy}></textarea>
                      </label>
                      <p class="muted small" style="margin:0">A repo named here is closed to accounts without a list.</p>
                      <div class="btn-group">
                        <button type="submit" class="btn" disabled={busy}>Save</button>
                        <button type="button" class="btn quiet" onclick={() => (scoping = '')}>Cancel</button>
                      </div>
                    </form>
                  {/if}
                {/each}
                {#if showSignInForm}
                  <form class="signin-form" transition:slide={{ duration: motion(BASE) }} onsubmit={(e) => { e.preventDefault(); signIn(r, true) }}>
                    <label>
                      <span class="muted small">Account label (optional)</span>
                      <input bind:value={labelInput} placeholder="e.g. Work" disabled={busy || !!login} />
                    </label>
                    <div class="btn-group">
                      <button type="submit" class="btn" disabled={busy || !!login}>Start sign-in</button>
                      <button type="button" class="btn quiet" onclick={() => (showSignInForm = false)}>Cancel</button>
                    </div>
                  </form>
                {:else}
                  <button class="btn quiet add" disabled={busy || !!login} onclick={() => (showSignInForm = true)}><span aria-hidden="true">+</span>Add account</button>
                {/if}
              {/if}
            </div>

          {/if}

          {#if takesProviderKeys(r.id) && !r.needsInstall && !r.installing}
            <div class="sec">
              <h3 class="section-label">API-key providers</h3>
              {#if !hasSubscriptionLogin(r.id)}<p class="muted small" style="margin:0 0 var(--s2)">{r.meta.name} has no subscription login. Add a provider with an API key.</p>{/if}
              {#each providers[r.id] ?? [] as p (p.id)}
                <div class="acct" transition:slide={{ duration: motion(BASE) }}>
                  <span class="acct-name"><span class="dot on" aria-hidden="true"></span><span class="label">{p.name}</span></span>
                  <span class="btn-group">
                    {#if removingProvider === p.id}
                      <InlineConfirm {busy} onconfirm={() => removeProvider(r.id, p.id)} oncancel={() => (removingProvider = '')} />
                    {:else}
                      <button class="btn quiet" disabled={busy} onclick={() => (removingProvider = p.id)}>Remove</button>
                    {/if}
                  </span>
                </div>
              {/each}
              <button class="btn quiet add" disabled={busy} onclick={() => (addingProvider = true)}><span aria-hidden="true">+</span>Add provider</button>
            </div>
          {/if}

          {#if !r.needsInstall && !r.installing}
          <div class="sec">
            <h3 class="section-label">Settings import</h3>
            <p class="muted small" style="margin:0 0 var(--s2)">
              Copy safe preferences from your own {r.meta.name} setup. Nothing is copied until you confirm.
            </p>
            {#if !r.activeAccount}
              <p class="muted small" style="margin:0">Sign in to preview and import personal preferences.</p>
            {:else}
              {#if preview && preview.accountId === r.activeAccount.id}
                {#if ['grok', 'pi'].includes(r.id)}<p class="muted small">Preview only. This harness cannot copy preferences yet.</p>{/if}
                <div class="preview-box">
                  {#each preview.keys ?? [] as item (item.key)}
                    <div class="kv">
                      <span><code>{item.key}</code></span>
                      <span class="tag {item.status === 'allowed' ? 'ok' : 'no'}">{preferenceStatusLabel(item.status)}</span>
                    </div>
                  {:else}
                    <p class="muted small" style="margin:6px 0">No personal preference keys found.</p>
                  {/each}
                </div>
                <div class="btn-group" style="margin-top:10px">
                  <button
                    class="btn"
                    disabled={busy || allowedPreviewCount === 0 || ['grok', 'pi'].includes(r.id)}
                    onclick={() => confirmImport(r.activeAccount)}
                  >
                    Confirm import ({allowedPreviewCount})
                  </button>
                  <button class="btn quiet" disabled={busy} onclick={() => loadPreview(r.activeAccount)}>Refresh preview</button>
                </div>
              {:else}
                <button class="btn quiet" disabled={busy} onclick={() => loadPreview(r.activeAccount)}>Preview settings import</button>
              {/if}
            {/if}
          </div>

          <div class="sec">
            <h3 class="section-label">Tools in {r.meta.name}</h3>
            {#each mcpRows as entry (entry.name)}
              {@const on = toolEnabledForHarness(entry, r.id)}
              <div class="kv">
                <span>{entry.name} <span class="muted small">MCP</span></span>
                <Switch {on} label={`${entry.name} MCP in ${r.meta.name}`} disabled={busy || !r.capability?.mcp} onchange={(v: boolean) => toggleTool('mcp', entry.name, r.id, v)} />
              </div>
            {/each}
            {#each skillRows as entry (entry.name)}
              {@const on = toolEnabledForHarness(entry, r.id)}
              <div class="kv">
                <span>{entry.name} <span class="muted small">Skill</span></span>
                <Switch {on} label={`${entry.name} skill in ${r.meta.name}`} disabled={busy || !r.capability?.skills} onchange={(v: boolean) => toggleTool('skill', entry.name, r.id, v)} />
              </div>
            {/each}
            {#if !mcpRows.length && !skillRows.length}
              <EmptyState title="No tools yet">Add one in the MCP servers & skills tab.</EmptyState>
            {/if}
          </div>
          {/if}
          </div>
          {/key}
        </aside>
      {/if}
    </div>
  {:else}
    <div id="tools-panel" role="tabpanel" aria-labelledby="harnesses-tools-tab">
      <div class="tools-bar">
        <p class="muted small" style="margin:0;max-width:520px">
          Choose which harness can use each tool in Dyna’s catalog.
        </p>
        <span class="btn-group">
          <button class="btn" onclick={() => (addToolHint = addToolHint === 'mcp' ? '' : 'mcp')}>Add MCP server</button>
          <button class="btn" onclick={() => (addToolHint = addToolHint === 'skill' ? '' : 'skill')}>Add skill</button>
        </span>
      </div>

      {#if addToolHint}
        {#key addToolHint}
          <ToolEditor kind={addToolHint} oncancel={() => addToolHint = ''} onsave={(next) => { catalog = next; addToolHint = ''; notice = 'Tool added. Turn it on for each harness that needs it.' }} />
        {/key}
      {/if}

      <div class="thead">
        <span></span>
        {#each HARNESS_ORDER as h (h.id)}
          <span class="hlabel" title={h.name}><HarnessIcon id={h.iconId} /><span>{h.short}</span></span>
        {/each}
      </div>

      {#each mcpRows as entry (entry.name)}
        <div class="trow">
          <div>
            <div class="name">{entry.name} <span class="tag">MCP</span> {#if removingTool?.kind === 'mcp' && removingTool.name === entry.name}<InlineConfirm {busy} onconfirm={removeTool} oncancel={() => (removingTool = null)} />{:else}<button class="remove-tool" aria-label={`Remove ${entry.name} MCP server`} disabled={busy} onclick={() => (removingTool = { kind: 'mcp', name: entry.name })}>Remove</button>{/if}</div>
            <div class="muted small">{toolSummary('mcp', entry)}</div>
          </div>
          {#each HARNESS_ORDER as h (h.id)}
            {@const on = toolEnabledForHarness(entry, h.id)}
            <Switch {on} label={`${entry.name} enabled for ${h.name}`} disabled={busy || !rows.find((r) => r.id === h.id)?.capability?.mcp} onchange={(v: boolean) => toggleTool('mcp', entry.name, h.id, v)} />
          {/each}
        </div>
      {/each}

      {#each skillRows as entry (entry.name)}
        <div class="trow">
          <div>
            <div class="name">{entry.name} <span class="tag">Skill</span> {#if removingTool?.kind === 'skill' && removingTool.name === entry.name}<InlineConfirm {busy} onconfirm={removeTool} oncancel={() => (removingTool = null)} />{:else}<button class="remove-tool" aria-label={`Remove ${entry.name} skill`} disabled={busy} onclick={() => (removingTool = { kind: 'skill', name: entry.name })}>Remove</button>{/if}</div>
            <div class="muted small">{toolSummary('skill', entry)}</div>
          </div>
          {#each HARNESS_ORDER as h (h.id)}
            {@const on = toolEnabledForHarness(entry, h.id)}
            <Switch {on} label={`${entry.name} enabled for ${h.name}`} disabled={busy || !rows.find((r) => r.id === h.id)?.capability?.skills} onchange={(v: boolean) => toggleTool('skill', entry.name, h.id, v)} />
          {/each}
        </div>
      {/each}

      {#if !mcpRows.length && !skillRows.length}
        <EmptyState title="No tools yet">Add an MCP server or skill above, and pick which harnesses can use it.</EmptyState>
      {/if}
    </div>
  {/if}
</section>

{#if addingProvider && selectedRow}
  <ProviderKeyDialog harness={selectedRow.id} name={selectedRow.meta.name} onclose={() => (addingProvider = false)} onsaved={(list) => { providers[selectedRow.id] = list; addingProvider = false; load() }} />
{/if}

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
  .up { margin-right: 6px; }
  .btn-group { display: flex; flex-wrap: wrap; align-items: center; gap: var(--s2); }
  .ver { font-family: var(--mono); font-size: var(--t-cap); color: var(--muted); }
  .muted { color: var(--muted); }
  .small { font-size: var(--t-sm); }
  .name { font-size: var(--t-body); font-weight: 500; }
  .panel-title { font-size: var(--t-head); }
  .tabs { display: flex; gap: var(--s1); border-bottom: 1px solid var(--line); margin: var(--s5) 0 0; }
  .tabs button { padding: var(--s2) 14px; font-size: var(--t-ui); color: var(--muted); border-bottom: 2px solid transparent; margin-bottom: -1px; }
  .tabs button.on { color: var(--ink); border-color: var(--iris); }
  .split { display: grid; grid-template-columns: minmax(0, 1fr) 380px; gap: var(--s5); margin-top: 20px; align-items: start; }
  .row { display: grid; grid-template-columns: minmax(0, 1fr) auto; gap: var(--s3); align-items: center; padding: 11px var(--s3); border-bottom: 1px solid var(--line-soft); border-radius: 8px; cursor: pointer; transition: background-color var(--motion-fast) var(--ease); }
  .pick { display: flex; align-items: center; gap: var(--s3); min-width: 0; text-align: left; }
  .who { display: grid; gap: 3px; min-width: 0; }
  .row.sel { background: var(--select); }
  @media (hover: hover) {
    .row:hover { background: var(--hover); }
  }
  .row:active { background: var(--select); }
  .panel { background: var(--side); border: 1px solid var(--line); border-radius: 10px; padding: 20px; }
  .ph { display: flex; align-items: center; gap: var(--s3); margin-bottom: var(--s4); }
  .panel .sec:last-child { padding-bottom: 0; }
  .sec { border-top: 1px solid var(--line); padding: var(--s4) 0; }
  .sec .section-label { margin-top: 0; }
  .kv { flex-wrap: wrap; display: flex; align-items: center; justify-content: space-between; gap: var(--s3); padding: 6px 0; font-size: var(--t-ui); }
  .remove-tool { color: var(--muted); font-size: var(--t-cap); margin-left: var(--s2); text-decoration: underline; }
  .tag { font-size: var(--t-cap); color: var(--muted); border: 1px solid var(--line-soft); border-radius: 4px; padding: 1px 6px; }
  .tag.ok { color: var(--sage); }
  .tag.no { color: var(--faint); text-decoration: line-through; }
  .preview-box { background: var(--main); border: 1px solid var(--line-soft); border-radius: 6px; padding: var(--s1) var(--s3); }
  .ver-field input, .signin-form input { background: var(--field); border: 1px solid var(--line); border-radius: 6px; padding: 5px 9px; font-size: var(--t-sm); color: var(--ink); min-width: 0; flex: 1; }
  .signin-form { display: grid; gap: var(--s2); margin-top: 10px; padding: 10px; background: var(--main); border: 1px solid var(--line-soft); border-radius: 6px; }
  .signin-form label, .ver-field { display: grid; gap: var(--s1); }
  .toolbar.install { display: grid; gap: var(--s2); justify-items: start; }
  .toolbar.install .ver-field { justify-self: stretch; }
  .link { font-size: var(--t-sm); color: var(--muted); text-decoration: underline; text-underline-offset: 3px; }
  .link:not(:disabled) { transition: color var(--motion-fast) var(--ease); }
  @media (hover: hover) { .link:hover:not(:disabled) { color: var(--ink); } }
  .installing { display: grid; gap: var(--s2); }
  /* An install has no percentage: a short bar sweeps until it ends. The
     global reduced-motion rule stops the sweep. */
  .progress { position: relative; display: block; width: 72px; height: 3px; border-radius: 2px; background: var(--line); overflow: hidden; }
  .progress.wide { width: 100%; }
  .progress::after { content: ''; position: absolute; inset: 0 auto 0 0; width: 40%; border-radius: inherit; background: var(--iris); animation: sweep 1.2s var(--ease) infinite; }
  @keyframes sweep { from { transform: translateX(-100%); } to { transform: translateX(250%); } }
  .terminal { display: flex; height: 240px; border: 1px solid var(--line); border-radius: 8px; overflow: hidden; }
  .login-box { margin-top: var(--s1); }
  .signin-card { display: grid; gap: 6px; justify-items: start; padding: 14px; background: var(--main); border: 1px solid var(--line-soft); border-radius: 8px; }
  .signin-card p { margin: 0; font-size: var(--t-ui); }
  .signin-card .btn { margin-top: 6px; }
  .toolbar { margin-bottom: var(--s4); }
  .acct { display: flex; flex-wrap: wrap; align-items: center; justify-content: space-between; gap: var(--s1) var(--s3); min-height: 38px; font-size: var(--t-ui); }
  .acct-name { display: flex; flex: 1 1 15rem; flex-wrap: wrap; align-items: center; gap: var(--s1) var(--s2); min-width: 0; }
  .acct .btn-group { flex-wrap: nowrap; }
  .acct-name .label { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .dot { width: 6px; height: 6px; flex: none; border-radius: 50%; border: 1px solid var(--line); }
  .dot.on { background: var(--sage); border-color: var(--sage); }
  .in-use { color: var(--sage); }
  .scope { color: var(--muted); white-space: nowrap; }
  .scope.limited { color: var(--ink); }
  .signin-form textarea { background: var(--field); border: 1px solid var(--line); border-radius: 6px; padding: 5px 9px; font: inherit; font-size: var(--t-sm); color: var(--ink); resize: vertical; min-width: 0; }
  .acct .btn.quiet { border-color: transparent; color: var(--muted); }
  .btn.add { margin-top: var(--s1); padding-left: 0; border-color: transparent; color: var(--muted); }
  .btn.add span { margin-right: 6px; }
  .tools-bar { display: flex; justify-content: space-between; align-items: center; flex-wrap: wrap; gap: var(--s3); margin: 18px 0 6px; }
  #tools-panel { overflow-x: auto; }
  .thead, .trow { min-width: 720px; display: grid; grid-template-columns: minmax(0, 1fr) repeat(8, 56px); align-items: center; justify-items: center; gap: 0; text-align: center; padding: 11px 0; border-bottom: 1px solid var(--line-soft); }
  .thead { font-size: var(--t-cap); color: var(--muted); text-transform: none; letter-spacing: 0; }
  .hlabel { display: flex; flex-direction: column; align-items: center; gap: 6px; font-size: var(--t-cap); }
  .trow > div:first-child { text-align: left; min-width: 0; padding-right: var(--s2); overflow-wrap: anywhere; }
  .trow > div:first-child { justify-self: stretch; }
  .error { color: var(--coral); font-size: var(--t-ui); margin: var(--s3) 0 0; }
  .notice { color: var(--muted); font-size: var(--t-sm); margin: var(--s3) 0 0; }
  .panel .error, .panel .notice { margin: 0 0 var(--s3); }
  code { font-family: var(--mono); font-size: var(--t-cap); }
  /* Narrow: the detail panel opens under the selected row. Rows and panel
     share one column, ordered by their inline order values. */
  @container (max-width: 860px) {
    .split { display: flex; flex-direction: column; align-items: stretch; gap: 0; }
    .list { display: contents; }
    .panel { margin: var(--s2) 0 var(--s3); padding: var(--s4); }
    .ph { display: none; }
  }
  @media (max-width: 720px) {
    .thead, .trow { grid-template-columns: minmax(120px, 1fr) repeat(8, 56px); font-size: var(--t-cap); }
  }
</style>
