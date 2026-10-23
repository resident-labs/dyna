<script module lang="ts">
  import type { LoginFlow } from './state.ts'
  // Keep a running sign-in reachable when Settings closes or changes panes.
  let pendingLogin: LoginFlow | null = null
</script>

<script lang="ts">
  import { onMount, onDestroy, tick } from 'svelte'
  import { errorText } from '../../errorText.ts'
  import { fly } from 'svelte/transition'
  import { BASE, motion } from '../../ui/motion.ts'
  import Loading from '../Loading.svelte'
  import { api } from '../../../api.ts'
  import HarnessIcon from './HarnessIcon.svelte'
  import HarnessList from './HarnessList.svelte'
  import HarnessTabs from './HarnessTabs.svelte'
  import InstallControls from './InstallControls.svelte'
  import AccountsSection from './AccountsSection.svelte'
  import ProvidersSection from './ProvidersSection.svelte'
  import SettingsImportSection from './SettingsImportSection.svelte'
  import HarnessToolsSection from './HarnessToolsSection.svelte'
  import ToolsTab from './ToolsTab.svelte'
  import Confirm from '../../ui/Confirm.svelte'
  import TabHeader from '../../rooms/TabHeader.svelte'
  import ProviderKeyDialog from './ProviderKeyDialog.svelte'
  import {
    HARNESS_ORDER,
    exactVersion,
    failText,
    harnessRowState,
    hasSubscriptionLogin,
    parseScopeList,
    personalLoginChoice,
    recommendedVersion,
    recoveredLogins,
    takesProviderKeys,
  } from './state.ts'
  import type { Confirmation, HarnessRow } from './state.ts'
  import type { HarnessAccount, HarnessConfig, HarnessSnapshot, PersonalLogin, PreferencePreview, ProviderKey, SignInState } from '../../api/harness.ts'

  let { onmodal = () => {} }: { onmodal?: (open: boolean) => void } = $props()

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
  // signInState is the live step of a sign-in running in the engine.
  let signInState = $state<SignInState | null>(pendingLogin?.signIn ?? null)
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

  // The pane polls the native sign-in step and collects the completed account.
  $effect(() => {
    const flow = login
    if (!flow) {
      signInState = null
      return
    }
    signInState = flow.signIn ?? { phase: 'starting' }
    const accountId = flow.accountId
    let live = true
    let finishing = false
    const tick = async () => {
      try {
        const next = await api.privateRuntimeLoginState(accountId)
        if (!live) return
        signInState = next
        if (next.phase === 'succeeded' && !finishing) {
          finishing = true
          await finishLogin()
          finishing = false
        }
      } catch {}
    }
    void tick()
    const timer = setInterval(tick, 2000)
    return () => {
      live = false
      clearInterval(timer)
    }
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
        notice = 'Follow the sign-in steps below.'
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
        notice = 'Follow the sign-in steps below.'
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
      if (!stopped) error = failText(e, 'Sign-in is not complete yet.')
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

  async function respondPaste(code: string) {
    if (busy || stopped || !login?.accountId) return false
    busy = true
    error = ''
    try {
      await api.respondPrivateRuntimeLogin(login.accountId, code)
      return true
    } catch (e) {
      if (!stopped) error = failText(e, 'Could not send the code.')
      return false
    } finally {
      if (!stopped) busy = false
    }
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
      const repos = parseScopeList(scopeText)
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
      <HarnessList
        {rows}
        selected={selectedRow?.id ?? ''}
        {busy}
        signingIn={!!login}
        onselect={selectHarness}
        oninstall={install}
        onsignin={(r) => { selectHarness(r.id); signIn(r) }}
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
            signingIn={!!login}
            bind:chooseVersion
            bind:versionInput
            bind:removing
            oninstall={(pin) => install(r, pin)}
            onuninstall={() => uninstall(r)}
          />
          {#if error}<p class="error" role="alert">{error}</p>{/if}
          {#if notice}<p class="notice" role="status">{notice}</p>{/if}

          {#if hasSubscriptionLogin(r.id) || (login && login.harness === r.id)}
            <AccountsSection
              row={r}
              {busy}
              signingIn={!!login}
              login={login && login.harness === r.id ? login : null}
              {signInState}
              scanning={scanning === r.id}
              bind:removing
              bind:scoping
              bind:scopeText
              bind:showSignInForm
              bind:labelInput
              onsignin={(add) => signIn(r, add)}
              oncancel={cancelLogin}
              onpaste={respondPaste}
              onrestart={(acct) => requestRestartLogin(r, acct)}
              onswitch={(acct) => requestSwitchAccount(r, acct)}
              onscope={openScope}
              onsavescope={saveScope}
              onremove={removeAccount}
            />
          {/if}

          {#if takesProviderKeys(r.id) && !r.needsInstall && !r.installing}
            <ProvidersSection
              row={r}
              providers={providers[r.id] ?? []}
              {busy}
              bind:removingProvider
              onremove={(provider) => removeProvider(r.id, provider)}
              onadd={() => (addingProvider = true)}
            />
          {/if}

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
            skills={skillRows}
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
      skills={skillRows}
      {rows}
      {busy}
      bind:addToolHint
      bind:removingTool
      onadded={(next) => { catalog = next; addToolHint = ''; notice = 'Tool added. Turn it on for each harness that needs it.' }}
      ontoggle={toggleTool}
      onremovetool={removeTool}
    />
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
  .btn-group { display: flex; flex-wrap: wrap; align-items: center; gap: var(--s2); }
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
