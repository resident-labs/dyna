<script lang="ts">
  // The owner's review of a terminal request that has not started: who asks,
  // why, and the exact command. Run starts it as filed; Decline ends it.
  // Nothing here runs until Run is clicked.
  import type { Question } from '../api/room.ts'
  import { errorText } from '../errorText.ts'
  import { commandLine } from './termRequest.ts'

  let { q, onrun, ondecline }: { q: Question; onrun: () => Promise<unknown>; ondecline: (reason: string) => Promise<unknown> } = $props()
  let filed = $derived(q.terminal?.argv ?? [])
  let plain = $derived(filed.length === 0) // a plain login shell, no command
  let declining = $state(false)
  let reason = $state('')
  let error = $state('')
  let busy = $state(false)

  async function act(call: () => Promise<unknown>) {
    if (busy) return
    busy = true
    error = ''
    try { await call() } catch (e) { error = errorText(e) } finally { busy = false }
  }
</script>

<section class="review" aria-label="Terminal request from {q.askerName}">
  <p class="who">For <b>{q.askerName}</b>: {q.terminal?.reason}</p>
  {#if plain}
    <p class="meta">A plain shell, no command. Run opens it, and it reports when the shell exits.</p>
  {:else}
    <pre class="cmd" aria-label="Command to run">{commandLine(filed)}</pre>
  {/if}
  <p class="meta">Runs in <code>{q.terminal?.cwd}</code> as you, with your display. {q.askerName} gets the exit code when it ends.</p>
  {#if error}<p class="err" role="alert">{error}</p>{/if}
  {#if declining}
    <div class="row">
      <input bind:value={reason} placeholder="Reason (optional)" aria-label="Reason for declining" onkeydown={(e) => { if (e.key === 'Enter') act(() => ondecline(reason)) }} />
      <button class="b" onclick={() => act(() => ondecline(reason))} disabled={busy}>Decline</button>
      <button class="b" onclick={() => (declining = false)}>Cancel</button>
    </div>
  {:else}
    <div class="row">
      <button class="b primary" onclick={() => act(onrun)} disabled={busy}>Run</button>
      <button class="b" onclick={() => (declining = true)}>Decline…</button>
    </div>
  {/if}
</section>

<style>
  .review { display: flex; flex-direction: column; gap: var(--s2); padding: var(--s3) 14px; background: var(--gold-wash); border-bottom: 1px solid var(--line-soft); font-size: var(--t-ui); color: var(--ink-2); overflow-y: auto; }
  .who { margin: 0; color: var(--ink); }
  .who b { color: var(--gold); font-weight: 600; }
  .cmd { margin: 0; padding: 6px var(--s3); font: var(--t-sm)/1.5 var(--mono, ui-monospace, monospace); background: #19181a; color: var(--ink); border-radius: 6px; white-space: pre-wrap; overflow-wrap: anywhere; border: 1px solid var(--line-soft); }
  .meta { margin: 0; font-size: var(--t-sm); color: var(--muted); }
  .err { margin: 0; font-size: var(--t-sm); color: var(--coral); }
  .row { display: flex; gap: var(--s2); flex-wrap: wrap; align-items: center; }
  input { flex: 1; min-width: 160px; padding: 6px var(--s3); border-radius: 6px; background: var(--raise); color: var(--ink); border: 1px solid var(--line-soft); font-size: var(--t-ui); }
  .b { font-size: var(--t-ui); font-weight: 600; padding: 6px var(--s3); border-radius: 6px; background: var(--raise); color: var(--ink-2); }
  .b:not(:disabled), .b.primary:not(:disabled) { transition: background-color var(--motion-fast) var(--ease), color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .b:hover:not(:disabled) { background: var(--raise-hover); color: var(--ink); }
  }
  .b:active:not(:disabled) { background: var(--select); }
  .b.primary { background: var(--iris); color: #18161d; }
  @media (hover: hover) {
    .b.primary:hover:not(:disabled) { background: var(--iris-hover); color: #18161d; }
  }
  .b.primary:active:not(:disabled) { background: var(--select); color: var(--ink); }
  .b:disabled { opacity: 0.6; }
  @media (max-width: 760px) {
    .b, input { min-height: 44px; }
  }
</style>
