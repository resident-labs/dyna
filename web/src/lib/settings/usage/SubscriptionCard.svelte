<script lang="ts">
  import Card from '../../ui/Card.svelte'
  import HarnessIcon from '../harnesses/HarnessIcon.svelte'
  import UsageMeter from './UsageMeter.svelte'
  import { accountLabel, accountStatus, harnessIcon, harnessName, needsSignIn, observation, unreadText, remainingPercent } from './state.ts'
  import { blockUntil } from './reset.ts'
  import { deadlineClock } from './clock.svelte.ts'
  import type { UsageAccount } from '../../api/account.ts'
  import type { UsageGroup } from './state.ts'
  interface Props {
    group: UsageGroup
    compact?: boolean
    onopen?: ((account: UsageAccount, event: MouseEvent) => void) | null
    onsignin?: (() => void) | null
  }
  let { group, compact = false, onopen = null, onsignin = null }: Props = $props()
  // One wake for the card: it starts at the first account block that ends, and the
  // clock asks for the next deadline after each wake.
  const now = deadlineClock(() => {
    const deadlines = group.accounts.map((account) => blockUntil(account)).filter((at) => Number.isFinite(at))
    return deadlines.length ? Math.min(...deadlines) : Number.NaN
  })
</script>

<Card title={harnessName(group.accounts[0]?.harness)}>
  {#each group.accounts as account (account.id)}
    {@const status = [accountStatus(account, group.plan, now()), account.active && !compact ? 'Active' : ''].filter(Boolean).join(' · ')}
    <div class="account" class:compact>
      <div class="heading">
        {#if !compact}<HarnessIcon id={harnessIcon(account.harness)} />{/if}
        <div class="identity">
          <b>{accountLabel(account)}</b>
          {#if status}<p>{status}</p>{/if}
        </div>
        <div class="actions">
          {#if needsSignIn(account) && onsignin}<button onclick={onsignin}>Sign in</button>{/if}
          {#if onopen}<button onclick={(event) => onopen(account, event)}>Details →</button>{/if}
        </div>
      </div>
      {#if account.windows?.length}
        <div class="windows">{#each account.windows as window (window.id || window.label)}<UsageMeter {window} observedAt={account.observedAt} compact={compact && remainingPercent(window) !== null} />{/each}</div>
      {:else}<p class="note">{unreadText(account)}</p>{/if}
      {#if !compact && observation(account)}<p class="note">{observation(account)}</p>{/if}
    </div>
  {/each}
</Card>

<style>
  .account { min-width: 0; padding-top: var(--s2); }
  .account + .account { border-top: 1px solid var(--line); margin-top: var(--s2); }
  .heading { display: flex; align-items: center; gap: var(--s2); flex-wrap: wrap; }
  .identity { flex: 1; min-width: 0; }
  b { font-size: var(--t-body); font-weight: 500; overflow-wrap: anywhere; }
  p { margin: var(--s1) 0 0; color: var(--muted); font-size: var(--t-sm); overflow-wrap: anywhere; }
  .actions { display: flex; gap: var(--s2); }
  button { color: var(--gold); font-size: var(--t-sm); overflow-wrap: anywhere; min-height: 36px; white-space: nowrap; }
  .windows { display: grid; gap: var(--s3); margin-top: var(--s3); }
  .note { line-height: 1.7; }
  .compact .windows { gap: var(--s2); margin-top: var(--s2); }
  @media (pointer: coarse) { button { min-height: 44px; } }
</style>
