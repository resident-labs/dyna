<script lang="ts">
  import { slide } from 'svelte/transition'
  import { BASE, motion } from '../../ui/motion.ts'
  import InlineConfirm from '../../ui/InlineConfirm.svelte'
  import SignInCard from './SignInCard.svelte'
  import { accountNeedsSignIn, accountScopeLabel, type HarnessRow, type LoginFlow } from './state.ts'
  import type { HarnessAccount, SignInState } from '../../api/harness.ts'

  // Accounts of the selected harness, with the sign-in card, the repo scopes
  // and the add-account form. The pane runs the sign-in flow and keeps the open
  // editors, so every change goes back through a callback.
  let {
    row,
    busy = false,
    signingIn = false,
    login = null,
    signInState = null,
    scanning = false,
    removing = $bindable(''),
    scoping = $bindable(''),
    scopeText = $bindable(''),
    showSignInForm = $bindable(false),
    labelInput = $bindable(''),
    onsignin,
    oncancel,
    onpaste,
    onrestart,
    onswitch,
    onscope,
    onsavescope,
    onremove,
  }: {
    row: HarnessRow
    busy?: boolean
    signingIn?: boolean
    login?: LoginFlow | null
    signInState?: SignInState | null
    scanning?: boolean
    removing?: string
    scoping?: string
    scopeText?: string
    showSignInForm?: boolean
    labelInput?: string
    onsignin: (add?: boolean) => void
    oncancel: () => void
    onpaste: (code: string) => boolean | void | Promise<boolean | void>
    onrestart: (account: HarnessAccount) => void
    onswitch: (account: HarnessAccount) => void
    onscope: (account: HarnessAccount) => void
    onsavescope: (accountId: string) => void
    onremove: (accountId: string) => void
  } = $props()
</script>

<div class="sec account">
  <h3 class="section-label">Accounts</h3>
  {#if login}
    <div class="login-box">
      <SignInCard signIn={signInState ?? { phase: 'starting' }} warning={login.warning} {busy} oncancel={oncancel} onpaste={onpaste} />
    </div>
  {:else if row.needsInstall || row.installing}
    <p class="muted small" style="margin:0">Install Dyna’s copy to sign in.{row.accounts.length ? ' Saved accounts stay.' : ''}</p>
  {:else if row.capability?.accountBlocked}
    <p class="muted small" style="margin:0">{row.capability.accountBlocked}</p>
  {:else if !row.accounts.length}
    <div class="signin-card">
      <p>Sign in to start using {row.meta.name}.</p>
      <p class="muted small">If you already use {row.meta.name}, Dyna can copy that login once.</p>
      <button class="btn pri" disabled={busy || signingIn} onclick={() => onsignin()}>{scanning ? 'Looking for your login…' : 'Sign in'}</button>
    </div>
  {:else}
    {#each row.accounts as acct (acct.id)}
      <div class="acct" transition:slide={{ duration: motion(BASE) }}>
        <span class="acct-name">
          <span class="dot" class:on={acct.active} aria-hidden="true"></span>
          <span class="label">{acct.label || row.meta.name}</span>
          {#if accountNeedsSignIn(acct)}<span class="muted small">Signed out</span>{:else if acct.active}<span class="in-use small">In use</span>{/if}
          <span class="scope small" class:limited={acct.repos?.length}>{accountScopeLabel(acct)}</span>
        </span>
        <span class="btn-group">
          {#if removing === acct.id}
            <InlineConfirm {busy} onconfirm={() => onremove(acct.id)} oncancel={() => (removing = '')} />
          {:else}
            {#if accountNeedsSignIn(acct)}
              <button class="btn pri" disabled={busy || signingIn} onclick={() => onrestart(acct)}>Sign in</button>
            {:else if !acct.active}
              <button class="btn quiet" disabled={busy || signingIn} onclick={() => onswitch(acct)}>Use</button>
            {/if}
            <button class="btn quiet" disabled={busy || signingIn} onclick={() => (scoping === acct.id ? (scoping = '') : onscope(acct))}>Repos</button>
            <button class="btn quiet" disabled={busy || signingIn} onclick={() => (removing = acct.id)}>Remove</button>
          {/if}
        </span>
      </div>
      {#if scoping === acct.id}
        <form class="signin-form" transition:slide={{ duration: motion(BASE) }} onsubmit={(e) => { e.preventDefault(); onsavescope(acct.id) }}>
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
      <form class="signin-form" transition:slide={{ duration: motion(BASE) }} onsubmit={(e) => { e.preventDefault(); onsignin(true) }}>
        <label>
          <span class="muted small">Account label (optional)</span>
          <input bind:value={labelInput} placeholder="e.g. Work" disabled={busy || signingIn} />
        </label>
        <div class="btn-group">
          <button type="submit" class="btn" disabled={busy || signingIn}>Start sign-in</button>
          <button type="button" class="btn quiet" onclick={() => (showSignInForm = false)}>Cancel</button>
        </div>
      </form>
    {:else}
      <button class="btn quiet add" disabled={busy || signingIn} onclick={() => (showSignInForm = true)}><span aria-hidden="true">+</span>Add account</button>
    {/if}
  {/if}
</div>

<style>
  .btn-group { display: flex; flex-wrap: wrap; align-items: center; gap: var(--s2); }
  .muted { color: var(--muted); }
  .small { font-size: var(--t-sm); }
  .sec { border-top: 1px solid var(--line); padding: var(--s4) 0; }
  .sec .section-label { margin-top: 0; }
  .login-box { margin-top: var(--s1); }
  .signin-card { display: grid; gap: 6px; justify-items: start; padding: 14px; background: var(--main); border: 1px solid var(--line-soft); border-radius: 8px; }
  .signin-card p { margin: 0; font-size: var(--t-ui); }
  .signin-card .btn { margin-top: 6px; }
  .signin-form input { background: var(--field); border: 1px solid var(--line); border-radius: 6px; padding: 5px 9px; font-size: var(--t-sm); color: var(--ink); min-width: 0; flex: 1; }
  .signin-form { display: grid; gap: var(--s2); margin-top: 10px; padding: 10px; background: var(--main); border: 1px solid var(--line-soft); border-radius: 6px; }
  .signin-form label { display: grid; gap: var(--s1); }
  .signin-form textarea { background: var(--field); border: 1px solid var(--line); border-radius: 6px; padding: 5px 9px; font: inherit; font-size: var(--t-sm); color: var(--ink); resize: vertical; min-width: 0; }
  .acct { display: flex; flex-wrap: wrap; align-items: center; justify-content: space-between; gap: var(--s1) var(--s3); min-height: 38px; font-size: var(--t-ui); }
  .acct-name { display: flex; flex: 1 1 15rem; flex-wrap: wrap; align-items: center; gap: var(--s1) var(--s2); min-width: 0; }
  .acct .btn-group { flex-wrap: nowrap; margin-left: auto; justify-content: flex-end; }
  /* One fixed column per action, so the buttons of every account line up
     under each other even when a row has no Use button. */
  .acct .btn-group .btn { min-width: 4.4rem; padding: 0 10px; }
  .acct-name .label { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .acct .btn.quiet { border-color: transparent; color: var(--muted); }
  .dot { width: 6px; height: 6px; flex: none; border-radius: 50%; border: 1px solid var(--line); }
  .dot.on { background: var(--sage); border-color: var(--sage); }
  .in-use { color: var(--sage); }
  .scope { color: var(--muted); white-space: nowrap; }
  .scope.limited { color: var(--ink); }
  .btn.add { margin-top: var(--s1); padding-left: 0; border-color: transparent; color: var(--muted); }
  .btn.add span { margin-right: 6px; }
</style>
