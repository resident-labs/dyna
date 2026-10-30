<script lang="ts">
  import UsageDrawer from './UsageDrawer.svelte'
  import UsageMeter from './UsageMeter.svelte'
  import { accountKindLabel, accountLabel, accountStatus, dateTime, needsSignIn, providerName, renewalText, reposLabel, usageUnavailableText } from './state.ts'
  import { blockUntil } from './reset.ts'
  import { deadlineClock } from './clock.svelte.ts'
  import type { ScopeAccount } from '../../api/account.ts'
  import type { ModelSelection } from '../profiles/state.ts'

  // The details of one account: its limits, who it signs in to and the profiles
  // that run it. A phone shows it as a bottom sheet, a desktop as a drawer.
  let {
    account,
    onclose,
    onsignin,
    oncreateprofile,
    onprofile,
  }: {
    account: ScopeAccount
    onclose: () => void
    onsignin: (account: ScopeAccount) => void
    oncreateprofile: (selection: ModelSelection) => void
    onprofile: (name: string) => void
  } = $props()
  // The header wakes when the last blocking window resets, so it stops saying
  // "limit reached" on an open page.
  const now = deadlineClock(() => blockUntil(account))
  const status = $derived([accountStatus(account, '', now()), account.stale ? 'Stale report' : ''].filter(Boolean).join(' · '))
  function createProfile() {
    if (!account.activeHere) return
    oncreateprofile({ harness: account.harness, routeId: account.routeId, connectionId: account.id })
  }
</script>
<UsageDrawer title={accountLabel(account)} {onclose}>
  {#if status}<p class="status">{status}</p>{/if}
  {#if needsSignIn(account)}<div class="actions"><button class="btn pri" onclick={() => onsignin(account)}>Sign in</button></div>{/if}
  <section><h3>Account limits</h3>
    {#if account.windows?.length}<div class="windows">{#each account.windows as window (window.id || window.label)}<UsageMeter {window} provider={account.provider} observedAt={account.observedAt} />{/each}</div>
    {:else}<p class="muted">{usageUnavailableText(account)}</p>{/if}
  </section>
  <section><h3>Connection</h3><dl>
    <dt>Provider</dt><dd>{providerName(account.provider)}</dd>
    {#if account.kind}<dt>Signed in</dt><dd>{accountKindLabel(account.kind)}</dd>{/if}
    {#if account.plan}{@const plan = account.plan}<dt>Plan</dt><dd>{typeof plan === 'string' ? plan : plan.label || 'Reported'}</dd>{/if}
    {#if account.source}<dt>Source</dt><dd>{account.source}</dd>{/if}
    <dt>Repos</dt><dd>{reposLabel(account)}</dd>
    {#if account.observedAt}<dt>Checked</dt><dd>{dateTime(account.observedAt) || 'Not reported'}</dd>{/if}
    {#if account.lastRenewal}<dt>Engine renewal</dt><dd>{renewalText(account.lastRenewal)}</dd>{/if}
  </dl></section>
  <section><h3>Linked profiles</h3><div class="profiles">
    {#each account.profiles ?? [] as name}<button class="quiet" onclick={() => onprofile(name)}>{name} ↗</button>{:else}<p class="muted">No linked profiles reported</p>{/each}
  </div><div class="actions"><button class="btn pri" onclick={createProfile} disabled={!account.activeHere} aria-describedby={!account.activeHere ? 'profile-account-hint' : undefined}>Create profile</button></div>{#if !account.activeHere}<p id="profile-account-hint" class="muted">Use this account in this scope before creating a profile.</p>{/if}</section>
</UsageDrawer>
<style>
  .status { color: var(--muted); font-size: var(--t-sm); margin: -12px 0 20px; }
  section { border-top: 1px solid var(--line); padding: 22px 0; }
  h3 { margin: 0; font-size: var(--t-body); font-weight: 500; }
  .windows { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 20px; margin-top: 18px; }
  .windows :global(.window:last-child:nth-child(odd)) { grid-column: 1 / -1; }
  .muted { color: var(--muted); font-size: var(--t-sm); line-height: 1.65; }
  dl { display: grid; grid-template-columns: 78px minmax(0, 1fr); gap: var(--s3); margin: 17px 0 0; font-size: var(--t-sm); }
  dt { color: var(--muted); } dd { margin: 0; overflow-wrap: anywhere; }
  .profiles, .actions { display: flex; flex-wrap: wrap; gap: var(--s2); margin-top: var(--s4); }
  .profiles .quiet { border: 1px solid var(--line); border-radius: 6px; padding: var(--s2) var(--s3); min-height: 36px; font-size: var(--t-sm); color: var(--ink-2); }
  @media (pointer: coarse) { .profiles .quiet { min-height: 44px; } }
</style>
