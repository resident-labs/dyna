<script lang="ts">
  import Card from '../../ui/Card.svelte'
  import InlineConfirm from '../../ui/InlineConfirm.svelte'
  import UsageMeter from './UsageMeter.svelte'
  import { accountKindLabel, accountLabel, accountStatus, needsSignIn, observation, parseRepoList, providerName, repoChips, reposLabel, remainingPercent, selectionText, usageUnavailableText } from './state.ts'
  import { blockUntil } from './reset.ts'
  import { deadlineClock } from './clock.svelte.ts'
  import type { ProviderAccounts, ScopeAccount } from '../../api/account.ts'

  // One provider and every account saved for it. The pane reads and writes the
  // accounts, so this only shows them and reports the button the owner pressed.
  interface Props {
    group: ProviderAccounts
    compact?: boolean
    roomScope?: boolean
    busy?: boolean
    onopen?: ((account: ScopeAccount, event: MouseEvent) => void) | null
    onuse?: ((account: ScopeAccount) => void) | null
    onsignin?: ((account: ScopeAccount) => void) | null
    onsaverepos?: ((account: ScopeAccount, repos: string[]) => Promise<boolean>) | null
    onremove?: ((account: ScopeAccount) => void) | null
  }
  let { group, compact = false, roomScope = false, busy = false, onopen = null, onuse = null, onsignin = null, onsaverepos = null, onremove = null }: Props = $props()
  // One wake for the card: it starts at the first account block that ends, and
  // the clock asks for the next deadline after each wake.
  const now = deadlineClock(() => {
    const deadlines = (group.accounts ?? []).map((account) => blockUntil(account)).filter((at) => Number.isFinite(at))
    return deadlines.length ? Math.min(...deadlines) : Number.NaN
  })
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

<Card title={providerName(group.provider)}>
  {#each group.accounts ?? [] as account (account.id)}
    {@const status = [accountStatus(account, '', now()), accountKindLabel(account.kind)].filter(Boolean).join(' · ')}
    <div class="account" class:compact>
      <div class="heading">
        <div class="identity">
          <b>{accountLabel(account)}</b>
          {#if status}<p class="status">{status}</p>{/if}
        </div>
        {#if account.activeHere}<span class="in-use">{selectionText(account, roomScope)}</span>
        {:else if roomScope && account.active}<span class="inherited">{selectionText(account, roomScope)}</span>{/if}
      </div>
      {#if account.windows?.length}
        <div class="windows">{#each account.windows as window (window.id || window.label)}<UsageMeter {window} observedAt={account.observedAt} compact={compact && remainingPercent(window) !== null} />{/each}</div>
      {:else}<p class="note">{usageUnavailableText(account)}</p>{/if}
      {#if repoChips(account).length}<div class="chips">{#each repoChips(account) as repo (repo)}<span class="chip">{repo}</span>{/each}</div>{/if}
      {#if !compact && observation(account)}<p class="note">{observation(account)}</p>{/if}
      {#if compact && onopen}<div class="actions"><button class="btn quiet" onclick={(event) => onopen?.(account, event)}>Details</button></div>{/if}
      {#if !compact}
        <div class="actions">
          {#if needsSignIn(account) && onsignin}<button class="btn pri" disabled={busy} onclick={() => onsignin?.(account)}>Sign in</button>{/if}
          {#if !account.activeHere && onuse}<button class="btn" disabled={busy} onclick={() => onuse?.(account)}>Use</button>{/if}
          {#if onopen}<button class="btn quiet" onclick={(event) => onopen?.(account, event)}>Details</button>{/if}
          {#if onsaverepos}<button class="btn quiet" disabled={busy} aria-expanded={scoping === account.id} onclick={() => editRepos(account)}>Repos<span class="repos-label"> · {reposLabel(account)}</span></button>{/if}
          {#if onremove}
            {#if scoping === 'remove:' + account.id}
              <InlineConfirm ask="Remove this account?" {busy} onconfirm={() => onremove?.(account)} oncancel={() => (scoping = '')} />
            {:else}
              <button class="btn quiet" disabled={busy} onclick={() => (scoping = 'remove:' + account.id)}>Remove</button>
            {/if}
          {/if}
        </div>
        {#if scoping === account.id && onsaverepos}
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
      {/if}
    </div>
  {/each}
</Card>

<style>
  .account { min-width: 0; padding-top: var(--s2); }
  .account + .account { border-top: 1px solid var(--line); margin-top: var(--s2); }
  .heading { display: flex; align-items: center; gap: var(--s2); flex-wrap: wrap; }
  .identity { flex: 1; min-width: 0; }
  b { font-size: var(--t-body); font-weight: 500; overflow-wrap: anywhere; }
  .status { margin: var(--s1) 0 0; color: var(--muted); font-size: var(--t-sm); overflow-wrap: anywhere; }
  .in-use { background: var(--sage); color: var(--main); border-radius: 4px; padding: 2px 8px; font-size: var(--t-cap); font-weight: 600; white-space: nowrap; }
  .inherited { color: var(--muted); font-size: var(--t-cap); white-space: nowrap; }
  .actions { display: flex; flex-wrap: wrap; align-items: center; gap: var(--s2); margin-top: var(--s3); }
  .repos-label { color: var(--muted); }
  .windows { display: grid; gap: var(--s3); margin-top: var(--s3); }
  .note { margin: var(--s2) 0 0; color: var(--muted); font-size: var(--t-sm); line-height: 1.7; }
  .chips { display: flex; flex-wrap: wrap; gap: var(--s1); margin-top: var(--s2); }
  .chip { background: var(--raise); border-radius: 4px; padding: 2px 7px; font-size: var(--t-cap); color: var(--ink-2); overflow-wrap: anywhere; }
  .repos { display: grid; gap: var(--s2); margin-top: 10px; padding: 10px; background: var(--main); border: 1px solid var(--line-soft); border-radius: 6px; }
  .repos label { display: grid; gap: var(--s1); }
  .repos textarea { background: var(--field); border: 1px solid var(--line); border-radius: 6px; padding: 5px 9px; font: inherit; font-size: var(--t-sm); color: var(--ink); resize: vertical; min-width: 0; }
  .muted { color: var(--muted); }
  .small { font-size: var(--t-sm); }
  .compact .windows { gap: var(--s2); margin-top: var(--s2); }
  @media (pointer: coarse) { .actions .btn { min-height: 44px; } }
</style>
