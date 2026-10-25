<script lang="ts">
  import { onMount, onDestroy } from 'svelte'
  import { errorText } from '../../errorText.ts'
  import { api } from '../../../api.ts'
  import { dialog } from '../../ui/dialog.ts'
  import SignInCard from './SignInCard.svelte'
  import { PROVIDERS, accountLabel, providerName } from './state.ts'
  import { piSignInID, recoveredLogins, type LoginFlow } from './login.ts'
  import type { AccountsSnapshot, Provider } from '../../api/account.ts'
  import type { SignInState } from '../../api/harness.ts'

  // The add form: pick a provider, then sign in or paste an API key. A sign-in
  // runs through Pi's provider flow, which saves one account for each provider
  // the person signs in to. handleOpen starts a saved sign-in again.
  interface Props {
    scope: string
    account?: { id: string; label: string } | null
    onclose: () => void
    onsaved: (snapshot: AccountsSnapshot) => void
  }
  let { scope, account = null, onclose, onsaved }: Props = $props()
  let provider = $state<Provider>('anthropic')
  let label = $state('')
  let key = $state('')
  let keyForm = $state(false)
  let busy = $state(false)
  let error = $state('')
  let flow = $state<LoginFlow | null>(null)
  let signIn = $state<SignInState | null>(null)
  let first: HTMLElement | undefined = $state()
  let stopped = false

  $effect(() => {
    const current = flow
    if (!current) return
    let live = true
    let finishing = false
    const poll = async () => {
      try {
        const next = await api.privateRuntimeLoginState(current.accountId)
        if (!live) return
        signIn = next
        if (next.phase === 'succeeded' && !finishing) {
          finishing = true
          await finish(current.accountId)
          finishing = false
        }
      } catch { /* the next poll repeats a transient read failure */ }
    }
    void poll()
    const timer = setInterval(poll, 2000)
    return () => { live = false; clearInterval(timer) }
  })

  onMount(async () => {
    // A sign-in that the owner started earlier stays reachable.
    try {
      const flows = recoveredLogins(await api.privateRuntimeLogins(), null).filter((f) => f.harness === 'pi')
      const saved = flows.find((f) => f.accountId === piSignInID) ?? flows[0]
      if (!stopped && saved) { flow = saved; signIn = saved.signIn ?? { phase: 'starting' } }
    } catch { /* no saved sign-in: the form starts a new one */ }
  })
  onDestroy(() => { stopped = true })

  async function reload(next?: AccountsSnapshot) {
    const snapshot = next ?? (await api.accountsList(scope, false))
    if (!stopped) onsaved(snapshot)
  }

  async function startRestart() {
    if (!account || busy) return
    busy = true; error = ''
    try {
      const result = await api.restartPrivateRuntimeLogin(account.id, false)
      if (!stopped) { flow = { ...result, harness: result.harness ?? 'pi' }; signIn = result.signIn ?? { phase: 'starting' } }
    } catch (e) { if (!stopped) error = errorText(e) || 'Could not start the sign-in.' }
    finally { busy = false }
  }

  async function startSignIn() {
    if (busy) return
    busy = true; error = ''
    try {
      // Pi's provider sign-in keeps its own home, so it changes no other login.
      const result = await api.beginPrivateRuntimeLogin('pi', '', '', false)
      if (!stopped) { flow = { ...result, harness: 'pi' }; signIn = result.signIn ?? { phase: 'starting' } }
    } catch (e) { if (!stopped) error = errorText(e) || 'Could not start the sign-in.' }
    finally { busy = false }
  }

  async function finish(accountId: string) {
    try {
      await api.finishPrivateRuntimeLogin(accountId)
      flow = null; signIn = null
      if (!stopped) await reload()
    } catch (e) { if (!stopped) error = errorText(e) || 'The sign-in is not complete yet.'; setPhaseFailed() }
  }
  function setPhaseFailed() {
    // Keep the card up so the owner can finish or cancel the step.
    signIn = { ...(signIn ?? { phase: 'failed' }), phase: 'failed' }
  }

  async function respondPaste(code: string) {
    if (!flow || busy) return false
    busy = true; error = ''
    try { await api.respondPrivateRuntimeLogin(flow.accountId, code); return true }
    catch (e) { if (!stopped) error = errorText(e) || 'Could not send the code.'; return false }
    finally { busy = false }
  }

  async function cancelSignIn() {
    const current = flow
    if (!current) return
    busy = true
    try { await api.cancelPrivateRuntimeLogin(current.accountId) } catch { /* the flow ends anyway */ }
    finally { if (!stopped) { busy = false; flow = null; signIn = null } }
  }

  async function saveKey() {
    if (busy || !key) return
    busy = true; error = ''
    try {
      const next = await api.accountsAdd(provider, label.trim(), key)
      key = ''
      if (!stopped) { onsaved(next) }
    } catch (e) { if (!stopped) error = errorText(e) || 'Could not save the key.' }
    finally { busy = false }
  }
</script>

<!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
<div class="backdrop">
  <!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
  <form class="card" use:dialog={{ close: onclose, busy, initial: () => first }} role="dialog" tabindex="-1" aria-modal="true" aria-label={account ? `Sign in again to ${account.label}` : 'Add account'} onclick={(e) => e.stopPropagation()} onsubmit={(e) => { e.preventDefault(); if (keyForm) void saveKey(); else void startRestart() }}>
    <h2>{account ? `Sign in again to ${account.label}` : 'Add account'}</h2>

    {#if flow}
      <p class="muted small">{account ? 'Complete the sign-in steps for this account.' : 'Sign in to a provider. Dyna saves one account for each provider you sign in to.'}</p>
      <SignInCard signIn={signIn ?? { phase: 'starting' }} {busy} oncancel={() => void cancelSignIn()} onpaste={respondPaste} />
    {:else if account}
      <p class="muted small">Dyna opens the sign-in steps for {account.label}.</p>
      <div class="row">
        <button type="button" class="btn quiet" onclick={onclose} disabled={busy}>Cancel</button>
        <button type="submit" class="btn" disabled={busy}>{busy ? 'Starting…' : 'Start sign-in'}</button>
      </div>
    {:else}
      <label>
        <span class="muted small">Provider</span>
        <select class="field" bind:this={first} bind:value={provider} disabled={busy} aria-label="Provider">
          {#each PROVIDERS as id (id)}<option value={id}>{providerName(id)}</option>{/each}
        </select>
      </label>
      <label>
        <span class="muted small">Label (optional)</span>
        <input class="field" bind:value={label} placeholder="e.g. Work" autocomplete="off" disabled={busy} aria-label="Account label" />
      </label>
      {#if keyForm}
        <label>
          <span class="muted small">API key for {providerName(provider)}</span>
          <input class="field mono" type="password" bind:value={key} autocomplete="off" spellcheck="false" disabled={busy} aria-label="API key" />
        </label>
        <p class="muted small">Dyna saves the key in the account's own home. It never shows the key again.</p>
        <div class="row">
          <button type="button" class="btn quiet" onclick={() => { key = ''; keyForm = false }} disabled={busy}>Back</button>
          <button type="submit" class="btn" disabled={busy || !key}>{busy ? 'Saving…' : 'Save key'}</button>
        </div>
      {:else}
        <div class="row">
          <button type="button" class="btn quiet" onclick={onclose} disabled={busy}>Cancel</button>
          <button type="button" class="btn quiet" onclick={() => (keyForm = true)} disabled={busy}>Use an API key</button>
          <button type="submit" class="btn" disabled={busy}>{busy ? 'Starting…' : 'Sign in'}</button>
        </div>
      {/if}
    {/if}
    {#if error}<p class="err" role="alert">{error}</p>{/if}
  </form>
</div>

<style>
  .backdrop { position: fixed; inset: 0; background: rgba(8, 8, 10, 0.62); display: grid; place-items: center; z-index: 60; padding: var(--s6); }
  .card { width: min(440px, 100%); max-height: 92dvh; overflow-y: auto; background: var(--raise); border: 1px solid var(--hair-strong); border-radius: var(--r-lg); box-shadow: 0 24px 64px rgba(0, 0, 0, 0.5); padding: var(--s5); }
  h2 { margin: 0 0 var(--s3); font-size: var(--t-title); font-weight: 600; }
  label { display: grid; gap: var(--s1); margin-bottom: var(--s3); }
  .field { background: var(--field); border: 1px solid var(--line); border-radius: var(--r-xs); padding: 7px 10px; font-size: var(--t-ui); color: var(--ink); min-height: 36px; }
  .field[type='password'] { font-family: var(--mono); }
  .muted { color: var(--muted); }
  .small { font-size: var(--t-sm); }
  p { margin: 0 0 var(--s2); }
  .err { color: var(--coral); }
  .row { display: flex; flex-wrap: wrap; justify-content: flex-end; gap: var(--s2); margin-top: var(--s4); }
  @media (max-width: 600px) {
    .backdrop { padding: var(--s3); align-items: end; }
    .card { padding: var(--s4); }
    .row .btn, .field { min-height: 44px; }
  }
</style>
