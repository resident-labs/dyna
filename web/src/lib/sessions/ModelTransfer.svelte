<script lang="ts">
  import { dialog as modal } from '../ui/dialog.ts'
  import { onMount } from 'svelte'
  import { api } from '../../api.js'
  import type { Member, ProfileChoice } from '../api/room.ts'
  import type { TransferState } from '../api/session.ts'

  let { member, roomId, profiles = [], onclose }: { member: Member; roomId: string; profiles?: ProfileChoice[]; onclose: () => void } = $props()
  let transfer = $state<TransferState | null>(null)
  let error = $state('')
  let busy = $state(false)
  let loaded = $state(false)
  let transferRevision = 0
  let pollSerial = 0
  let active = $derived(['waiting', 'stopping', 'starting'].includes(transfer?.phase ?? ''))
  const PROGRESS: Record<string, string> = { waiting: 'Preparing the handoff…', stopping: 'Switching models…', starting: 'Starting the new model…', completed: 'The new model is ready.', failed: 'Couldn’t start that model. Choose a model to try again.', cancelled: 'Transfer cancelled. Choose a model to try again.' }
  let progress = $derived(PROGRESS[transfer?.phase ?? ''] ?? '')

  function isCurrent(p: ProfileChoice) {
    const profile = transfer?.phase === 'completed' ? transfer.profile : member.profile
    const model = transfer?.phase === 'completed' ? transfer.model : member.model
    return p.name === profile && (!model || model === p.model)
  }
  function unavailable(p: ProfileChoice) {
    return busy || !loaded || (active && (!transfer?.error && !error || p.name !== transfer?.profile)) ||
      (!active && isCurrent(p) && (transfer?.phase === 'completed' || member.state !== 'error') && !['failed', 'cancelled'].includes(transfer?.phase ?? ''))
  }

  async function refresh(force = false) {
    if (busy && !force) return
    const revision = transferRevision
    const serial = ++pollSerial
    const current = () => revision === transferRevision && serial === pollSerial && (force || !busy)
    try {
      const next = await api.sessionTransfer(roomId, member.id)
      if (current()) {
        transfer = next
        if (error === 'Couldn’t check transfer progress. Try again.') error = ''
      }
    } catch (e) { if (current()) error = 'Couldn’t check transfer progress. Try again.' }
  }
  onMount(() => {
    let alive = true
    refresh().then(async () => {
      if (!alive) return
      // Opening a saved transfer also resumes its durable worker. The API is
      // idempotent, so this never creates a second replacement.
      if (active) await resume()
      if (!alive) return
      loaded = true
    })
    const timer = setInterval(() => { if (active) refresh() }, 1000)
    return () => { alive = false; transferRevision++; clearInterval(timer) }
  })
  async function start(p: ProfileChoice) {
    if (unavailable(p)) return
    if (active) { await resume(); return }
    const revision = ++transferRevision
    pollSerial++
    busy = true; error = ''
    try {
      const next = await api.transferSession(roomId, member.id, { profile: p.name, waitSeconds: 30, retry: ['failed', 'cancelled'].includes(transfer?.phase ?? '') })
      if (revision === transferRevision) transfer = next
    } catch (e) {
      if (revision === transferRevision) {
        error = 'Couldn’t start transfer. Choose a model to try again.'
        await refresh(true)
      }
    } finally { if (revision === transferRevision) busy = false }
  }
  async function resume() {
    if (!transfer || busy) return
    const revision = ++transferRevision
    pollSerial++
    busy = true; error = ''
    try {
      const next = await api.transferSession(roomId, member.id, { profile: transfer.profile, model: transfer.model || '', waitSeconds: 30 })
      if (revision === transferRevision) transfer = next
    } catch (e) { if (revision === transferRevision) error = 'Transfer paused. Select the same model to try again.' }
    finally { if (revision === transferRevision) busy = false }
  }
  async function cancel() {
    if (busy) return
    const revision = ++transferRevision
    pollSerial++
    busy = true; error = ''
    try {
      const next = await api.cancelSessionTransfer(roomId, member.id)
      if (revision === transferRevision) transfer = next
    } catch (e) { if (revision === transferRevision) error = 'Couldn’t cancel. The transfer may have started.' }
    finally { if (revision === transferRevision) busy = false }
  }
</script>

<!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
<div class="backdrop">
  <!-- svelte-ignore a11y_click_events_have_key_events -->
  <div class="card" role="dialog" tabindex="-1" aria-modal="true" aria-label="Transfer to new model" use:modal={{ close: onclose }} onclick={(e) => e.stopPropagation()}>
    <header><div><h2>Transfer {member.name}</h2><p>Choose a model to continue this session.</p></div><button class="close" onclick={onclose} aria-label="Close">×</button></header>
    {#if transfer}<p class="progress" class:failed={transfer.phase === 'failed'} aria-live="polite">{progress}</p>{/if}
    {#if error}<p class="error" role="alert">{error}</p>{/if}
    <div class="models" aria-label="Models">
      {#each profiles as p (p.name)}
        <button class="model" class:current={isCurrent(p)} disabled={unavailable(p)} onclick={() => start(p)}>
          <span class="label"><strong>{p.name}</strong>{#if p.model && p.model !== p.name}<span class="detail">{p.model}</span>{/if}</span>
          <span class="badge">{active && transfer?.profile === p.name ? 'Switching…' : isCurrent(p) ? 'Current' : '→'}</span>
        </button>
      {/each}
    </div>
    {#if loaded && !profiles.length}<p class="error">No models are available. Add a model in Settings.</p>{/if}
    {#if !loaded}<p class="hint">Loading models…</p>{/if}
    {#if transfer?.phase === 'waiting'}<footer><button disabled={busy} onclick={cancel}>Cancel transfer</button></footer>{/if}
  </div>
</div>

<style>
  .backdrop { position: fixed; inset: 0; z-index: 62; padding: 20px; background: #08080aaa; display: grid; place-items: center; }
  .card { width: min(440px, 100%); max-height: calc(100dvh - 40px); overflow-y: auto; border-radius: 12px; background: var(--side); color: var(--ink); padding: 22px; box-shadow: 0 24px 64px #0008; outline: none; }
  header { display: flex; align-items: flex-start; justify-content: space-between; gap: var(--s3); }
  h2 { margin: 0; font-size: var(--t-head); overflow-wrap: anywhere; }
  header > div { min-width: 0; }
  header p { margin: 5px 0 20px; color: var(--muted); font-size: var(--t-ui); }
  .close { font-size: var(--t-display); line-height: 1; min-width: 32px; min-height: 32px; border-radius: 6px; transition: background-color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .close:hover { background: var(--hover); }
  }
  .close:active { background: var(--select); }
  .models { display: grid; gap: 6px; }
  .model { display: flex; align-items: center; justify-content: space-between; gap: var(--s3); width: 100%; text-align: left; min-height: 58px; padding: 10px 14px; border: 1px solid var(--line); border-radius: 8px; background: var(--main); }
  .model:not(:disabled) { transition: background-color var(--motion-fast) var(--ease), border-color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .model:not(:disabled):hover { background: var(--hover); border-color: var(--iris-edge); }
  }
  .model:not(:disabled):active { background: var(--select); }
  .model:focus-visible { outline: 2px solid var(--iris); outline-offset: 2px; }
  .label { display: grid; gap: 2px; min-width: 0; }
  strong { font-size: var(--t-body); font-weight: 600; overflow-wrap: anywhere; }
  .detail { color: var(--muted); font-size: var(--t-sm); overflow-wrap: anywhere; }
  .badge { color: var(--muted); font-size: var(--t-sm); flex: none; }
  .model:not(:disabled) .badge { color: var(--iris); }
  .current { background: transparent; }
  .progress { padding: 10px var(--s3); border-radius: 6px; background: var(--iris-wash); color: var(--iris); font-size: var(--t-ui); margin: 0 0 var(--s3); }
  .failed, .error { color: var(--coral); }
  .error, .hint { font-size: var(--t-ui); margin: 0 0 var(--s3); }
  .hint { color: var(--muted); }
  footer { margin-top: var(--s4); display: flex; justify-content: flex-end; }
  footer button { padding: var(--s2) var(--s3); border-radius: 6px; background: var(--hover); font-size: var(--t-ui); }
  button:disabled { opacity: .55; cursor: default; }
  @media (max-width: 480px) { .backdrop { padding: var(--s4); } .card { padding: 18px; max-height: calc(100dvh - 32px); } }
</style>
