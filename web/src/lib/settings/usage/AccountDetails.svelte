<script lang="ts">
  import UsageDrawer from './UsageDrawer.svelte'
  import UsageMeter from './UsageMeter.svelte'
  import { accountLabel, accountStatus, dateTime, harnessName, needsSignIn } from './state.ts'
  import { blockUntil } from './reset.ts'
  import { deadlineClock } from './clock.svelte.ts'
  import type { UsageAccount } from '../../api/account.ts'
  import type { ModelSelection } from '../profiles/state.ts'
  interface Props {
    account: UsageAccount
    onclose: () => void
    onsignin?: () => void
    oncreateprofile: (selection: ModelSelection) => void
    onprofile?: (name: string) => void
  }
  let { account, onclose, onsignin = () => {}, oncreateprofile, onprofile = () => {} }: Props = $props()
  // The header wakes when the last blocking window resets, so it stops saying
  // "limit reached" on an open page.
  const now = deadlineClock(() => blockUntil(account))
  const status = $derived([accountStatus(account, '', now()), account.stale ? 'Stale report' : ''].filter(Boolean).join(' · '))
  function createProfile() {
    if (!account.active) return
    oncreateprofile({ harness: account.harness, routeId: account.routeId, connectionId: account.id })
  }
</script>
<UsageDrawer title={accountLabel(account)} {onclose}>
  {#if status}<p class="status">{status}</p>{/if}
  {#if needsSignIn(account)}<div class="actions"><button class="primary" onclick={onsignin} title="Open Harnesses to sign in">Sign in</button></div>{/if}
  <section><h3>Account limits</h3>
    {#if account.windows?.length}<div class="windows">{#each account.windows as window (window.id || window.label)}<UsageMeter {window} observedAt={account.observedAt} />{/each}</div>
    {:else}<p class="muted">Live remaining not reported</p>{/if}
  </section>
  <section><h3>Connection</h3><dl>
    <dt>Harness</dt><dd>{harnessName(account.harness)}</dd>
    {#if account.providerId}<dt>Provider</dt><dd>{account.providerId}</dd>{/if}
    {#if account.routeId}<dt>Route</dt><dd>{account.routeId}</dd>{/if}
    {#if account.source}<dt>Source</dt><dd>{account.source}</dd>{/if}
    {#if account.observedAt}<dt>Checked</dt><dd>{dateTime(account.observedAt) || 'Not reported'}</dd>{/if}
  </dl></section>
  <section><h3>Linked profiles</h3><div class="profiles">
    {#each account.profiles ?? [] as name}<button onclick={() => onprofile(name)}>{name} ↗</button>{:else}<p class="muted">No linked profiles reported</p>{/each}
  </div><div class="actions"><button class="primary" onclick={createProfile} disabled={!account.active} aria-describedby={!account.active ? 'profile-account-hint' : undefined}>Create profile</button></div>{#if !account.active}<p id="profile-account-hint" class="muted">Select this account before creating a profile.</p>{/if}</section>
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
  button { border: 1px solid var(--line); border-radius: 6px; padding: var(--s2) var(--s3); min-height: 36px; font-size: var(--t-sm); color: var(--ink-2); }
  button:disabled { opacity: .5; cursor: default; }
  button:not(:disabled) { transition: background-color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    button:hover:not(:disabled) { background: var(--raise-hover); }
  }
  button:active:not(:disabled) { background: var(--select); } .primary:active:not(:disabled) { color: var(--ink); } .primary { background: var(--gold); border-color: var(--gold); color: var(--main); }
  @media (pointer: coarse) { button { min-height: 44px; } }
</style>
