<script lang="ts">
  import { onDestroy } from 'svelte'
  import type { SignInState } from '../../api/harness.ts'
  import { signInStatus } from './state.ts'

  // SignInCard shows one in-app native sign-in: the link, the one-time code,
  // the current step and, when the login reads a code, a paste box. It fits a
  // 375 px wide phone, where a terminal does not.
  let {
    signIn,
    warning = '',
    busy = false,
    oncancel,
    onpaste,
  }: {
    signIn: SignInState
    warning?: string
    busy?: boolean
    oncancel: () => void
    onpaste?: (code: string) => boolean | void | Promise<boolean | void>
  } = $props()

  let pasted = $state('')
  let selectedChoice = $state('')
  let submittedChoiceStep = $state('')
  let copied = $state(false)
  let copyTimer: ReturnType<typeof setTimeout> | undefined
  const choices = $derived(signIn.choices ?? [])
  // Polls return new objects for the same step, so use its displayed content.
  const choiceStep = $derived(JSON.stringify([signIn.url, signIn.code, signIn.message, choices]))
  const choicePending = $derived(choices.length > 0 && submittedChoiceStep === choiceStep)
  const status = $derived(signInStatus(signIn))
  $effect(() => {
    if (!choices.some((choice) => choice.id === selectedChoice)) selectedChoice = choices[0]?.id ?? ''
  })
  const running = $derived(!['succeeded', 'failed', 'cancelled'].includes(signIn.phase))

  onDestroy(() => clearTimeout(copyTimer))

  async function copy() {
    if (!signIn.code) return
    try {
      await navigator.clipboard.writeText(signIn.code)
      copied = true
      clearTimeout(copyTimer)
      copyTimer = setTimeout(() => (copied = false), 1500)
    } catch {
      // A browser without clipboard access still shows the code to copy by hand.
      copied = false
    }
  }

  async function send() {
    const code = choices.length ? selectedChoice : pasted.trim()
    if (!code || busy || choicePending) return
    const step = choices.length ? choiceStep : ''
    if (step) submittedChoiceStep = step
    pasted = ''
    try {
      if (await onpaste?.(code) === false && submittedChoiceStep === step) submittedChoiceStep = ''
    } catch {
      if (submittedChoiceStep === step) submittedChoiceStep = ''
    }
  }
</script>

<div class="signin-card" aria-live="polite">
  {#if warning}<p class="muted small warn">{warning}</p>{/if}
  {#if signIn.url}
    <a class="link-btn" href={signIn.url} target="_blank" rel="noreferrer noopener">Open the sign-in page</a>
  {/if}
  {#if signIn.code}
    <div class="code-row">
      <output class="code" aria-label="One-time code">{signIn.code}</output>
      <button type="button" class="btn quiet" disabled={busy} onclick={copy}>{copied ? 'Copied' : 'Copy'}</button>
    </div>
  {/if}
  <p class="status">{status}</p>
  {#if signIn.acceptsPaste && running}
    <form class="paste" onsubmit={(e) => { e.preventDefault(); send() }}>
      {#if choices.length}
        <label>
          <span class="muted small">Provider or login method</span>
          <select bind:value={selectedChoice} disabled={busy || choicePending} aria-label="Sign-in option">
            {#each choices as choice (choice.id)}<option value={choice.id}>{choice.label}</option>{/each}
          </select>
        </label>
      {:else}
        <label>
          <span class="muted small">Paste the code or redirect URL from your browser</span>
          <input bind:value={pasted} autocomplete="off" spellcheck="false" disabled={busy} aria-label="Sign-in response" />
        </label>
      {/if}
      <button type="submit" class="btn" disabled={busy || choicePending || (choices.length ? !selectedChoice : !pasted.trim())}>Continue</button>
    </form>
  {/if}
  <div class="row">
    <button type="button" class="btn quiet" disabled={busy} onclick={oncancel}>Cancel sign-in</button>
  </div>
</div>

<style>
  .signin-card { display: grid; gap: var(--s2); min-width: 0; }
  .warn { margin: 0; }
  .link-btn { display: block; text-align: center; background: var(--iris); color: #18161d; border-radius: 6px; padding: var(--s2) var(--s3); font-size: var(--t-ui); font-weight: 500; text-decoration: none; overflow-wrap: anywhere; }
  .code-row { display: flex; align-items: center; gap: var(--s2); flex-wrap: wrap; min-width: 0; }
  .code { font-family: var(--mono); font-size: var(--t-title); letter-spacing: 0.06em; overflow-wrap: anywhere; }
  .status { margin: 0; color: var(--muted); font-size: var(--t-sm); }
  .paste { display: grid; gap: var(--s2); min-width: 0; }
  .paste label { display: grid; gap: var(--s1); min-width: 0; }
  .paste input, .paste select { background: var(--field); border: 1px solid var(--line); border-radius: var(--r-xs); padding: 7px 10px; font-size: var(--t-ui); color: var(--ink); font-family: var(--mono); width: 100%; min-width: 0; }
  .row { display: flex; gap: var(--s2); }
  .muted { color: var(--muted); }
  .small { font-size: var(--t-sm); }
</style>
