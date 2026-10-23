<script>
  import Segmented from '../ui/Segmented.svelte'
  import { dialog } from '../ui/dialog.js'
  import { onMount } from 'svelte'
  import { api, copyText, fileURL, authenticatedFetch } from '../../api.js'
  import { human, extOf } from './attach.js'
  import { code, setWrap } from './codeprefs.svelte.js'
  import { afterOpen } from './afterOpen.ts'
  import VisualView from './VisualView.svelte'
  import Document from './Document.svelte'
  import TablePreview from './TablePreview.svelte'
  import PDFPreview from './PDFPreview.svelte'
  import { previewKind, previewLabel, markdownDocument, documentHTML, formatJSON, TEXT_LIMIT } from './preview.ts'

  let { roomId, att, onjump = null, onclose } = $props()
  let cardReady = $state(false)
  let text = $state('')
  let html = $state('')
  let error = $state('')
  let downloadError = $state('')
  let loading = $state(true)
  let downloading = $state(false)
  let kind = $derived(previewKind(att))
  let image = $derived(kind === 'image')
  let card = $derived(kind === 'card')
  let hasSource = $derived(['markdown', 'table', 'json'].includes(kind))
  // svelte-ignore state_referenced_locally
  let view = $state(att.view === 'source' ? 'source' : 'preview')
  let source = $derived(view === 'source')
  let showTextTools = $derived(!loading && !error && (kind === 'text' || kind === 'json' || (source && hasSource)))
  let copyStatus = $state('')
  let copyTimer
  let copyGeneration = 0
  $effect(() => {
    att
    copyGeneration++
    copyStatus = ''
    return () => clearTimeout(copyTimer)
  })
  async function copyContents() {
    const current = ++copyGeneration
    clearTimeout(copyTimer)
    copyStatus = ''
    try {
      await copyText(text)
      if (current !== copyGeneration) return
      copyStatus = 'Copied contents'
    } catch {
      if (current !== copyGeneration) return
      copyStatus = 'Could not copy'
    }
    copyTimer = setTimeout(() => { copyStatus = '' }, 2000)
  }
  let delimiter = $derived(/\.tsv$/i.test(att.name) || att.mime === 'text/tab-separated-values' ? '\t' : ',')
  let formatted = $derived.by(() => {
    if (kind !== 'json' || !text) return { text, error: '' }
    try { return { text: formatJSON(text), error: '' } }
    catch { return { text, error: 'This file is not valid JSON. Showing the original text.' } }
  })

  onMount(() => {
    let stopped = false
    const controller = new AbortController()
    ;(async () => {
      if (!['markdown', 'table', 'json', 'text', 'word'].includes(kind)) { loading = false; return }
      try {
        if (kind !== 'word' && att.size > TEXT_LIMIT) throw new Error('This file is too large for a text preview. Download it to read the full file.')
        if (kind === 'word') {
          const [module, response] = await Promise.all([
            import('mammoth/mammoth.browser.js'),
            authenticatedFetch(fileURL(roomId, att.id), { signal: controller.signal }),
          ])
          if (!response.ok) throw new Error('The document could not be loaded.')
          const result = await module.default.convertToHtml({ arrayBuffer: await response.arrayBuffer() }, { externalFileAccess: false })
          if (!stopped) html = documentHTML(result.value)
        } else {
          const content = await api.attachmentText(roomId, att.id)
          if (stopped) return
          text = content
          if (kind === 'markdown') html = markdownDocument(content)
        }
      } catch (e) {
        if (!stopped) error = String(e?.message || e)
      } finally {
        if (!stopped) loading = false
      }
    })()
    return () => { stopped = true; controller.abort() }
  })

  async function download(file = att) {
    downloading = true
    downloadError = ''
    try {
      const response = await authenticatedFetch(fileURL(roomId, file.id))
      if (!response.ok) throw new Error('The download failed. Please try again.')
      const url = URL.createObjectURL(await response.blob())
      const link = document.createElement('a')
      link.href = url
      link.download = file.name
      // The app handles document links globally; this download stays local.
      link.onclick = (e) => e.stopPropagation()
      document.body.append(link)
      link.click()
      link.remove()
      setTimeout(() => URL.revokeObjectURL(url), 60_000)
    } catch (e) { downloadError = String(e.message || e) }
    finally { downloading = false }
  }

</script>

<!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
<div class="backdrop">
  <!-- svelte-ignore a11y_click_events_have_key_events -->
  <div class="frame" tabindex="-1" use:dialog={{ close: onclose }} use:afterOpen={() => { cardReady = true }} class:image class:card onclick={(e) => e.stopPropagation()} role="dialog" aria-modal="true" aria-label={att.name}>
    {#if card}
      <VisualView ready={cardReady} {roomId} {att} {onjump} ondownload={download} {downloading} error={downloadError} {onclose} />
    {:else}
    <header>
      <span class="file-mark" aria-hidden="true">{extOf(att.name)}</span>
      <div class="heading">
        <span class="name">{att.name}</span>
        <span class="sub">{previewLabel(kind)} · {human(att.size)}</span>
      </div>
      {#if onjump}<button class="download" onclick={onjump}>Jump to post</button>{/if}
      <button class="download" onclick={() => download()} disabled={downloading} aria-label={`Download ${att.name}`}>
        <svg viewBox="0 0 16 16" width="15" height="15" aria-hidden="true"><path d="M8 2v8m-3-3 3 3 3-3M3 11v3h10v-3" stroke="currentColor" fill="none" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round" /></svg>
        <span>{downloading ? 'Saving…' : 'Download'}</span>
      </button>
      <button class="x" onclick={onclose} aria-label="Close preview">
        <svg viewBox="0 0 16 16" width="14" height="14" aria-hidden="true"><path d="M4 4l8 8M12 4l-8 8" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" /></svg>
      </button>
    </header>
    {#if hasSource || showTextTools}
      <div class="toolbar">
        {#if hasSource}<Segmented small label="Document view" options={[['preview', 'Preview'], ['source', 'Source']]} value={source ? 'source' : 'preview'} onchange={(v) => (view = v)} />{/if}
        {#if showTextTools}
          <div class="source-tools">
            <button class:active={code.wrap} aria-pressed={code.wrap} onclick={() => setWrap(!code.wrap)}>Wrap</button>
            <button onclick={copyContents} title="Copy the original file text">Copy contents</button>
          </div>
          <span class="copy-status" class:error={copyStatus === 'Could not copy'} role="status">{copyStatus}</span>
        {/if}
      </div>
    {/if}
    {#if downloadError}<p class="err" role="alert">{downloadError}</p>{/if}
    <div class="content" class:media={image}>
      {#if image}
        <img src={fileURL(roomId, att.id)} alt={att.name} onerror={() => error = 'This image could not be loaded.'} />
        {#if error}<p class="err" role="alert">{error}</p>{/if}
      {:else if kind === 'pdf'}
        <PDFPreview {roomId} {att} />
      {:else if loading}
        <div class="status" role="status"><span class="loading-dot"></span>Preparing preview…</div>
      {:else if error}
        <div class="status"><strong>Preview unavailable</strong><p class="err" role="alert">{error}</p><button onclick={() => download()}>Download file</button></div>
      {:else if source}
        <pre class="source" class:wrap={code.wrap}>{text}</pre>
      {:else if kind === 'markdown' || kind === 'word'}
        {#if html}<div class="document-scroll"><Document {html} /></div>{:else}<div class="status">This document has no text to preview.</div>{/if}
      {:else if kind === 'table'}
        <TablePreview {text} {delimiter} />
      {:else if kind === 'json' || kind === 'text'}
        {#if formatted.error}<p class="note">{formatted.error}</p>{/if}
        <pre class="source" class:wrap={code.wrap}>{formatted.text}</pre>
      {:else}
        <div class="status">
          <span class="large-mark" aria-hidden="true">{extOf(att.name)}</span>
          <strong>{att.name}</strong>
          <p>{(/\.doc$/i).test(att.name) ? 'Download this Word document to open it. Save it as .docx for an in-app preview.' : 'Download this file to open it in your preferred app.'}</p>
          <button onclick={() => download()}>Download file</button>
        </div>
      {/if}
    </div>
    {/if}
  </div>
</div>

<style>
  .backdrop { position: fixed; inset: 0; z-index: 60; background: rgba(8, 8, 10, 0.82); display: grid; grid-template: minmax(0, 1fr) / minmax(0, 1fr); place-items: center; padding: 32px; }
  .frame { width: min(1040px, 100%); height: min(860px, 100%); min-height: 0; max-width: 100%; max-height: 100%; display: flex; flex-direction: column; background: var(--side); border: 1px solid var(--line); border-radius: 12px; box-shadow: 0 24px 100px #0007; overflow: hidden; outline: none; animation: open var(--motion) var(--ease); }
  @keyframes open { from { opacity: 0; transform: scale(0.98); } }
  .frame.card { width: min(1400px, 100%); height: min(1000px, 100%); }
  .frame.image { width: fit-content; height: auto; min-width: min(480px, 100%); }
  header { display: flex; align-items: center; gap: 12px; padding: 16px 20px; border-bottom: 1px solid var(--line-soft); flex: none; }
  .file-mark { width: 38px; height: 42px; background: var(--raise); border: 1px solid var(--line); border-radius: 6px; display: grid; place-items: center; color: var(--iris); font: 10px var(--mono); flex: none; }
  .heading { min-width: 0; display: flex; flex-direction: column; gap: 3px; flex: 1; }
  .name { font-weight: 500; font-size: 14px; color: var(--ink); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .sub { color: var(--muted); font-size: 11.5px; }
  .download { display: flex; align-items: center; gap: 7px; padding: 7px 10px; color: var(--ink-2); border-radius: 6px; font-size: 12px; transition: background-color var(--motion-fast) var(--ease), color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .download:hover, .x:hover { background: var(--hover); color: var(--ink); }
  }
  .download:active, .x:active { background: var(--select); }
  .download:disabled { opacity: 0.5; }
  .x { width: 30px; height: 30px; border-radius: 6px; display: grid; place-items: center; color: var(--muted); flex: none; transition: background-color var(--motion-fast) var(--ease), color var(--motion-fast) var(--ease); }
  .toolbar { display: flex; align-items: center; gap: 8px 18px; flex-wrap: wrap; padding: 10px 20px; border-bottom: 1px solid var(--line-soft); }
  .source-tools { display: flex; gap: 6px; }
  .source-tools button { min-height: 36px; padding: 6px 10px; border-radius: 6px; background: var(--raise); color: var(--muted); font-size: 12px; }
  .source-tools button.active { color: var(--iris); background: var(--iris-wash); }
  .source-tools button:focus-visible { outline: 2px solid var(--iris-edge); outline-offset: 2px; }
  .copy-status { font-size: 12px; color: var(--sage); }
  .copy-status.error { color: var(--coral); }
  .source.wrap { white-space: pre-wrap; overflow-wrap: anywhere; }
  .content { display: flex; flex-direction: column; flex: 1; min-height: 0; overflow: hidden; background: var(--main); }
  .document-scroll { flex: 1; min-height: 0; overflow: auto; }
  .media { display: block; overflow: auto; text-align: center; background: var(--rail); }
  img { display: block; max-width: 100%; max-height: calc(100dvh - 160px); object-fit: contain; margin: auto; }
  .source { flex: 1; overflow: auto; margin: 0; padding: 24px; font: 12.5px/1.75 var(--mono); color: var(--ink-2); white-space: pre; tab-size: 4; }
  .status { display: flex; flex-direction: column; align-items: center; justify-content: center; gap: 12px; padding: 40px 24px; flex: 1; text-align: center; color: var(--muted); font-size: 13px; overflow-wrap: anywhere; }
  .status strong { color: var(--ink); font-weight: 500; }
  .status p { margin: 0; max-width: 440px; line-height: 1.7; }
  .status button { color: var(--ink); background: var(--raise); border: 1px solid var(--line); border-radius: 6px; padding: 7px 14px; margin-top: 6px; }
  .large-mark { color: var(--iris); font: 18px var(--mono); border: 1px solid var(--line); border-radius: 10px; padding: 20px 14px; background: var(--side); margin-bottom: 10px; }
  .loading-dot { width: 7px; height: 7px; background: var(--iris); border-radius: 50%; }
  .err { color: var(--coral); font-size: 13px; margin: 12px 20px; }
  .note { color: var(--gold); font-size: 12px; margin: 12px 24px 0; }
  @media (max-width: 600px) {
    .backdrop { padding: 10px; }
    .frame { height: 100%; border-radius: 9px; }
    header { padding: 12px; gap: 9px; }
    .file-mark { width: 32px; height: 36px; font-size: 9px; }
    .download span { display: none; }
    .download { padding: 7px; }
    .toolbar { padding: 8px 12px; }
    .source-tools button { min-height: 44px; }
    .source { padding: 16px; font-size: 11.5px; }
  }
</style>
