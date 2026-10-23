<script>
  // The pending owner comments on a diff and the bar that sends them as one
  // batch. Nothing is sent until Send; Discard drops them all. Comments added
  // while a send runs stay pending.
  //
  // Props:
  //   pending  the comments (bindable)
  //   author   who they go to, for the label
  //   deliver  async (comments) that makes the one call
  import { tick } from 'svelte'
  import { sendLabel, sendPending } from './diffComments.ts'

  let { pending = $bindable([]), author = '', deliver } = $props()
  let sending = $state(false)
  let error = $state('')
  let editing = $state.raw(null)
  let draft = $state('')
  let field = $state()
  let origin = null
  $effect(() => {
    if (editing && !pending.includes(editing)) editing = null
  })
  async function edit(comment, event) {
    if (sending || editing) return
    origin = event.currentTarget
    editing = comment
    draft = comment.text
    await tick()
    field?.focus()
  }
  function finishEdit() {
    const button = origin
    editing = null
    tick().then(() => { if (!editing && button?.isConnected) button.focus({ preventScroll: true }) })
  }
  function save() {
    if (sending || !editing || !draft.trim()) return
    pending = pending.map(comment => comment === editing ? { ...comment, text: draft.trim() } : comment)
    finishEdit()
  }
  function editKey(event) {
    if (event.isComposing) return
    if (event.key === 'Escape') {
      event.preventDefault()
      event.stopPropagation()
      finishEdit()
    } else if (event.key === 'Enter' && (event.ctrlKey || event.metaKey)) {
      event.preventDefault()
      event.stopPropagation()
      save()
    }
  }
  function remove(index) {
    if (!sending && !editing) pending = pending.filter((_, i) => i !== index)
  }
  async function send() {
    if (sending || editing) return
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
    <ul class="plist" class:editing={!!editing}>
      {#each pending as p, i}
        <li>
          <div class="comment-head"><span class="mono">{p.file}:{p.line}</span><span class="row-actions">
            <button class="edit tool" disabled={sending || !!editing} aria-label="Edit comment on {p.file} line {p.line}" onclick={(event) => edit(p, event)}>Edit</button>
            <button class="rm tool" disabled={sending || !!editing} aria-label="Remove comment on {p.file} line {p.line}" onclick={() => remove(i)}>Remove</button>
          </span></div>
          {#if editing === p}
            <div class="editor" data-esc>
              <textarea bind:this={field} bind:value={draft} rows="3" aria-label="Edit comment text" onkeydown={editKey}></textarea>
              <div class="row-actions"><button class="tool" disabled={!draft.trim()} onclick={save}>Save</button><button class="tool" onclick={finishEdit}>Cancel</button></div>
            </div>
          {:else}<p class="comment-text">{p.text}</p>{/if}
        </li>
      {/each}
    </ul>
    {#if error}<p class="err">{error}</p>{/if}
    <div class="sact">
      <button class="send" onclick={send} disabled={sending || !!editing}>{sending ? 'Sending…' : sendLabel(pending.length, author)}</button>
      <button class="tool" onclick={() => (pending = [])} disabled={sending || !!editing}>Discard</button>
    </div>
  </div>
{/if}

<style>
  .sendbar { position: sticky; bottom: 0; margin-top: 10px; padding: 10px 12px; background: var(--side); border: 1px solid var(--iris-edge); border-radius: 10px; display: flex; flex-direction: column; gap: 8px; }
  .plist { list-style: none; margin: 0; padding: 0; max-height: 120px; overflow-y: auto; font-size: 13px; color: var(--ink-2); display: flex; flex-direction: column; gap: 4px; }
  .plist.editing { max-height: min(50dvh, 360px); }
  .comment-head { display: flex; align-items: center; flex-wrap: wrap; gap: var(--s2); }
  .row-actions { display: flex; gap: var(--s1); }
  .comment-head .row-actions { margin-left: auto; }
  .comment-text { margin: var(--s1) 0; white-space: pre-wrap; }
  .editor { display: flex; flex-direction: column; gap: var(--s2); margin-top: var(--s1); }
  textarea { width: 100%; min-height: 72px; padding: var(--s2); border: 1px solid var(--line); border-radius: var(--r-xs); background: var(--rail); color: var(--ink); resize: vertical; font: inherit; }
  textarea:focus { outline-color: var(--iris); }
  .plist li { overflow-wrap: anywhere; }
  .plist .mono { font-family: var(--mono); font-size: 12px; color: var(--muted); }
  .rm { margin-left: 6px; color: var(--faint); }
  .sact { display: flex; flex-wrap: wrap; gap: 6px; }
  .send { background: var(--gold); color: #18161d; font-size: 13px; font-weight: 600; border-radius: 6px; padding: 6px 12px; }
  .tool { font-size: 12px; font-weight: 600; color: var(--muted); padding: 5px 10px; border-radius: 6px; background: var(--raise); transition: color var(--motion-fast), background-color var(--motion-fast); }
  .tool:hover { color: var(--ink); background: var(--raise-hover); }
  .tool:active { background: var(--raise-hover); }
  .err { margin: 0; color: var(--coral); font-size: 12.5px; overflow-wrap: anywhere; }
  button:disabled { opacity: 0.5; cursor: default; pointer-events: none; }
  @media (max-width: 760px) { textarea { font-size: 16px; } .tool, .rm { min-height: 44px; } .send { min-height: 44px; } .tool { padding: 10px 12px; } .rm { padding: 4px 12px; } }
</style>
