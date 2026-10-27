<script lang="ts">
  // The banner over a request's terminal. While the command runs it only says
  // so. When the command exits, the dock has already sent the result, and the
  // banner is one quiet line. "Add a note" and "Share last 20 lines" send a
  // follow-up; the lines are shown before they are sent.
  import { errorText } from '../errorText.ts'
  import { sentLine, type SentReport } from './termRequest.ts'

  let { name, sent, onpreview, onfollowup }: {
    name: string
    // sent is the result sent on exit, or undefined while the command runs.
    sent: SentReport | undefined
    onpreview: () => Promise<string>
    onfollowup: (note: string, lines: string | null) => Promise<unknown>
  } = $props()
  let open = $state<'' | 'note' | 'lines'>('')
  let note = $state('')
  let lines = $state('')
  let error = $state('')
  let busy = $state(false)
  let again = $state('') // what the last follow-up sent

  async function show(kind: 'note' | 'lines') {
    error = ''
    if (open === kind) { open = ''; return }
    if (kind === 'lines') {
      try { lines = await onpreview() } catch (e) { error = errorText(e); return }
    }
    open = kind
  }

  async function send() {
    if (busy) return
    busy = true
    error = ''
    try {
      await onfollowup(note, open === 'lines' ? lines : null)
      again = open === 'lines' ? 'Sent the last 20 lines.' : 'Sent your note.'
      open = ''
      note = ''
    } catch (e) { error = errorText(e) } finally { busy = false }
  }
</script>

<section class="result" aria-label="Result for {name}">
  {#if !sent}
    <p class="line">Running for <b>{name}</b>. The result goes to {name} when it exits.</p>
  {:else}
    <p class="line">{sentLine(sent)}
      <button class="link" onclick={() => show('note')}>Add a note</button>
      <button class="link" onclick={() => show('lines')}>Share last 20 lines</button>
      {#if again}<span class="again">{again}</span>{/if}
    </p>
    {#if open}
      {#if open === 'lines'}<pre class="lines" aria-label="Lines that will be sent">{lines || '(nothing on the screen)'}</pre>{/if}
      <input bind:value={note} placeholder="Note to {name} (optional)" aria-label="Note" maxlength="2000" onkeydown={(e) => { if (e.key === 'Enter') send() }} />
      <div class="row">
        <button class="b primary" onclick={send} disabled={busy || (open === 'note' && !note.trim())}>Send to {name}</button>
        <button class="b" onclick={() => (open = '')}>Cancel</button>
      </div>
    {/if}
    {#if error}<p class="err" role="alert">{error}</p>{/if}
  {/if}
</section>

<style>
  .result { display: flex; flex-direction: column; gap: var(--s2); padding: var(--s2) 14px; background: var(--gold-wash); border-bottom: 1px solid var(--line-soft); font-size: var(--t-ui); color: var(--ink-2); flex: none; max-height: 70%; overflow-y: auto; }
  .line { margin: 0; display: flex; flex-wrap: wrap; gap: var(--s2); align-items: baseline; }
  .line b { color: var(--ink); }
  .again { font-size: var(--t-sm); color: var(--muted); }
  .link { background: none; padding: 0; font-size: var(--t-sm); color: var(--muted); text-decoration: underline; }
  @media (hover: hover) {
    .link:hover { color: var(--ink); }
  }
  input { padding: 6px var(--s3); border-radius: 6px; background: var(--raise); color: var(--ink); border: 1px solid var(--line-soft); font-size: var(--t-ui); }
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
    .b, input { min-height: 44px; }
    .link { min-height: 44px; }
  }
</style>
