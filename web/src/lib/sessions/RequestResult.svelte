<script lang="ts">
  // The banner over a request's terminal. While the command runs it only says
  // so; when the command exits (or at any time for a plain shell) the owner
  // reports done or failed with a note. Sharing the last lines is off, and
  // ticking it shows exactly what would be sent before anything is.
  import type { Question } from '../api/room.ts'
  import { emptyForm, whatAgentGets, type ResultForm } from './termRequest.ts'

  let { q, exit, onpreview, onreport }: {
    q: Question
    // exit is the command's exit code, or undefined while it still runs.
    exit: number | undefined
    onpreview: () => Promise<string>
    onreport: (verdict: 'done' | 'failed', form: ResultForm) => Promise<unknown>
  } = $props()
  let form = $state<ResultForm>(emptyForm())
  let error = $state('')
  let busy = $state(false)
  let plain = $derived((q.terminal?.argv ?? []).length === 0)
  let ready = $derived(plain || exit !== undefined)

  async function tick() {
    error = ''
    if (!form.share) {
      try {
        const lines = await onpreview()
        form = { ...form, share: true, lines }
      } catch (e) { error = e instanceof Error ? e.message : String(e) }
    } else form = { ...form, share: false, lines: null }
  }

  async function report(verdict: 'done' | 'failed') {
    if (busy) return
    busy = true
    error = ''
    try { await onreport(verdict, form) } catch (e) { error = e instanceof Error ? e.message : String(e) } finally { busy = false }
  }
</script>

<section class="result" aria-label="Result for {q.askerName}">
  {#if !ready}
    <p class="line">Running for <b>{q.askerName}</b>. When it exits you can report the result.</p>
  {:else}
    <p class="line">{#if exit !== undefined}The command exited with code <b>{exit}</b>.{:else}Report when the step is done.{/if} {whatAgentGets(q.askerName, exit !== undefined, form)}</p>
    <input bind:value={form.note} placeholder="Note to {q.askerName} (optional)" aria-label="Note" maxlength="2000" />
    <label class="share"><input type="checkbox" checked={form.share} onchange={tick} /> Also share the last 20 lines</label>
    {#if form.share && form.lines !== null}
      <pre class="lines" aria-label="Lines that will be sent">{form.lines || '(nothing on the screen)'}</pre>
    {/if}
    {#if error}<p class="err" role="alert">{error}</p>{/if}
    <div class="row">
      <button class="b primary" onclick={() => report('done')} disabled={busy}>Report done</button>
      <button class="b" onclick={() => report('failed')} disabled={busy}>Report failed</button>
    </div>
  {/if}
</section>

<style>
  .result { display: flex; flex-direction: column; gap: var(--s2); padding: var(--s2) 14px; background: var(--gold-wash); border-bottom: 1px solid var(--line-soft); font-size: var(--t-ui); color: var(--ink-2); flex: none; max-height: 70%; overflow-y: auto; }
  .line { margin: 0; }
  .line b { color: var(--ink); }
  input:not([type='checkbox']) { padding: 6px var(--s3); border-radius: 6px; background: var(--raise); color: var(--ink); border: 1px solid var(--line-soft); font-size: var(--t-ui); }
  .share { display: flex; align-items: center; gap: var(--s2); font-size: var(--t-sm); color: var(--muted); }
  .lines { margin: 0; padding: 6px var(--s3); font: var(--t-sm)/1.45 var(--mono, ui-monospace, monospace); background: #19181a; color: var(--ink); border-radius: 6px; border: 1px solid var(--line-soft); white-space: pre-wrap; overflow-wrap: anywhere; max-height: 96px; overflow-y: auto; }
  .err { margin: 0; font-size: var(--t-sm); color: var(--coral); }
  .row { display: flex; gap: var(--s2); flex-wrap: wrap; }
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
    .b, input:not([type='checkbox']) { min-height: 44px; }
    .share { min-height: 44px; }
    input[type='checkbox'] { width: 22px; height: 22px; }
  }
</style>
