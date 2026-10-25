<script lang="ts">
  import { errorText } from '../../errorText.ts'
  import { onMount, onDestroy, tick } from 'svelte'
  import Loading from '../Loading.svelte'
  import TabHeader from '../../rooms/TabHeader.svelte'
  import EmptyState from '../../ui/EmptyState.svelte'
  import Confirm from '../../ui/Confirm.svelte'
  import Card from '../../ui/Card.svelte'
  import { api } from '../../../api.ts'
  import SubscriptionCard from './SubscriptionCard.svelte'
  import AccountDetails from './AccountDetails.svelte'
  import AddAccount from './AddAccount.svelte'
  import LocalActivity from './LocalActivity.svelte'
  import { accountLabel, accountsOf, problemText, providerName, requestGate } from './state.ts'
  import type { AccountsSnapshot, ScopeAccount } from '../../api/account.ts'
  import type { ModelSelection } from '../profiles/state.ts'

  // The Accounts page: every saved provider sign-in, grouped by provider, with
  // the usage of each account and the account its provider uses in the selected
  // scope. The Activity report is the second tab.
  interface Props {
    onmodal?: (open: boolean) => void
    accountId?: string
    roomId?: string
    oncreateprofile?: (selection: ModelSelection) => void
    onprofile?: (name: string) => void
  }
  let { onmodal = () => {}, accountId = '', roomId = '', oncreateprofile = () => {}, onprofile = () => {} }: Props = $props()
  let snapshot = $state<AccountsSnapshot | null>(null)
  let loading = $state(false)
  let error = $state('')
  let notice = $state('')
  let tab = $state('accounts')
  let roomScope = $state(false)
  let adding = $state(false)
  let signInFor = $state<{ id: string; label: string } | null>(null)
  let removing = $state<ScopeAccount | null>(null)
  let busy = $state(false)
  let detail = $state<{ account: ScopeAccount; key: number } | null>(null)
  let activity = $state<ReturnType<typeof LocalActivity>>()
  let mounted = false
  let opener: EventTarget | null | undefined
  let detailSequence = 0
  let openedInitial = false
  const gate = requestGate()

  // scope is the room whose choice the page shows, or empty for the global one.
  const scope = $derived(roomScope && roomId ? roomId : '')
  const groups = $derived(snapshot?.providers ?? [])
  const problems = $derived(snapshot?.problems ?? [])
  const count = $derived(accountsOf(snapshot).length)

  $effect(() => onmodal(!!detail || adding || !!signInFor || !!removing))
  onMount(() => { mounted = true; void load(false) })
  onDestroy(() => { mounted = false; gate.invalidate(); onmodal(false) })

  async function load(refresh = false) {
    const wanted = scope
    const ticket = gate.next()
    loading = true; error = ''
    try {
      const next = refresh ? await api.accountsList(wanted, true) : await api.accountsView(wanted)
      if (!mounted || !gate.current(ticket)) return
      snapshot = next
      if (!openedInitial && accountId) {
        openedInitial = true
        const account = accountsOf(next).find((a) => a.id === accountId)
        if (account) open(account)
      }
    } catch (e) { if (mounted && gate.current(ticket)) error = errorText(e) || 'Could not read the accounts.' }
    finally { if (mounted && gate.current(ticket)) loading = false }
  }

  // One Refresh button: it reloads whichever tab is showing.
  function refresh() {
    if (tab === 'activity') activity?.reload()
    else void load(true)
  }

  function toggleScope() {
    if (!roomId) return
    roomScope = !roomScope
    notice = ''
    void load(false)
  }

  async function use(account: ScopeAccount) {
    if (busy) return
    busy = true; error = ''; notice = ''
    try {
      snapshot = await api.accountsUse(account.id, scope)
      notice = `${accountLabel(account)} is now in use.`
    } catch (e) { if (mounted) error = errorText(e) || 'Could not choose the account.' }
    finally { if (mounted) busy = false }
  }

  async function saveRepos(account: ScopeAccount, repos: string[]): Promise<boolean> {
    if (busy) return false
    busy = true; error = ''; notice = ''
    try {
      await api.setAccountRepos(account.id, repos)
      await load(false)
      notice = `Saved the repo limit of ${accountLabel(account)}.`
      return true
    } catch (e) { if (mounted) error = errorText(e) || 'Could not save the repo list.'; return false }
    finally { if (mounted) busy = false }
  }

  async function remove(account: ScopeAccount) {
    if (busy) return
    busy = true; error = ''; notice = ''
    try {
      await api.accountsRemove(account.id)
      removing = null
      await load(false)
      notice = `Removed ${accountLabel(account)}.`
    } catch (e) { if (mounted) error = errorText(e) || 'Could not remove the account.' }
    finally { if (mounted) busy = false }
  }

  function startSignIn(account: ScopeAccount) {
    detail = null
    signInFor = { id: account.id, label: accountLabel(account) }
  }

  function open(account: ScopeAccount, event?: MouseEvent) {
    if (!detail) opener = event?.currentTarget ?? document.activeElement
    detail = { account, key: ++detailSequence }
  }
  async function closeDetails() {
    detail = null
    await tick()
    if (mounted && opener instanceof HTMLElement && opener.isConnected) opener.focus()
  }

  async function saved(next: AccountsSnapshot) {
    adding = false
    signInFor = null
    if (next) snapshot = next
    await load(false)
  }

  function createProfile(selection: ModelSelection) { detail = null; oncreateprofile(selection) }
  function viewProfile(name: string) { detail = null; onprofile(name) }

  function tabKey(e: KeyboardEvent) {
    if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(e.key)) return
    e.preventDefault()
    tab = e.key === 'Home' ? 'accounts' : e.key === 'End' ? 'activity' : tab === 'accounts' ? 'activity' : 'accounts'
    document.getElementById('accounts-' + tab + '-tab')?.focus()
  }
</script>

<section class="accounts">
  <TabHeader flush title="Accounts" blurb="Provider sign-ins, their usage, and the account each provider uses.">
    {#snippet action()}
      <button class="btn quiet" onclick={refresh} disabled={tab === 'accounts' && loading}>{tab === 'accounts' && loading ? 'Refreshing…' : 'Refresh'}</button>
      <button class="btn pri" onclick={() => (adding = true)}>Add account</button>
    {/snippet}
  </TabHeader>

  <div class="tabs" role="tablist" tabindex="-1" aria-label="Account views" onkeydown={tabKey}>
    <button id="accounts-accounts-tab" role="tab" aria-selected={tab === 'accounts'} aria-controls="accounts-accounts" tabindex={tab === 'accounts' ? 0 : -1} onclick={() => (tab = 'accounts')}>Accounts</button>
    <button id="accounts-activity-tab" role="tab" aria-selected={tab === 'activity'} aria-controls="accounts-activity" tabindex={tab === 'activity' ? 0 : -1} onclick={() => (tab = 'activity')}>Activity</button>
  </div>

  {#if error}<p class="error" role="alert">{error}</p>{/if}
  {#if notice}<p class="notice" role="status">{notice}</p>{/if}
  {#if snapshot?.message}<p class="error" role="alert">{snapshot.message}</p>{/if}

  <div id="accounts-accounts" role="tabpanel" aria-labelledby="accounts-accounts-tab" hidden={tab !== 'accounts'}>
    {#if roomId}
      <div class="scope" role="group" aria-label="Account scope">
        <button class="btn" aria-pressed={!roomScope} class:sel={!roomScope} onclick={() => { if (roomScope) toggleScope() }}>Global</button>
        <button class="btn" aria-pressed={roomScope} class:sel={roomScope} onclick={() => { if (!roomScope) toggleScope() }}>This room</button>
        <span class="muted">A room choice overrides the global one for this room's turns.</span>
      </div>
    {/if}
    {#if snapshot}<div class="groups" aria-busy={loading} aria-label="Accounts by provider">
      {#if count}<p class="muted count">{count} {count === 1 ? 'account' : 'accounts'}{roomScope && roomId ? ' · this room' : ''}</p>{/if}
      {#each groups as group (group.provider)}
        <SubscriptionCard {group} {roomScope} {busy} onopen={open} onuse={use} onsignin={startSignIn} onsaverepos={saveRepos} onremove={(account) => (removing = account)} />
      {:else}<EmptyState title="No accounts yet">Choose Add account, then sign in to a provider or paste an API key. Dyna saves one account for each provider you sign in to.</EmptyState>{/each}
      {#if problems.length}
        <Card title="Saved accounts Dyna skipped" count={problems.length}>
          {#each problems as problem (problem.id)}<p class="problem" role="status">{problemText(problem)}</p>{/each}
        </Card>
      {/if}
    </div>{:else if loading}<Loading>Loading accounts…</Loading>{/if}
  </div>

  {#if tab === 'activity'}<div id="accounts-activity" role="tabpanel" aria-labelledby="accounts-activity-tab"><LocalActivity bind:this={activity} {roomId} /></div>{/if}
</section>

{#if detail}{#key detail.key}
  <AccountDetails account={detail.account} onclose={closeDetails} onsignin={startSignIn} oncreateprofile={createProfile} onprofile={viewProfile} />
{/key}{/if}

{#if adding || signInFor}
  <AddAccount {scope} account={signInFor} onclose={() => { adding = false; signInFor = null }} onsaved={saved} />
{/if}

{#if removing}
  {@const target = removing}
  <Confirm
    title={`Remove ${accountLabel(target)}?`}
    lines={['Dyna deletes the saved sign-in and the account\'s own home. Turns that use it stop working.', `${providerName(target.provider)} keeps its other accounts.`]}
    action="Remove"
    danger
    {busy}
    onconfirm={() => void remove(target)}
    oncancel={() => { if (!busy) removing = null }}
  />
{/if}

<style>
  .accounts { container-type: inline-size; }
  .tabs { display: flex; gap: var(--s1); margin-bottom: var(--s4); }
  .tabs button { border-radius: 14px; padding: var(--s1) 10px; font-size: var(--t-sm); color: var(--muted); min-height: 28px; transition: background-color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .tabs button:hover { background: var(--hover); }
  }
  .tabs button:active { background: var(--select); }
  .tabs button[aria-selected=true] { background: var(--select); color: var(--ink); }
  .scope { display: flex; flex-wrap: wrap; align-items: center; gap: var(--s2); margin-bottom: var(--s4); }
  .scope .sel { background: var(--select); border-color: var(--line); color: var(--ink); }
  .scope .muted { font-size: var(--t-sm); color: var(--muted); flex: 1 1 14rem; }
  .count { margin: 0 0 var(--s2); font-size: var(--t-sm); }
  .groups { display: grid; gap: var(--s3); }
  .problem { margin: 0; color: var(--coral); font-size: var(--t-sm); overflow-wrap: anywhere; }
  .error { color: var(--coral); font-size: var(--t-ui); overflow-wrap: anywhere; }
  .notice { color: var(--muted); font-size: var(--t-sm); }
  [hidden] { display: none !important; }
  @media (pointer: coarse) { .btn, .tabs button { min-height: 44px; } }
</style>
