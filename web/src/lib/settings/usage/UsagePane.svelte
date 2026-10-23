<script lang="ts">
  import { errorText } from '../../errorText.ts'
  import { onMount, onDestroy, tick } from 'svelte'
  import Loading from '../Loading.svelte'
  import TabHeader from '../../rooms/TabHeader.svelte'
  import EmptyState from '../../ui/EmptyState.svelte'
  import { api } from '../../../api.ts'
  import SubscriptionCard from './SubscriptionCard.svelte'
  import AccountDetails from './AccountDetails.svelte'
  import LocalActivity from './LocalActivity.svelte'
  import { requestGate, usageGroups } from './state.ts'
  import type { UsageAccount, UsageSnapshot } from '../../api/account.ts'
  import type { ModelSelection } from '../profiles/state.ts'
  interface Props {
    onmodal?: (open: boolean) => void
    accountId?: string
    roomId?: string
    oncreateprofile?: (selection: ModelSelection) => void
    onprofile?: (name: string) => void
    onharnesses?: () => void
  }
  let { onmodal = () => {}, accountId = '', roomId = '', oncreateprofile = () => {}, onprofile = () => {}, onharnesses = () => {} }: Props = $props()
  let snapshot = $state<UsageSnapshot | null>(null), loading = $state(false), error = $state('')
  let tab = $state('accounts'), modal = $state<{ account: UsageAccount; key: number } | null>(null)
  let activity = $state<ReturnType<typeof LocalActivity>>()
  let mounted = false, opener: EventTarget | null | undefined, modalSequence = 0, openedInitial = false
  const gate = requestGate()
  const accounts = $derived(snapshot?.accounts ?? [])
  const groups = $derived(usageGroups(accounts))
  $effect(() => onmodal(!!modal))
  onMount(() => { mounted = true; load(false) })
  onDestroy(() => { mounted = false; gate.invalidate(); onmodal(false) })
  async function load(refresh = false) {
    const ticket = gate.next(); loading = true; error = ''
    try {
      const next = refresh ? await api.accountUsage(true) : await api.accountUsageCached()
      if (!mounted || !gate.current(ticket)) return
      snapshot = next
      if (!openedInitial && accountId) {
        openedInitial = true
        const account = next.accounts?.find((a: UsageAccount) => a.id === accountId)
        if (account) open(account)
      }
    } catch (e) { if (mounted && gate.current(ticket)) error = errorText(e) }
    finally { if (mounted && gate.current(ticket)) loading = false }
  }
  // One Refresh button: it reloads whichever tab is showing.
  function refresh() {
    if (tab === 'activity') activity?.reload()
    else load(true)
  }
  function open(account: UsageAccount, event?: MouseEvent) {
    if (!modal) opener = event?.currentTarget ?? document.activeElement
    modal = { account, key: ++modalSequence }
  }
  async function close() { modal = null; await tick(); if (mounted && opener instanceof HTMLElement && opener.isConnected) opener.focus() }
  function signIn() { modal = null; onharnesses() }
  function createProfile(selection: ModelSelection) { modal = null; oncreateprofile(selection) }
  function viewProfile(name: string) { modal = null; onprofile(name) }
  function tabKey(e: KeyboardEvent) {
    if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(e.key)) return
    e.preventDefault()
    tab = e.key === 'Home' ? 'accounts' : e.key === 'End' ? 'activity' : tab === 'accounts' ? 'activity' : 'accounts'
    document.getElementById('usage-' + tab + '-tab')?.focus()
  }
</script>
<section class="usage">
  <TabHeader flush title="Usage">
    {#snippet action()}<button class="btn quiet" onclick={refresh} disabled={tab === 'accounts' && loading}>{tab === 'accounts' && loading ? 'Refreshing…' : 'Refresh'}</button>{/snippet}
  </TabHeader>
  <div class="tabs" role="tablist" tabindex="-1" aria-label="Usage views" onkeydown={tabKey}>
    <button id="usage-accounts-tab" role="tab" aria-selected={tab === 'accounts'} aria-controls="usage-accounts" tabindex={tab === 'accounts' ? 0 : -1} onclick={() => (tab = 'accounts')}>Account limits</button>
    <button id="usage-activity-tab" role="tab" aria-selected={tab === 'activity'} aria-controls="usage-activity" tabindex={tab === 'activity' ? 0 : -1} onclick={() => (tab = 'activity')}>Dyna activity</button>
  </div>
  {#if error}<p class="error" role="alert">{error}</p>{/if}
  {#if snapshot?.message}<p class="error" role="alert">{snapshot.message}</p>{/if}
  <div id="usage-accounts" role="tabpanel" aria-labelledby="usage-accounts-tab" hidden={tab !== 'accounts'}>
    <div class="section-heading"><h2>Your accounts</h2>{#if accounts.length}<span>{accounts.length} connections</span>{/if}</div>
    {#if snapshot}<div class="accounts" aria-busy={loading}>
      {#each groups as group (group.id)}
        <SubscriptionCard {group} onopen={open} onsignin={signIn} />
      {:else}<EmptyState title="No harness accounts">Sign in to a harness under Harnesses, and its limits show here.</EmptyState>{/each}
    </div>{:else if loading}<Loading>Loading accounts…</Loading>{/if}
  </div>
  {#if tab === 'activity'}<div id="usage-activity" role="tabpanel" aria-labelledby="usage-activity-tab"><LocalActivity bind:this={activity} {roomId} /></div>{/if}
</section>
{#if modal}{#key modal.key}<AccountDetails account={modal.account} onclose={close} onsignin={signIn} oncreateprofile={createProfile} onprofile={viewProfile} />{/key}{/if}
<style>
  .tabs { display: flex; gap: var(--s1); margin-bottom: var(--s5); }
  .tabs button { border-radius: 14px; padding: var(--s1) 10px; font-size: var(--t-sm); color: var(--muted); min-height: 28px; transition: background-color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .tabs button:hover { background: var(--hover); }
  }
  .tabs button:active { background: var(--select); }
  .tabs button[aria-selected=true] { background: var(--select); color: var(--ink); }
  .section-heading { display: flex; justify-content: space-between; align-items: center; gap: var(--s3); margin-bottom: 17px; }
  h2 { margin: 0; font-size: var(--t-title); font-weight: 500; } .section-heading>span { color: var(--muted); background: var(--raise); border-radius: 4px; padding: 3px 7px; font-size: var(--t-cap); }
  .accounts { display: grid; gap: var(--s3); }
  .error { color: var(--coral); font-size: var(--t-ui); overflow-wrap: anywhere; }
  [hidden] { display: none !important; }
  @media (pointer: coarse) { .btn, .tabs button { min-height: 44px; } }
</style>
