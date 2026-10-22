<script lang="ts">
  import { dialog } from './dialog.js'
  // A small dialog that asks for one value: a new name, a steering message,
  // or a typed name that confirms a delete. With `match`, the action stays
  // off until the value equals it exactly. onsubmit(value) may return a
  // promise; a rejection shows as an error and keeps the dialog open.
  import { untrack } from 'svelte'

  interface Props {
    title: string
    lines?: string[]
    label?: string
    value?: string
    placeholder?: string
    multiline?: boolean
    match?: string
    action?: string
    danger?: boolean
    onsubmit: (value: string) => void | Promise<unknown>
    oncancel: () => void
  }

  let {
    title,
    lines = [],
    label = '',
    value = '',
    placeholder = '',
    multiline = false,
    match = '',
    action = 'Save',
    danger = false,
    onsubmit,
    oncancel,
  }: Props = $props()

  let text = $state(untrack(() => value)) // the start value; the dialog owns the text
  let busy = $state(false)
  let error = $state('')
  let field: HTMLInputElement | HTMLTextAreaElement | undefined = $state()

  let ok = $derived(!busy && (match ? text === match : text.trim() !== '' && (multiline || text.trim() !== value.trim())))

  async function submit() {
    if (!ok) return
    busy = true
    error = ''
    try {
      await onsubmit(multiline ? text : text.trim())
    } catch (e) {
      const message = (typeof e === 'object' || typeof e === 'function') && e !== null && 'message' in e ? e.message : undefined
      error = String(message || e)
      busy = false
    }
  }

  function keydown(e: KeyboardEvent) {
    if (e.key === 'Enter' && (!multiline || e.ctrlKey || e.metaKey) && e.target === field) {
      e.preventDefault()
      submit()
    }
  }
</script>


<!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
<div class="backdrop">
  <!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
  <div class="card" use:dialog={{ close: oncancel, busy, initial: () => field, select: !multiline && !!value }} role="dialog" onkeydown={keydown} tabindex="-1" aria-modal="true" aria-label={title} onclick={(e) => e.stopPropagation()}>
    <h2>{title}</h2>
    {#each lines as l}<p>{l}</p>{/each}
    <label>
      {#if label}<span class="lab">{label}</span>{/if}
      {#if multiline}
        <textarea bind:this={field} bind:value={text} {placeholder} rows="4" disabled={busy}></textarea>
      {:else}
        <input bind:this={field} bind:value={text} {placeholder} disabled={busy} autocomplete="off" autocapitalize="off" spellcheck="false" />
      {/if}
    </label>
    {#if multiline}<p class="hint"><kbd>Ctrl</kbd>+<kbd>Enter</kbd> to send</p>{/if}
    {#if error}<p class="err">{error}</p>{/if}
    <div class="row">
      <button class="ghost" onclick={oncancel} disabled={busy}>Cancel</button>
      <button class="go" class:danger onclick={submit} disabled={!ok}>{busy ? 'Working…' : action}</button>
    </div>
  </div>
</div>

<style>
  .backdrop { position: fixed; inset: 0; background: rgba(8, 8, 10, 0.62); display: grid; place-items: center; z-index: 60; padding: var(--s6); }
  .card { width: min(460px, 100%); background: var(--side); border-radius: 12px; box-shadow: 0 24px 64px rgba(0, 0, 0, 0.5); padding: 20px 20px var(--s4); }
  h2 { margin: 0 0 10px; font-size: var(--t-head); font-weight: 600; overflow-wrap: anywhere; }
  p { margin: 0 0 var(--s2); color: var(--ink-2); font-size: var(--t-body); line-height: var(--leading); }
  label { display: flex; flex-direction: column; gap: 6px; margin-top: var(--s3); }
  .lab { font-size: var(--t-sm); font-weight: 700; color: var(--muted); text-transform: uppercase; letter-spacing: 0.06em; }
  input, textarea { width: 100%; background: var(--rail); border: 1px solid var(--line); border-radius: 7px; color: var(--ink); font: inherit; font-size: var(--t-title); padding: 9px 11px; outline: none; resize: vertical; }
  input:focus, textarea:focus { border-color: var(--iris-edge); }
  .hint { margin: 6px 0 0; font-size: var(--t-sm); color: var(--faint); }
  kbd { font-family: var(--sans); font-size: var(--t-cap); font-weight: 600; color: var(--muted); }
  .err { color: var(--coral); margin-top: 10px; }
  .row { display: flex; justify-content: flex-end; gap: var(--s2); margin-top: var(--s4); }
  .row button { padding: var(--s2) var(--s4); border-radius: 6px; font-size: var(--t-body); font-weight: 500; }
  .ghost { color: var(--ink-2); transition: background-color var(--motion-fast) var(--ease), color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .ghost:hover { background: var(--hover); color: var(--ink); }
  }
  .ghost:active { background: var(--select); }
  .go { background: var(--iris); color: #18161d; }
  .go.danger { background: #c9564b; color: #fff; }
  .go:disabled, .ghost:disabled { opacity: 0.45; cursor: default; }
  @media (max-width: 760px) {
    .backdrop { padding: var(--s4); align-items: end; }
    .card { border-radius: 14px; padding-bottom: calc(16px + env(safe-area-inset-bottom)); }
    .row button { min-height: 44px; flex: 1; }
  }
</style>
