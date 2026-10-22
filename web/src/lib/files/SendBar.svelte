<script>
  // The pending owner comments on a diff and the bar that sends them as one
  // batch. Nothing is sent until Send; Discard drops them all. Comments added
  // while a send runs stay pending.
  //
  // Props:
  //   pending  the comments (bindable)
  //   author   who they go to, for the label
  //   deliver  async (comments) that makes the one call
  import { sendLabel, sendPending } from './diffComments.ts'

  let { pending = $bindable([]), author = '', deliver } = $props()
  let sending = $state(false)
  let error = $state('')
  async function send() {
    sending = true
    error = ''
    try {
      await sendPending(deliver, () => pending, (rest) => (pending = rest))
    } catch (e) {
      error = String(e?.message || e)
    } finally {
      sending = false
    }
  }
</script>

{#if pending.length}
  <div class="sendbar" role="region" aria-label="Pending comments">
    <ul class="plist">
      {#each pending as p, i}
        <li><span class="mono">{p.file}:{p.line}</span> {p.text}<button class="rm" aria-label="Remove comment" onclick={() => (pending = pending.filter((_, j) => j !== i))}>×</button></li>
      {/each}
    </ul>
    {#if error}<p class="err">{error}</p>{/if}
    <div class="sact">
      <button class="send" onclick={send} disabled={sending}>{sending ? 'Sending…' : sendLabel(pending.length, author)}</button>
      <button class="tool" onclick={() => (pending = [])} disabled={sending}>Discard</button>
    </div>
  </div>
{/if}

<style>
  .sendbar { position: sticky; bottom: 0; margin-top: 10px; padding: 10px 12px; background: var(--side); border: 1px solid var(--iris-edge); border-radius: 10px; display: flex; flex-direction: column; gap: 8px; }
  .plist { list-style: none; margin: 0; padding: 0; max-height: 120px; overflow-y: auto; font-size: 13px; color: var(--ink-2); display: flex; flex-direction: column; gap: 4px; }
  .plist li { overflow-wrap: anywhere; }
  .plist .mono { font-family: var(--mono); font-size: 12px; color: var(--muted); }
  .rm { margin-left: 6px; color: var(--faint); }
  .sact { display: flex; gap: 6px; }
  .send { background: var(--gold); color: #18161d; font-size: 13px; font-weight: 600; border-radius: 6px; padding: 6px 12px; }
  .tool { font-size: 12px; font-weight: 600; color: var(--muted); padding: 5px 10px; border-radius: 6px; background: var(--raise); transition: color var(--motion-fast), background-color var(--motion-fast); }
  .tool:hover { color: var(--ink); background: var(--raise-hover); }
  .tool:active { background: var(--raise-hover); }
  .err { margin: 0; color: var(--coral); font-size: 12.5px; overflow-wrap: anywhere; }
  button:disabled { opacity: 0.5; cursor: default; pointer-events: none; }
  @media (max-width: 760px) { .send { min-height: 44px; } .tool { padding: 10px 12px; } .rm { padding: 4px 12px; } }
</style>
