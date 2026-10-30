<script lang="ts">
  import Card from '../../ui/Card.svelte'
  import StatusPill from '../../ui/StatusPill.svelte'
  import UsageMeter from './UsageMeter.svelte'
  import UsageSegment from './UsageSegment.svelte'
  import { accountKindLabel, accountLabel, accountStatus, amountWindow, blockedNow, dateTime, needsSignIn, parseRepoList, percent, poolMean, poolsHere, poolWindows, providerColor, providerName, repoChips, reposLabel, segmentRemaining, usageUnavailableText } from './state.ts'
  import { blockUntil } from './reset.ts'
  import { meterWake, poolPace, type PaceStatus } from './pace.ts'
  import { deadlineClock } from './clock.svelte.ts'
  import type { ProviderAccounts, ScopeAccount } from '../../api/account.ts'

  // One provider and every account saved for it. Each usage window shows a
  // headline over the accounts that can serve this scope and one segment per
  // account; a row per account below holds its actions. The pane reads and
  // writes the accounts, so this only shows them and reports the button the
  // owner pressed. compact is the room home: the same windows, and rows that
  // only open details.
  interface Props {
    group: ProviderAccounts
    compact?: boolean
    roomScope?: boolean
    busy?: boolean
    onopen?: ((account: ScopeAccount, event: MouseEvent) => void) | null
    onuse?: ((account: ScopeAccount) => void) | null
    onsignin?: ((account: ScopeAccount) => void) | null
    onsaverepos?: ((account: ScopeAccount, repos: string[]) => Promise<boolean>) | null
  }
  let { group, compact = false, roomScope = false, busy = false, onopen = null, onuse = null, onsignin = null, onsaverepos = null }: Props = $props()
  const accounts = $derived(group.accounts ?? [])
  const pools = $derived(poolWindows(accounts))
  const single = $derived(accounts.length === 1 ? accounts[0] : undefined)
  // One wake for the card: the first account block that ends, or the next
  // minute while a window counts down, so the headline and pace stay current.
  const now = deadlineClock(() => {
    const at = Date.now()
    const deadlines = accounts.flatMap((account) => [blockUntil(account, at), ...(account.windows ?? []).map((window) => meterWake(window.resetsAt, at))]).filter((deadline) => Number.isFinite(deadline))
    return deadlines.length ? Math.min(...deadlines) : Number.NaN
  })
  const paceWords: Record<PaceStatus, string> = { ahead: '▲ ahead of pace', on: '● on pace', under: '▼ under pace' }
  // The row whose repo list is open, and the list being edited.
  let scoping = $state('')
  let scopeText = $state('')
  let saving = $state(false)
  function editRepos(account: ScopeAccount) {
    scoping = scoping === account.id ? '' : account.id
    scopeText = (account.repos ?? []).join('\n')
  }
  async function saveRepos(account: ScopeAccount) {
    if (!onsaverepos || saving) return
    saving = true
    try {
      if (await onsaverepos(account, parseRepoList(scopeText))) scoping = ''
    } finally {
      saving = false
    }
  }
</script>

<div class="card-wrap" class:clickable={!!onopen && !!single}>
{#if onopen && single}<button class="card-link" aria-label="Open {providerName(group.provider)} account details for {accountLabel(single)}" onclick={(event) => onopen?.(single, event)}></button>{/if}
<Card title={providerName(group.provider)} count={accounts.length > 1 ? accounts.length : null}>
  {#snippet leading()}<span class="provider-dot" style:background={providerColor(group.provider)}></span>{/snippet}
  {#if pools.length}
    <div class="pools">
      {#each pools as pool (pool.key)}
        {@const mean = poolMean(pool, now())}
        {@const pace = poolPace(pool.segments.filter((segment) => poolsHere(segment.account) && segmentRemaining(segment, now()) !== null).map((segment) => segment.window), now())}
        <div class="pool">
          <div class="headline">
            <span class="micro-label">{pool.label}</span>
            {#if mean === null}<span class="none">Not reported</span>{:else}<strong class="ui-num" class:low={mean < 20}>{percent(mean)}</strong>{/if}
            {#if pace}<span class="pace" class:ahead={pace === 'ahead'}>{paceWords[pace]}</span>{/if}
          </div>
          <div class="segments">
            {#each pool.segments as segment (segment.account.id)}
              {#if accounts.length > 1 && onopen}
                <button class="segment-link" aria-label="Open details for {accountLabel(segment.account)}" onclick={(event) => onopen?.(segment.account, event)}><UsageSegment name={accountLabel(segment.account)} window={segment.window} observedAt={segment.account.observedAt} provider={group.provider} active={segment.account.activeHere} muted={!poolsHere(segment.account)} /></button>
              {:else}<UsageSegment name={accountLabel(segment.account)} window={segment.window} observedAt={segment.account.observedAt} provider={group.provider} active={segment.account.activeHere} muted={!poolsHere(segment.account)} showAccount={false} showValue={mean === null && segmentRemaining(segment, now()) !== null} />{/if}
            {/each}
          </div>
        </div>
      {/each}
    </div>
  {/if}
  <div class="accounts" class:compact>
    {#each accounts as account (account.id)}
      {@const block = blockedNow(account, now())}
      {@const signIn = needsSignIn(account) || account.usageReason === 'expired'}
      {@const amounts = (account.windows ?? []).filter(amountWindow)}
      <div class="account">
        <div class="row">
          <div class="identity">
            {#if accounts.length > 1 && onopen}<button class="identity-link" aria-label="Open details for {accountLabel(account)}" onclick={(event) => onopen?.(account, event)}></button>{/if}
            <b>{accountLabel(account)}</b>
            <span class="facts">{[signIn || block ? '' : accountStatus(account, '', now(), ''), accountKindLabel(account.kind), account.servesHere ? '' : 'Other repos only', roomScope && account.active && !account.activeHere ? 'Global choice' : ''].filter(Boolean).join(' · ')}</span>
            {#if accounts.length > 1 && account.activeHere}<span class="facts">In use</span>{/if}
            {#if signIn}<StatusPill label="Needs sign-in" tone="danger" />
            {:else if block}<StatusPill label={block.ids.includes('seven_day') ? 'Weekly limit reached' : 'Session limit reached'} tone="warning" detail={block.reset ? `Resets ${dateTime(block.reset)}` : ''} />{/if}
          </div>
          <div class="actions">
            {#if !compact && signIn && onsignin}<button class="btn pri" disabled={busy} onclick={() => onsignin?.(account)}>Sign in</button>{/if}
            {#if !compact && !account.activeHere && onuse}<button class="btn" disabled={busy} onclick={() => onuse?.(account)}>Use</button>{/if}
            {#if !compact && onsaverepos}<button class="btn quiet" disabled={busy} aria-expanded={scoping === account.id} onclick={() => editRepos(account)}>Repos<span class="repos-label"> · {reposLabel(account)}</span></button>{/if}
          </div>
        </div>
        {#if amounts.length}<div class="amounts">{#each amounts as window (window.id || window.label)}<UsageMeter {window} provider={group.provider} observedAt={account.observedAt} {compact} />{/each}</div>
        {:else if !account.windows?.length}<p class="note">{usageUnavailableText(account)}</p>{/if}
        {#if !compact && repoChips(account).length}<div class="chips">{#each repoChips(account) as repo (repo)}<span class="chip">{repo}</span>{/each}</div>{/if}
        {#if !compact && scoping === account.id && onsaverepos}
          <form class="repos" onsubmit={(e) => { e.preventDefault(); void saveRepos(account) }}>
            <label>
              <span class="muted small">Only use this account in these repos (one per line, empty for any)</span>
              <textarea bind:value={scopeText} rows="3" placeholder="github.com/org/name" disabled={busy || saving}></textarea>
            </label>
            <p class="muted small">A repo named here is closed to accounts without a list.</p>
            <div class="actions">
              <button type="submit" class="btn" disabled={busy || saving}>{saving ? 'Saving…' : 'Save'}</button>
              <button type="button" class="btn quiet" onclick={() => (scoping = '')}>Cancel</button>
            </div>
          </form>
        {/if}
      </div>
    {/each}
  </div>
</Card>
</div>

<style>
  .card-wrap { position: relative; min-width: 0; }
  .card-wrap :global([data-ui-card]) { height: 100%; }
  .card-link { position: absolute; inset: 0; z-index: 1; width: 100%; border: 0; border-radius: var(--r-card); background: transparent; cursor: pointer; transition: background-color var(--motion-fast) var(--ease); }
  .card-link:focus-visible { outline: 2px solid var(--iris); outline-offset: 2px; }
  .actions, .repos { position: relative; z-index: 2; }
  .identity-link { position: absolute; inset: 0; border: 0; border-radius: var(--r-xs); background: transparent; cursor: pointer; transition: background-color var(--motion-fast) var(--ease); }
  .identity-link:focus-visible, .segment-link:focus-visible { outline: 2px solid var(--iris); outline-offset: 2px; }
  .segment-link { min-width: 0; padding: 0; border: 0; background: transparent; text-align: left; cursor: pointer; }
  @media (hover: hover) {
    .card-link:hover, .identity-link:hover { background: var(--hover); }
    .segment-link:hover { filter: brightness(1.2); }
  }
  .card-link:active, .identity-link:active { background: var(--select); }
  .provider-dot { width: 8px; height: 8px; border-radius: 50%; flex: none; align-self: center; }
  .pools { container-type: inline-size; display: grid; gap: var(--s3); margin-top: var(--s1); }
  .pool { display: grid; grid-template-columns: 7.5rem minmax(0, 1fr); gap: var(--s2) var(--s4); align-items: center; }
  .headline { display: flex; flex-direction: column; gap: 2px; min-width: 0; }
  .headline strong { font-size: var(--t-stat); font-weight: var(--w-strong); line-height: var(--leading-tight); color: var(--ink); }
  .headline strong.low { color: var(--tone-warning-fg); }
  .none { font-size: var(--t-sm); color: var(--muted); }
  .pace { font-size: var(--t-cap); color: var(--muted); white-space: nowrap; }
  .pace.ahead { color: var(--tone-warning-fg); }
  .segments { display: grid; gap: var(--s1); min-width: 0; }
  @container (max-width: 30rem) {
    .pool { grid-template-columns: minmax(0, 1fr); }
    .headline { flex-direction: row; flex-wrap: wrap; align-items: baseline; gap: 0 var(--s2); }
  }
  .accounts { display: flex; flex-direction: column; margin-top: var(--s2); border-top: 1px solid var(--border-soft); }
  .accounts:first-child { margin-top: 0; border-top: 0; }
  .account { min-width: 0; padding: var(--s2) 0; }
  .account + .account { border-top: 1px solid var(--border-soft); }
  .row { display: flex; flex-wrap: wrap; align-items: center; gap: var(--s2) var(--s3); }
  .identity { position: relative; flex: 1 1 14rem; min-width: 0; display: flex; flex-wrap: wrap; align-items: center; gap: var(--s1) var(--s2); }
  b { flex-basis: 100%; font-size: var(--t-ui); font-weight: var(--w-medium); overflow-wrap: anywhere; }
  .facts { color: var(--muted); font-size: var(--t-sm); overflow-wrap: anywhere; }
  .actions { display: flex; flex-wrap: wrap; align-items: center; gap: var(--s2); }
  .repos-label { color: var(--muted); }
  .amounts { display: grid; gap: var(--s2); margin-top: var(--s2); }
  .note { margin: var(--s1) 0 0; color: var(--muted); font-size: var(--t-sm); line-height: var(--leading); }
  .chips { display: flex; flex-wrap: wrap; gap: var(--s1); margin-top: var(--s2); }
  .chip { background: var(--raise); border-radius: var(--r-badge); padding: 2px 7px; font-size: var(--t-cap); color: var(--ink-2); overflow-wrap: anywhere; }
  .repos { display: grid; gap: var(--s2); margin-top: 10px; padding: 10px; background: var(--main); border: 1px solid var(--line-soft); border-radius: var(--r-xs); }
  .repos label { display: grid; gap: var(--s1); }
  .repos textarea { background: var(--field); border: 1px solid var(--line); border-radius: var(--r-xs); padding: 5px 9px; font: inherit; font-size: var(--t-sm); color: var(--ink); resize: vertical; min-width: 0; }
  .repos .actions { margin-top: var(--s1); }
  .muted { color: var(--muted); }
  .small { font-size: var(--t-sm); }
  @media (pointer: coarse) { .actions .btn { min-height: 44px; } }
</style>
