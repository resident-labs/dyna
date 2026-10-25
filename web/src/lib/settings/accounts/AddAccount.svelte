<script lang="ts">
  import { onMount, onDestroy, untrack } from 'svelte'
  import { errorText } from '../../errorText.ts'
  import { api } from '../../../api.ts'
  import { dialog } from '../../ui/dialog.ts'
  import SignInCard from './SignInCard.svelte'
  import { PI_KEY_PROVIDERS, PI_OAUTH_PROVIDERS, providerName } from './state.ts'
  import { harnessLabel, harnessPin, NATIVE_HARNESSES, openCodeKey, runningFlow, sharedLogin } from './signin.ts'
  import type { AccountsSnapshot, Provider } from '../../api/account.ts'
  import type { HarnessSnapshot, PersonalLogin, PrivateLogin, SignInState } from '../../api/harness.ts'

  // The add form: pick a provider, then sign in through its native harness or
  // through Pi, or paste an API key. A provider below Pi is still configured
  // through the harness that serves it, so both sign-in paths stay here until
  // credentials are shared. Handing in an account signs that account in again.
  interface Props {
    scope: string
    account?: { id: string; label: string; provider?: Provider; harness?: string } | null
    onclose: () => void
    onsaved: (snapshot: AccountsSnapshot) => void
  }
  let { scope, account = null, onclose, onsaved }: Props = $props()
  type HarnessChoice = (typeof NATIVE_HARNESSES)[number] | 'pi'
  type PiMethod = 'oauth' | 'key'
  let provider = $state<Provider>(untrack(() => account?.provider ?? 'anthropic'))
  let harnessChoice = $state<HarnessChoice | ''>(untrack(() => account ? (account.harness === 'pi' ? 'pi' : 'claude-code') : ''))
  let piMethod = $state<PiMethod>('oauth')
  let keyWhere = $state<'pi' | 'opencode'>('pi')
  let label = $state('')
  let key = $state('')
  let busy = $state(false)
  let error = $state('')
  let flow = $state<PrivateLogin | null>(null)
  let signIn = $state<SignInState | null>(null)
  let runtimes = $state<HarnessSnapshot | null>(null)
  let personal = $state<PersonalLogin | null>(null)
  let personalPin = $state('')
  let confirmAsk = $state<{ title: string; lines: string[]; action: string; run: () => void } | null>(null)
  let first: HTMLElement | undefined = $state()
  let stopped = false

  const piProviders = $derived(piMethod === 'oauth' ? PI_OAUTH_PROVIDERS : PI_KEY_PROVIDERS)
  const openCode = $derived(openCodeKey(provider))
  $effect(() => {
    const fallback = piProviders[0]
    if (fallback && !piProviders.includes(provider)) provider = fallback
  })
  $effect(() => {
    if (!openCode) keyWhere = 'pi'
  })
  const canSubmit = $derived(account !== null || (harnessChoice !== '' && harnessChoice !== 'pi') || (harnessChoice === 'pi' && (piMethod === 'oauth' || key.length > 0)))
  const title = $derived(account ? `Sign in again to ${account.label}` : 'Add account')

  // The pane polls the native sign-in step and collects the finished account.
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
    try {
      const [snapshot, flows] = await Promise.all([api.privateRuntimes(), api.privateRuntimeLogins()])
      if (stopped) return
      runtimes = snapshot
      const saved = runningFlow(flows, account)
      if (saved) { flow = saved; signIn = saved.signIn ?? { phase: 'starting' } }
    } catch { /* no saved sign-in and no snapshot: the form starts a new one */ }
  })
  onDestroy(() => { stopped = true })

  async function reload(next?: AccountsSnapshot) {
    const snapshot = next ?? (await api.accountsView(scope))
    if (!stopped) onsaved(snapshot)
  }

  async function snapshotOfHarness(): Promise<HarnessSnapshot | null> {
    if (runtimes) return runtimes
    try {
      runtimes = await api.privateRuntimes()
    } catch { return null }
    return runtimes
  }

  // submit is the form's one action, so a new account signs in, saves a key, or
  // starts the native path, and an existing account signs in again.
  async function submit() {
    if (busy || flow || personal) return
    if (account) return startRestart()
    if (harnessChoice !== '' && harnessChoice !== 'pi') return startNative()
    if (harnessChoice === 'pi') return piMethod === 'key' ? saveKey() : startPi()
  }

  async function startRestart() {
    if (!account || busy) return
    const snapshot = await snapshotOfHarness()
    if (sharedLogin(snapshot, account.harness ?? '') && !confirmAsk) {
      confirmAsk = {
        title: `Sign in to ${account.label} again?`,
        lines: [`Signing in also changes the login of your own ${harnessLabel(account.harness ?? '')} CLI.`],
        action: 'Continue sign-in',
        run: () => void performRestart(true),
      }
      return
    }
    await performRestart(false)
  }

  async function performRestart(confirmed: boolean) {
    if (!account || busy) return
    busy = true; error = ''
    try {
      const result = await api.restartPrivateRuntimeLogin(account.id, confirmed)
      if (!stopped) { flow = result; signIn = result.signIn ?? { phase: 'starting' }; confirmAsk = null }
    } catch (e) { if (!stopped) error = errorText(e) || 'Could not start the sign-in.' }
    finally { if (!stopped) busy = false }
  }

  // startNative offers the owner's own saved login first, as the harness page
  // did, then starts the native sign-in.
  async function startNative() {
    if (!harnessChoice || harnessChoice === 'pi' || busy) return
    const harness = harnessChoice
    busy = true; error = ''
    try {
      const snapshot = await snapshotOfHarness()
      const pin = harnessPin(snapshot, harness)
      if (!pin) throw new Error(`Install ${harnessLabel(harness)} in Harnesses before signing in.`)
      const found = await api.findPersonalLogin(harness, pin)
      if (!stopped && found) { personal = found; personalPin = pin; return }
      await beginNative(harness, pin)
    } catch (e) { if (!stopped) error = errorText(e) || 'Could not start the sign-in.' }
    finally { if (!stopped) busy = false }
  }

  async function beginNative(harness: string, pin: string) {
    if (sharedLogin(runtimes, harness)) {
      confirmAsk = {
        title: `Sign in to ${harnessLabel(harness)}?`,
        lines: [`Signing in to Dyna's ${harnessLabel(harness)} account also changes the login of your own ${harnessLabel(harness)} CLI.`],
        action: 'Continue sign-in',
        run: () => void performBegin(harness, pin, true),
      }
      return
    }
    await performBegin(harness, pin, false)
  }

  async function performBegin(harness: string, pin: string, confirmed: boolean) {
    busy = true; error = ''
    try {
      const result = await api.beginPrivateRuntimeLogin(harness, pin, label.trim(), '', confirmed)
      if (!stopped) { flow = { ...result, harness }; signIn = result.signIn ?? { phase: 'starting' }; confirmAsk = null; personal = null }
    } catch (e) { if (!stopped) error = errorText(e) || 'Could not start the sign-in.' }
    finally { if (!stopped) busy = false }
  }

  // startPi runs the exact Pi OAuth flow the owner chose, so Pi does not ask
  // for the provider a second time.
  async function startPi() {
    if (busy || !provider) return
    busy = true; error = ''
    try {
      const result = await api.beginPrivateRuntimeLogin('pi', '', '', provider, false)
      if (!stopped) { flow = { ...result, harness: 'pi' }; signIn = result.signIn ?? { phase: 'starting' } }
    } catch (e) { if (!stopped) error = errorText(e) || 'Could not start the sign-in.' }
    finally { if (!stopped) busy = false }
  }

  async function usePersonal() {
    if (!personal || busy) return
    busy = true; error = ''
    try {
      await api.usePersonalLogin(personal.harness)
      personal = null
      if (!stopped) await reload()
    } catch (e) { if (!stopped) error = errorText(e) || 'Could not use your saved login.' }
    finally { if (!stopped) busy = false }
  }

  async function saveKey() {
    if (busy || !key) return
    busy = true; error = ''
    try {
      if (keyWhere === 'opencode') {
        if (!openCode) throw new Error(`${providerName(provider)} has no API key in OpenCode.`)
        const next = await api.accountsAdd('opencode', provider, label.trim(), key)
        key = ''
        if (!stopped) onsaved(next)
        return
      }
      const next = await api.accountsAdd('pi', provider, label.trim(), key)
      key = ''
      if (!stopped) onsaved(next)
    } catch (e) { if (!stopped) error = errorText(e) || 'Could not save the key.' }
    finally { if (!stopped) busy = false }
  }

  async function finish(accountId: string) {
    try {
      await api.finishPrivateRuntimeLogin(accountId)
      flow = null; signIn = null
      if (!stopped) await reload()
    } catch (e) {
      if (!stopped) {
        error = errorText(e) || 'The sign-in is not complete yet.'
        signIn = { ...(signIn ?? { phase: 'failed' }), phase: 'failed' }
      }
    }
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
</script>

<!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
<div class="backdrop">
  <!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
  <form class="card" use:dialog={{ close: onclose, busy, initial: () => first }} role="dialog" tabindex="-1" aria-modal="true" aria-label={title} onclick={(e) => e.stopPropagation()} onsubmit={(e) => { e.preventDefault(); void submit() }}>
    <h2>{title}</h2>

    {#if flow}
      <p class="muted small">{account ? 'Complete the sign-in steps for this account.' : 'Complete the sign-in steps below. Dyna saves the account when they finish.'}</p>
      <SignInCard signIn={signIn ?? { phase: 'starting' }} warning={flow.warning} {busy} oncancel={() => void cancelSignIn()} onpaste={respondPaste} />
    {:else if personal}
      {@const found = personal}
      <p>Dyna found your personal {harnessLabel(found.harness)} login {found.label}.</p>
      {#if found.saved}
        <p class="muted small">Your personal account {found.label} is already saved in Dyna.</p>
        <div class="row">
          <button type="button" class="btn quiet" onclick={onclose} disabled={busy}>Cancel</button>
          <button type="button" class="btn" disabled={busy} onclick={() => void performBegin(found.harness, personalPin, false)}>Sign in with another account</button>
        </div>
      {:else}
        <p class="muted small">Use it, and Dyna copies the login once. Or sign in with another account.</p>
        <div class="row">
          <button type="button" class="btn quiet" onclick={onclose} disabled={busy}>Cancel</button>
          <button type="button" class="btn quiet" disabled={busy} onclick={() => void performBegin(found.harness, personalPin, false)}>Sign in instead</button>
          <button type="button" class="btn" disabled={busy} onclick={() => void usePersonal()}>{busy ? 'Copying…' : 'Use this account'}</button>
        </div>
      {/if}
    {:else if confirmAsk}
      {@const ask = confirmAsk}
      <p class="muted small">{ask.lines[0]}</p>
      <div class="row">
        <button type="button" class="btn quiet" onclick={() => (confirmAsk = null)} disabled={busy}>Cancel</button>
        <button type="button" class="btn" disabled={busy} onclick={() => ask.run()}>{ask.action}</button>
      </div>
    {:else if account}
      <p class="muted small">Dyna opens the sign-in steps for {account.label}.</p>
      <div class="row">
        <button type="button" class="btn quiet" onclick={onclose} disabled={busy}>Cancel</button>
        <button type="submit" class="btn" disabled={busy}>{busy ? 'Starting…' : 'Start sign-in'}</button>
      </div>
    {:else}
      <label>
        <span class="muted small">Step 1 · Harness</span>
        <select class="field" bind:this={first} bind:value={harnessChoice} disabled={busy} aria-label="Harness">
          <option value="">Choose a harness</option>
          {#each NATIVE_HARNESSES as h (h)}<option value={h}>{harnessLabel(h)}</option>{/each}
          <option value="pi">Pi</option>
        </select>
      </label>
      {#if harnessChoice && harnessChoice !== 'pi'}
        <label>
          <span class="muted small">Label (optional)</span>
          <input class="field" bind:value={label} placeholder="e.g. Work" autocomplete="off" disabled={busy} aria-label="Account label" />
        </label>
        <p class="muted small">Dyna uses its own copy of {harnessLabel(harnessChoice)}. If it finds your saved login, it offers to copy it once.</p>
      {:else if harnessChoice === 'pi'}
        <fieldset class="how">
          <legend class="muted small">Step 2 · Sign-in method</legend>
          <label class="choice"><input type="radio" name="pi-method" value="oauth" bind:group={piMethod} disabled={busy} /><span>Sign in with the provider</span></label>
          <label class="choice"><input type="radio" name="pi-method" value="key" bind:group={piMethod} disabled={busy} /><span>API key</span></label>
        </fieldset>
        <label>
          <span class="muted small">Step 3 · Provider</span>
          <select class="field" bind:value={provider} disabled={busy} aria-label="Provider">
            {#each piProviders as id (id)}<option value={id}>{providerName(id)}</option>{/each}
          </select>
        </label>
        {#if piMethod === 'key'}
          {#if openCode}
            <label>
              <span class="muted small">Save the key in</span>
              <select class="field" bind:value={keyWhere} disabled={busy} aria-label="Save the key in">
                <option value="pi">Pi</option>
                <option value="opencode">OpenCode</option>
              </select>
            </label>
          {/if}
          <label>
            <span class="muted small">Label (optional)</span>
            <input class="field" bind:value={label} placeholder="e.g. Work" autocomplete="off" disabled={busy} aria-label="Account label" />
          </label>
          <label>
            <span class="muted small">API key for {providerName(provider)}</span>
            <input class="field mono" type="password" bind:value={key} autocomplete="off" spellcheck="false" disabled={busy} aria-label="API key" />
          </label>
          <p class="muted small">{keyWhere === 'opencode' ? 'Dyna saves the key in OpenCode’s private home.' : 'Dyna saves the key in the account’s own Pi home.'} It never shows the key again.</p>
        {:else}
          <p class="muted small">Dyna starts {providerName(provider)}’s Pi OAuth sign-in directly. Pi asks only for the approval it needs.</p>
        {/if}
      {/if}
      <div class="row">
        <button type="button" class="btn quiet" onclick={onclose} disabled={busy}>Cancel</button>
        <button type="submit" class="btn" disabled={busy || !canSubmit}>{busy ? 'Working…' : harnessChoice === 'pi' && piMethod === 'key' ? 'Save key' : 'Sign in'}</button>
      </div>
    {/if}
    {#if error}<p class="err" role="alert">{error}</p>{/if}
  </form>
</div>

<style>
  .backdrop { position: fixed; inset: 0; background: rgba(8, 8, 10, 0.62); display: grid; place-items: center; z-index: 60; padding: var(--s6); }
  .card { width: min(440px, 100%); max-height: 92dvh; overflow-y: auto; background: var(--raise); border: 1px solid var(--hair-strong); border-radius: var(--r-lg); box-shadow: 0 24px 64px rgba(0, 0, 0, 0.5); padding: var(--s5); }
  h2 { margin: 0 0 var(--s3); font-size: var(--t-title); font-weight: 600; }
  label { display: grid; gap: var(--s1); margin-bottom: var(--s3); }
  .how { display: grid; gap: var(--s1); border: 0; margin: 0 0 var(--s3); padding: 0; }
  .choice { display: flex; align-items: center; gap: var(--s2); margin: 0; min-height: 32px; }
  .choice input { inline-size: 16px; block-size: 16px; }
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
    .row .btn, .field, .choice { min-height: 44px; }
  }
</style>
