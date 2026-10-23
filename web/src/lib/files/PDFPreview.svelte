<script>
  import { onMount } from 'svelte'
  import { fileURL, authenticatedFetch } from '../../api.js'
  let { roomId, att } = $props()
  let pdf = $state.raw(null)
  let page = $state(1)
  let pageInput = $state('1')
  let jumpError = $state('')
  const jumpErrorId = $props.id()
  $effect(() => { pageInput = String(page); jumpError = '' })
  function parsePageInput(value, total) {
    const text = value.trim()
    if (!/^\d+$/.test(text)) return null
    const number = Number(text)
    return Number.isSafeInteger(number) && number >= 1 && number <= total ? number : null
  }
  function jump(event) {
    event.preventDefault()
    if (!pdf) return
    const number = parsePageInput(pageInput, pages)
    if (number === null) {
      jumpError = `Enter a whole page number from 1 to ${pages}.`
      return
    }
    page = number
    pageInput = String(number)
    jumpError = ''
  }
  function inputKey(event) {
    if (event.isComposing) return
    if (event.key === 'Escape') {
      event.preventDefault()
      event.stopPropagation()
      pageInput = String(page)
      jumpError = ''
    }
  }
  let pages = $state(0)
  let busy = $state(true)
  let error = $state('')
  let pageText = $state('')
  let textView = $state(false)
  let canvas = $state()
  let container = $state()
  let width = $state(600)

  onMount(() => {
    let stopped = false, loading
    const controller = new AbortController()
    const observer = new ResizeObserver(([entry]) => { width = Math.max(200, entry.contentRect.width - 48) })
    observer.observe(container)
    ;(async () => {
      try {
        const [lib, worker, response] = await Promise.all([
          import('pdfjs-dist/legacy/build/pdf.mjs'),
          import('pdfjs-dist/legacy/build/pdf.worker.min.mjs?url'),
          authenticatedFetch(fileURL(roomId, att.id), { signal: controller.signal }),
        ])
        if (!response.ok) throw new Error('The PDF could not be loaded.')
        const data = new Uint8Array(await response.arrayBuffer())
        if (stopped) return
        lib.GlobalWorkerOptions.workerSrc = worker.default
        loading = lib.getDocument({ data, isEvalSupported: false, useSystemFonts: true, enableXfa: false })
        loading.onPassword = () => {
          error = 'This PDF needs a password. Download it to open it in your PDF app.'
          busy = false
          loading.destroy()
        }
        const doc = await loading.promise
        if (stopped) { doc.destroy(); return }
        pages = doc.numPages
        pdf = doc
      } catch (e) {
        if (!stopped) { error ||= `The PDF preview could not open. ${e.message || e}`; busy = false }
      }
    })()
    return () => { stopped = true; controller.abort(); observer.disconnect(); loading?.destroy() }
  })

  $effect(() => {
    const doc = pdf, num = page, fit = width, target = canvas
    if (!doc || !target) return
    let stopped = false, task
    busy = true
    error = ''
    ;(async () => {
      try {
        const current = await doc.getPage(num)
        const base = current.getViewport({ scale: 1 })
        const scale = Math.min(fit / base.width, 2)
        const viewport = current.getViewport({ scale })
        const ratio = Math.min(window.devicePixelRatio || 1, 2, Math.sqrt(8_000_000 / (viewport.width * viewport.height)))
        if (stopped) return
        target.width = Math.ceil(viewport.width * ratio)
        target.height = Math.ceil(viewport.height * ratio)
        target.style.width = `${viewport.width}px`
        target.style.height = `${viewport.height}px`
        task = current.render({ canvasContext: target.getContext('2d'), viewport, transform: [ratio, 0, 0, ratio, 0, 0] })
        await task.promise
        const content = await current.getTextContent()
        if (stopped) return
        pageText = content.items.map((item) => item.str + (item.hasEOL ? '\n' : ' ')).join('')
        busy = false
      } catch (e) {
        if (!stopped) { error = `This page could not be shown. ${e.message || e}`; busy = false }
      }
    })()
    return () => { stopped = true; task?.cancel() }
  })
</script>

<div class="pdf" bind:this={container}>
  <div class="tools">
    <div class="paging">
      <button onclick={() => page--} disabled={!pdf || page <= 1} aria-label="Previous page">←</button>
      <form class="page-jump" onsubmit={jump}>
        <label>Page <input data-esc aria-label="Page number" aria-invalid={!!jumpError} aria-describedby={jumpError ? jumpErrorId : undefined} type="text" inputmode="numeric" enterkeyhint="go" bind:value={pageInput} onkeydown={inputKey} oninput={() => jumpError = ''} disabled={!pdf} /></label>
        <span>of {pages || '…'}</span>
        <button disabled={!pdf}>Go</button>
      </form>
      <button onclick={() => page++} disabled={!pdf || page >= pages} aria-label="Next page">→</button>
    </div>
    <button class:active={textView} onclick={() => textView = !textView} disabled={!pdf} aria-pressed={textView}>Text view</button>
  </div>
  {#if jumpError}<p class="error jump-error" id={jumpErrorId} role="alert">{jumpError}</p>{/if}
  {#if error}<p class="error" role="alert">{error}</p>{/if}
  <div class="pages" aria-busy={busy}>
    {#if busy}<p class="status" role="status">Loading page…</p>{/if}
    <canvas bind:this={canvas} class:hidden={textView || !!error} aria-label={`Page ${page} of ${att.name}`}></canvas>
    {#if textView && !error}<pre>{pageText || (busy ? '' : 'This page has no selectable text.')}</pre>{/if}
  </div>
</div>

<style>
  .pdf { display: flex; flex-direction: column; flex: 1; min-height: 0; }
  .tools { display: flex; flex-wrap: wrap; justify-content: space-between; align-items: center; gap: 12px; padding: 10px 16px; border-bottom: 1px solid var(--line-soft); color: var(--muted); font-size: 12px; }
  .paging { display: flex; align-items: center; gap: var(--s1); font-variant-numeric: tabular-nums; }
  .page-jump, .page-jump label { display: flex; align-items: center; gap: var(--s1); }
  .page-jump input { width: 5ch; min-width: 44px; padding: 5px; border: 1px solid var(--line); border-radius: var(--r-xs); background: var(--side); color: var(--ink); text-align: center; font: inherit; }
  .page-jump input:focus { outline-color: var(--iris); }
  .jump-error { margin: var(--s2) var(--s4); }
  @media (max-width: 760px) { .tools { padding: var(--s2); gap: var(--s1); } .tools button, .page-jump input { min-height: 44px; } .page-jump input { font-size: 16px; } }
  button { padding: 5px 10px; border-radius: 5px; transition: background-color var(--motion-fast) var(--ease), color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    button:hover { background: var(--hover); color: var(--ink); }
  }
  button:active { background: var(--select); }
  button.active { background: var(--select); color: var(--ink); }
  button:disabled { opacity: 0.35; cursor: default; }
  .pages { overflow: auto; flex: 1; min-height: 0; padding: 24px; text-align: center; }
  canvas { max-width: 100%; background: white; border-radius: 2px; box-shadow: 0 5px 24px #0004; vertical-align: top; }
  canvas.hidden { display: none; }
  .status { margin: 0 0 16px; color: var(--muted); font-size: 13px; }
  .error { margin: 20px; color: var(--coral); font-size: 13px; }
  pre { text-align: left; white-space: pre-wrap; overflow-wrap: anywhere; font: 14px/1.8 var(--sans); margin: 0 auto; max-width: 760px; color: var(--ink-2); }
</style>
