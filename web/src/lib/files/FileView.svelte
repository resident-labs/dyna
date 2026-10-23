<script>
  import { untrack } from 'svelte'
  import Segmented from '../ui/Segmented.svelte'
  // One repository file: code with line numbers and syntax colors, an
  // image, rendered markdown (with a Source switch), or a note for a binary
  // file. The server caps text at 2 MiB and says when it cut a file.
  import { api, copyText } from '../../api.ts'
  import { human } from './attach.ts'
  import { markdownDocument } from './preview.ts'
  import Document from './Document.svelte'
  import { highlight, languageOf, escape, splitLines } from '../ui/highlight.ts'
  import { code, setWrap } from './codeprefs.svelte.ts'
  import { crumbs, isMarkdown } from './repo.ts'
  import History from './History.svelte'
  import { ago } from '../ui/format.ts'

  let { roomId, session = '', path, rootLabel = '', narrow = false, at = '', was = '', onat = null, onfolder, onback = null } = $props()

  let file = $state(null)
  let error = $state('')
  let html = $state('')
  let mode = $state('rendered') // markdown: rendered or source
  let copied = $state(false)
  let copyError = $state(false)
  let copyTimer
  $effect(() => {
    path
    copied = false
    copyError = false
    return () => clearTimeout(copyTimer)
  })
  let historyOpen = $state(false)
  // A linked version starts as its commit alone; a History row adds the details.
  const stub = (hash, path) => (hash ? { hash, shortHash: hash.slice(0, 7), path } : null)
  let version = $state(untrack(() => stub(at, was))) // the history row whose version is shown; null shows the file now
  function show(v) {
    version = v
    onat?.(v?.hash ?? '', v?.path ?? '')
  }
  // Back and Forward change the linked version under an open file.
  $effect(() => {
    const hash = at
    const path = was
    untrack(() => { if ((version?.hash ?? '') !== hash) version = stub(hash, path) })
  })
  let blameOn = $state(false)
  let blame = $state(null)
  let blameError = $state('')

  let name = $derived(path.split('/').pop())
  let lang = $derived(file ? languageOf(name, file.language) : '')
  let md = $derived(file && !file.binary && isMarkdown(name, file.language))
  let lineCount = $derived(file?.text ? file.text.split('\n').length - (file.text.endsWith('\n') ? 1 : 0) : 0)
  // One row per line, in blocks the browser can skip drawing off screen.
  let blocks = $derived.by(() => {
    const lines = splitLines(html)
    if (file?.text?.endsWith('\n')) lines.pop()
    const out = []
    for (let i = 0; i < lines.length; i += 400) out.push({ start: i, lines: lines.slice(i, i + 400) })
    return out
  })
  let digits = $derived(String(Math.max(1, lineCount)).length)

  let metaText = $derived(file ? [file.binary ? (file.url ? 'image' : 'binary') : lang || 'text', human(file.size), ...(file.binary ? [] : [`${lineCount} line${lineCount === 1 ? '' : 's'}`])].join(' · ') : '')

  $effect(() => {
    const p = path
    const v = version
    let current = true // a later selection or file makes this request stale
    file = null
    error = ''
    html = ''
    ;(v ? api.gitReadFile(roomId, session, v.hash, v.path || p) : api.readFile(roomId, session, p)).then(
      async (f) => {
        if (!current) return
        file = f
        if (!f.binary && f.text != null) {
          // Plain first, colors when the highlighter is ready.
          html = escape(f.text)
          const h = await highlight(f.text, languageOf(f.name || name, f.language))
          if (current) html = h
        }
      },
      (e) => { if (current) error = String(e?.message || e) },
    )
    return () => { current = false }
  })

  // Blame is read only after the reader switches it on, and only for the file now.
  $effect(() => {
    const p = path
    if (!blameOn || version) return
    blame = null
    blameError = ''
    api.gitBlame(roomId, session, p).then(
      (b) => { if (p === path) blame = b },
      (e) => { if (p === path) blameError = String(e?.message || e) },
    )
  })
  // The commit that last changed each line, keyed by the first line of its range.
  let blameStarts = $derived(new Map((blame?.ranges ?? []).map((r) => [r.startLine, r.commit])))

  async function copyPath() {
    const selectedPath = path
    clearTimeout(copyTimer)
    copied = false
    copyError = false
    try {
      await copyText(selectedPath)
      if (path !== selectedPath) return
      copied = true
    } catch {
      if (path !== selectedPath) return
      copyError = true
    }
    copyTimer = setTimeout(() => { copied = false; copyError = false }, 2000)
  }
</script>

<div class="with-history">
<section class="view">
  <header>
    {#if onback}
      <button class="back" onclick={onback} aria-label="Back to the folder">
        <svg viewBox="0 0 20 20" width="20" height="20" aria-hidden="true"><path d="M12.5 4.5 7 10l5.5 5.5" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" /></svg>
      </button>
    {/if}
    <nav class="crumbs" aria-label="File path">
      <button onclick={() => onfolder('')}>{rootLabel || 'repo'}</button>
      {#each crumbs(path) as c, i}
        <span class="sep" aria-hidden="true">/</span>
        {#if i === crumbs(path).length - 1}
          <span class="leaf">{c.name}</span>
        {:else}
          <button onclick={() => onfolder(c.path)}>{c.name}</button>
        {/if}
      {/each}
    </nav>
    <span class="spacer"></span>
    {#if md}
      <Segmented small label="Markdown view" options={[['rendered', 'Preview'], ['source', 'Source']]} value={mode} onchange={(v) => (mode = v)} />
    {/if}
    {#if file && !file.binary && !(md && mode === 'rendered')}
      <button class="tool" class:on={code.wrap} aria-pressed={code.wrap} onclick={() => setWrap(!code.wrap)} title={code.wrap ? 'Show long lines on one line' : 'Wrap long lines'}>Wrap</button>
    {/if}
    <button class="tool" class:on={blameOn} aria-pressed={blameOn} disabled={!!version} onclick={() => (blameOn = !blameOn)} title={version ? 'Blame shows the file now. Show the current version first.' : 'Show who last changed each line'}>Blame</button>
    <button class="tool" class:on={historyOpen} aria-pressed={historyOpen} onclick={() => (historyOpen = !historyOpen)} title="Show the commits of this file">History</button>
    <button class="tool copy" class:copied onclick={copyPath} title="Copy the path" aria-label="Copy the path">
      <svg viewBox="0 0 20 20" width="18" height="18" aria-hidden="true">
        {#if copied}<path d="m4 10 4 4 8-9" fill="none" stroke="currentColor" stroke-width="1.8" />
        {:else}<rect x="7" y="7" width="9" height="9" rx="2" fill="none" stroke="currentColor" stroke-width="1.8" /><path d="M13 7V5a2 2 0 0 0-2-2H5a2 2 0 0 0-2 2v6a2 2 0 0 0 2 2h2" fill="none" stroke="currentColor" stroke-width="1.8" />{/if}
      </svg><span class="copy-label">{copied ? 'Copied' : 'Copy path'}</span>
    </button>
  </header>
  <div class="copy-status" class:error={copyError} role="status">{copyError ? 'Could not copy' : copied ? 'Copied path' : ''}</div>
  {#if version}
    <p class="version">Showing {version.shortHash}{#if version.author} · {version.author} · {ago(version.time)}{/if}<button onclick={() => show(null)}>Show current</button></p>
  {/if}
  {#if file}
    <p class="meta">{metaText}</p>
  {/if}
  {#if blameError}<p class="err">Blame is unavailable. {blameError}</p>{/if}

  <div class="body">
    {#if error}
      <p class="err">{error}</p>
    {:else if !file}
      <p class="quiet">Loading {name}…</p>
    {:else if file.binary && file.url}
      <div class="image"><img src={file.url} alt={name} /></div>
    {:else if file.binary}
      <div class="empty">
        <strong>No preview for this file</strong>
        <p>It is a binary file of {human(file.size)}. Open it on the computer, or ask an agent about it.</p>
      </div>
    {:else}
      {#if file.truncated}<p class="cut">This file is large. The view shows only its first {human(file.text.length)}.</p>{/if}
      {#if md && mode === 'rendered'}
        <div class="doc"><Document html={markdownDocument(file.text)} /></div>
      {:else}
        <div class="code hl" class:wrap={code.wrap} style="--gw: {digits}ch">
          {#each blocks as b (b.start)}
            <div class="blk" style="contain-intrinsic-size: auto {b.lines.length * 20}px">
              {#each b.lines as l, i}
                {@const who = blameOn && !version ? blameStarts.get(b.start + i + 1) : null}
                <div class="ln">{#if blameOn && !version}<span class="who" title={who?.subject}>{#if who}<span class="mono">{who.shortHash}</span> {who.author}{/if}</span>{/if}<span class="n" aria-hidden="true">{b.start + i + 1}</span><code class="c">{@html l || ' '}</code></div>
              {/each}
            </div>
          {/each}
        </div>
      {/if}
    {/if}
  </div>
</section>
{#if historyOpen}
  <History {roomId} {session} {path} {narrow} selected={version} onselect={(c) => { show(c); if (narrow) historyOpen = false }} onclose={() => (historyOpen = false)} />
{/if}
</div>

<style>
  .with-history { flex: 1; display: flex; min-width: 0; min-height: 0; }
  .view { flex: 1; display: flex; flex-direction: column; min-width: 0; min-height: 0; }
  .copy { display: flex; align-items: center; justify-content: center; gap: 6px; }
  .copy.copied, .copy-status { color: var(--sage); }
  .copy-status { min-height: 18px; padding: 0 16px; font-size: 12px; text-align: right; }
  .copy-status.error { color: var(--coral); }
  header { display: flex; align-items: center; gap: 8px; padding: 10px 16px 4px; min-width: 0; }
  .back { width: 32px; height: 32px; display: grid; place-items: center; border-radius: 6px; color: var(--muted); flex: none; transition: background-color var(--motion-fast) var(--ease), color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .back:hover { background: var(--hover); color: var(--ink); }
  }
  .back:active { background: var(--select); }
  .crumbs { display: flex; align-items: center; gap: 2px; min-width: 0; overflow-x: auto; scrollbar-width: none; font-size: 14px; white-space: nowrap; }
  .crumbs::-webkit-scrollbar { display: none; }
  .crumbs button { color: var(--muted); padding: 2px 4px; border-radius: 4px; transition: color var(--motion-fast) var(--ease), background-color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .crumbs button:hover { color: var(--ink); background: var(--hover); }
  }
  .crumbs button:active { background: var(--select); }
  .sep { color: var(--faint); }
  .leaf { color: var(--ink); font-weight: 600; padding: 2px 4px; }
  .spacer { flex: 1; }
  .tool { flex: none; font-size: 12px; font-weight: 600; color: var(--muted); padding: 4px 10px; border-radius: 6px; background: var(--raise); transition: color var(--motion-fast) var(--ease), background-color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .tool:hover { color: var(--ink); background: var(--raise-hover); }
  }
  .tool:active { background: var(--select); }
  .who { flex: none; width: 22ch; padding: 0 8px 0 12px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 11.5px; color: var(--faint); user-select: none; border-right: 1px solid var(--line-soft); }
  .who .mono { color: var(--iris); }
  .version { margin: 0 16px 6px; display: flex; align-items: center; gap: 10px; font-size: 12.5px; color: var(--gold); background: var(--gold-wash); border-radius: 6px; padding: 6px 10px; }
  .version button { margin-left: auto; color: var(--ink); text-decoration: underline; }
  .tool:disabled { opacity: 0.5; }
  .meta { margin: 0; padding: 0 20px 8px; font-size: 12px; color: var(--faint); }
  .body { flex: 1; min-height: 0; overflow: auto; padding: 0 16px 16px; display: flex; flex-direction: column; }
  .code { background: #19181a; border: 1px solid var(--line-soft); border-radius: 8px; padding: 8px 0; width: max-content; min-width: 100%; font-family: var(--mono); font-size: 12.5px; line-height: 1.6; font-variant-ligatures: none; color: var(--ink-2); tab-size: 4; }
  .code.wrap { width: auto; }
  .blk { content-visibility: auto; }
  .ln { display: flex; }
  .n { flex: none; width: calc(var(--gw) + 22px); padding: 0 10px 0 12px; text-align: right; color: #55514b; user-select: none; position: sticky; left: 0; background: #19181a; border-right: 1px solid var(--line-soft); }
  .c { flex: 1; min-width: 0; padding: 0 16px 0 14px; white-space: pre; font: inherit; }
  .wrap .c { white-space: pre-wrap; overflow-wrap: anywhere; padding-left: calc(14px + 2ch); text-indent: -2ch; }
  .tool.on { color: var(--iris); background: var(--iris-wash); }
  .doc { background: var(--side); border: 1px solid var(--line-soft); border-radius: 10px; }
  .image { flex: 1; display: grid; place-items: center; border-radius: 8px; padding: 16px; background: repeating-conic-gradient(#1f1e21 0 25%, #252427 0 50%) 0 0 / 20px 20px; }
  .image img { max-width: 100%; max-height: 70vh; object-fit: contain; }
  .empty { margin: 24px auto; max-width: 420px; text-align: center; background: var(--side); border: 1px solid var(--line-soft); border-radius: 12px; padding: 22px; }
  .empty p { color: var(--muted); font-size: 14px; margin: 6px 0 0; }
  .cut { margin: 0 0 8px; font-size: 12.5px; color: var(--gold); background: var(--gold-wash); border-radius: 6px; padding: 6px 10px; }
  .quiet { color: var(--faint); font-size: 13.5px; padding: 8px 4px; }
  .err { color: var(--coral); padding: 8px 4px; }

  @media (max-width: 760px) {
    header { padding: 6px 8px 2px; flex-wrap: wrap; }
    .back { width: 44px; height: 44px; }
    /* The back button returns to the folder; the name is enough here. */
    .crumbs button, .sep { display: none; }
    .leaf { overflow: hidden; text-overflow: ellipsis; }
    .crumbs { overflow: hidden; flex: 1; min-width: 80px; }
    .spacer { display: none; }
    .tool.copy { width: 44px; height: 44px; padding: 0; }
    .copy-label { display: none; }
    .tool { padding: 10px 12px; }
    .meta { padding: 0 14px 6px; }
    .body { padding: 0 8px 12px; }
    .code { font-size: 12px; }
    .n { width: calc(var(--gw) + 14px); padding: 0 6px 0 6px; }
    .c { padding: 0 8px 0 8px; }
    .wrap .c { padding-left: calc(8px + 2ch); }
  }
</style>
