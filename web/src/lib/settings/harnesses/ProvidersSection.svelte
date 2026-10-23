<script lang="ts">
  import { slide } from 'svelte/transition'
  import { BASE, motion } from '../../ui/motion.ts'
  import InlineConfirm from '../../ui/InlineConfirm.svelte'
  import type { ProviderKey, SignInState } from '../../api/harness.ts'
  import SignInCard from './SignInCard.svelte'
  import { hasSubscriptionLogin, type HarnessRow, type LoginFlow } from './state.ts'

  // The providers of the selected harness, each signed in by API key or, for
  // Pi, by a subscription sign-in. The pane reads the list and runs the add,
  // sign-in and remove calls, so this only shows what it is given.
  let {
    row,
    providers = [],
    busy = false,
    signingIn = false,
    login = null,
    signInState = null,
    removingProvider = $bindable(''),
    onremove,
    onadd,
    onsignin,
    oncancel,
    onpaste,
  }: {
    row: HarnessRow
    providers?: ProviderKey[]
    busy?: boolean
    signingIn?: boolean
    login?: LoginFlow | null
    signInState?: SignInState | null
    removingProvider?: string
    onremove: (providerId: string) => void
    onadd: () => void
    onsignin: () => void
    oncancel: () => void
    onpaste: (code: string) => boolean | void | Promise<boolean | void>
  } = $props()
  // Pi signs in to any provider in one login; other harnesses sign in per account.
  const signsInByProvider = $derived(row.id === 'pi')
</script>

<div class="sec">
  <h3 class="section-label">{signsInByProvider ? 'Providers' : 'API-key providers'}</h3>
  {#if login}
    <div class="login-box">
      <SignInCard signIn={signInState ?? { phase: 'starting' }} warning={login.warning} {busy} {oncancel} {onpaste} />
    </div>
  {/if}
  {#if !hasSubscriptionLogin(row.id)}<p class="muted small" style="margin:0 0 var(--s2)">{row.meta.name} has no subscription login. Add a provider with an API key.</p>{/if}
  {#each providers as p (p.id)}
    <div class="acct" transition:slide={{ duration: motion(BASE) }}>
      <span class="acct-name"><span class="dot on" aria-hidden="true"></span><span class="label">{p.name}</span><span class="muted small">{p.auth === 'oauth' ? 'Signed in' : 'API key'}</span></span>
      <span class="btn-group">
        {#if removingProvider === p.id}
          <InlineConfirm {busy} onconfirm={() => onremove(p.id)} oncancel={() => (removingProvider = '')} />
        {:else}
          <button class="btn quiet" disabled={busy} onclick={() => (removingProvider = p.id)}>Remove</button>
        {/if}
      </span>
    </div>
  {/each}
  <div class="btn-group adds">
    <button class="btn quiet add" disabled={busy || signingIn} onclick={onadd}><span aria-hidden="true">+</span>{signsInByProvider ? 'Add API key' : 'Add provider'}</button>
    {#if signsInByProvider && !row.capability?.accountBlocked}
      <button class="btn quiet add" disabled={busy || signingIn} onclick={onsignin}><span aria-hidden="true">+</span>Sign in to a provider</button>
    {/if}
  </div>
</div>

<style>
  .btn-group { display: flex; flex-wrap: wrap; align-items: center; gap: var(--s2); }
  .muted { color: var(--muted); }
  .small { font-size: var(--t-sm); }
  .sec { border-top: 1px solid var(--line); padding: var(--s4) 0; }
  .sec .section-label { margin-top: 0; }
  .acct { display: flex; flex-wrap: wrap; align-items: center; justify-content: space-between; gap: var(--s1) var(--s3); min-height: 38px; font-size: var(--t-ui); }
  .acct-name { display: flex; flex: 1 1 15rem; flex-wrap: wrap; align-items: center; gap: var(--s1) var(--s2); min-width: 0; }
  .acct .btn-group { flex-wrap: nowrap; margin-left: auto; justify-content: flex-end; }
  .acct .btn-group .btn { min-width: 4.4rem; padding: 0 10px; }
  .acct-name .label { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .dot { width: 6px; height: 6px; flex: none; border-radius: 50%; border: 1px solid var(--line); }
  .dot.on { background: var(--sage); border-color: var(--sage); }
  .login-box { margin-bottom: var(--s2); }
  .adds { margin-top: var(--s1); }
  .btn.add { margin-top: 0; padding-left: 0; border-color: transparent; color: var(--muted); }
  .btn.add span { margin-right: 6px; }
</style>
