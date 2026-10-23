<script>
  // One file's unified diff: hunk headers, old and new line numbers, and
  // added or removed lines on a sage or coral wash. Long lines wrap unless
  // the reader turned wrapping off.
  import { tick } from 'svelte'
  import { code } from './codeprefs.svelte.js'
  // With oncomment, a + shows on each line and opens a comment box under it.
  import { commentLine } from './diffComments.ts'
  let { file, oncomment = null } = $props()
  let picked = $state('') // the tapped line; on touch screens only it shows the +
  let boxAt = $state('') // "hunk:line" of the open comment box
  let draft = $state('')
  let field = $state()
  let origin = null
  async function open(key, event) {
    origin = event.currentTarget
    boxAt = key
    draft = ''
    await tick()
    if (boxAt === key) field?.focus()
  }
  function close() {
    const button = origin
    boxAt = ''
    tick().then(() => { if (!boxAt && button?.isConnected) button.focus({ preventScroll: true }) })
  }
  function keydown(event, line) {
    if (event.isComposing) return
    if (event.key === 'Escape') {
      event.preventDefault()
      event.stopPropagation()
      close()
    } else if (event.key === 'Enter' && (event.ctrlKey || event.metaKey)) {
      event.preventDefault()
      event.stopPropagation()
      add(line)
    }
  }
  function add(l) {
    const text = draft.trim()
    if (!text) return
    oncomment({ file: file.path, line: commentLine(l), quote: l.t + l.text, text })
    close()
  }
</script>

{#if file.kind === 'binary'}
  <p class="note">Binary file. Git shows no line changes for it.</p>
{:else if !file.hunks.length}
  <p class="note">{file.kind === 'renamed' ? 'Renamed with no line changes.' : 'No line changes.'}</p>
{:else}
  <div class="diff" class:wrap={code.wrap} role="table" aria-label="Changes in {file.path}">
    {#each file.hunks as h, hi}
      <div class="hunk" role="row"><span class="hh mono">{h.header.replace(/^(@@[^@]*@@).*/, '$1')}</span>{#if h.context}<span class="ctx">{h.context}</span>{/if}</div>
      {#each h.lines as l, li}
        {@const key = `${hi}:${li}`}
        <!-- Tapping a line only selects it, so the + can show on touch screens; the + button is the keyboard path. -->
        <!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
        <div class="ln {key === picked ? 'sel ' : ''}{l.t === '+' ? 'add' : l.t === '-' ? 'del' : l.t === '\\' ? 'eol' : ''}" role="row" onclick={() => (picked = key)}>
          {#if oncomment && l.t !== '\\'}<button class="addc" aria-label="Comment on line {commentLine(l)}" onclick={(event) => open(key, event)}>+</button>{/if}
          <span class="n" aria-hidden="true">{l.a || ''}</span>
          <span class="n" aria-hidden="true">{l.b || ''}</span>
          <span class="s" aria-hidden="true">{l.t === '\\' ? '' : l.t}</span>
          <span class="t">{l.text}</span>
        </div>
        {#if boxAt === key}
          <div class="box" data-esc>
            <textarea rows="3" bind:this={field} bind:value={draft} onkeydown={(event) => keydown(event, l)} placeholder="Comment for the author" aria-label="Comment on line {commentLine(l)}"></textarea>
            <div class="bar"><button class="go" disabled={!draft.trim()} onclick={() => add(l)}>Add comment</button><button class="cx" onclick={close}>Cancel</button><span class="hint">Ctrl/⌘+Enter to add</span></div>
          </div>
        {/if}
      {/each}
    {/each}
  </div>
{/if}

<style>
  .diff { font-family: var(--mono); font-size: 12.5px; line-height: 1.6; font-variant-ligatures: none; overflow-x: auto; background: #19181a; }
  .hunk, .ln { display: grid; grid-template-columns: 44px 44px 18px 1fr; min-width: max-content; }
  .hunk { display: flex; gap: 10px; align-items: baseline; padding: 4px 12px; background: var(--iris-wash); color: var(--iris); border-top: 1px solid var(--line-soft); border-bottom: 1px solid var(--line-soft); }
  .hunk:first-child { border-top: 0; }
  .hh { font-size: 12px; }
  .ctx { color: var(--muted); font-family: var(--mono); font-size: 12px; white-space: pre; }
  .n { text-align: right; padding-right: 8px; color: #55514b; user-select: none; }
  .s { text-align: center; color: var(--faint); user-select: none; }
  .t { white-space: pre; padding-right: 16px; color: var(--ink-2); tab-size: 4; }
  .wrap .hunk, .wrap .ln { min-width: 0; }
  .wrap .t { white-space: pre-wrap; overflow-wrap: anywhere; min-width: 0; padding-left: 2ch; text-indent: -2ch; }
  .wrap .ctx { white-space: normal; }
  .add { background: rgba(151, 201, 149, 0.10); }
  .add .s, .add .t { color: #b9dcb7; }
  .add .n { color: #6f8f6d; background: rgba(151, 201, 149, 0.06); }
  .del { background: rgba(239, 138, 128, 0.10); }
  .del .s, .del .t { color: #f2b0a9; }
  .del .n { color: #9a6560; background: rgba(239, 138, 128, 0.06); }
  .ln { position: relative; }
  .addc { position: absolute; left: 46px; top: 2px; width: 18px; height: 18px; line-height: 16px; border-radius: 4px; background: var(--iris); color: #18161d; font-weight: 700; opacity: 0; }
  .addc:focus-visible { opacity: 1; }
  @media (hover: hover) { .ln:hover .addc { opacity: 1; } }
  .box { padding: 8px 12px 10px 62px; background: var(--side); border-top: 1px solid var(--line-soft); border-bottom: 1px solid var(--line-soft); font-family: var(--sans, inherit); min-width: 0; }
  .box textarea { width: 100%; font: inherit; font-size: 13.5px; background: var(--rail); border: 1px solid var(--line); border-radius: 6px; color: var(--ink); padding: 6px 8px; resize: vertical; }
  .box textarea:focus { border-color: var(--iris-edge); outline: none; }
  .bar { display: flex; flex-wrap: wrap; align-items: center; gap: 6px; margin-top: 6px; }
  .hint { color: var(--faint); font-size: var(--t-cap); }
  .go { background: var(--gold); color: #18161d; font-size: 13px; font-weight: 600; border-radius: 6px; padding: 5px 12px; }
  .cx { font-size: 12.5px; font-weight: 600; color: var(--ink-2); border-radius: 6px; padding: 4px 10px; background: var(--raise); }
  .go:disabled { opacity: 0.5; }
  .eol .t { color: var(--faint); font-style: italic; }
  .note { margin: 0; padding: 12px 14px; font-size: 13px; color: var(--faint); background: #19181a; }
  @media (max-width: 760px) {
    .diff { font-size: 11.5px; }
    .hunk, .ln { grid-template-columns: 34px 34px 14px 1fr; }
    .n { padding-right: 4px; }
    .addc { opacity: 0; pointer-events: none; left: auto; right: 6px; top: 1px; width: 28px; height: 18px; line-height: 16px; }
    .ln.sel .addc, .addc:focus-visible { opacity: 1; pointer-events: auto; }
    .ln.sel .t { padding-right: 40px; }
    .box { padding-left: 12px; }
    .box textarea { font-size: 16px; }
    .go, .cx { min-height: 44px; padding: 0 14px; }
  }
</style>
