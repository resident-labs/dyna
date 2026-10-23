<script>
  import { tick } from 'svelte'
  import { documentCopy } from './documentCopy.ts'
  import { copyText, openExternal } from '../../api.ts'
  let { html } = $props()
  let article
  $effect(() => {
    html
    const root = article
    let active = true
    let cleanup = () => {}
    tick().then(() => {
      if (active && root) cleanup = documentCopy(root, copyText)
    })
    return () => { active = false; cleanup() }
  })

  function click(e) {
    const link = e.target.closest?.('a[href]')
    if (!link) return
    e.preventDefault()
    e.stopPropagation()
    const href = link.getAttribute('href')
    if (href.startsWith('#')) {
      let id
      try { id = decodeURIComponent(href.slice(1)) } catch { return }
      const target = e.currentTarget.querySelector(`[id="${CSS.escape(id)}"], [name="${CSS.escape(id)}"]`)
      target?.scrollIntoView({ block: 'start', behavior: 'instant' })
    } else if (/^https?:\/\//i.test(href)) openExternal(href)
  }
</script>

<!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_noninteractive_element_interactions -->
<article bind:this={article} class="document prose" onclick={click}>{@html html}</article>

<style>
  .document { width: min(100%, 800px); margin: 0 auto; padding: 38px 44px 64px; font-size: 15px; line-height: 1.8; }
  .document :global(h1) { font-size: 2em; letter-spacing: -0.035em; margin: 0 0 0.7em; }
  .document :global(h2) { font-size: 1.4em; margin: 1.8em 0 0.6em; padding-bottom: 0.4em; border-bottom: 1px solid var(--line-soft); }
  .document :global(h3), .document :global(h4), .document :global(h5), .document :global(h6) { color: var(--ink); margin: 1.5em 0 0.5em; font-weight: 600; }
  .document :global(p) { margin: 0 0 1em; }
  .document :global(ul), .document :global(ol) { margin: 0.5em 0 1em; padding-left: 1.6em; }
  .document :global(li) { margin: 0.3em 0; }
  .document :global(blockquote) { border-color: var(--iris-edge); background: var(--iris-wash); padding: 12px 18px; border-radius: 0 6px 6px 0; margin: 1.2em 0; }
  .document :global(blockquote > :last-child) { margin-bottom: 0; }
  .document :global(.document-code) { position: relative; }
  .document :global(.document-copy) { position: absolute; right: 8px; top: 6px; min-height: 44px; padding: 0 12px; font-size: 12px; color: var(--muted); border-radius: 6px; background: var(--raise); transition: color var(--motion-fast) var(--ease), background-color var(--motion-fast) var(--ease); }
  .document :global(.document-copy:hover) { color: var(--ink); background: var(--raise-hover); }
  .document :global(.document-copy:focus-visible) { outline: 2px solid var(--iris-edge); outline-offset: 2px; }
  .document :global(.document-copy-status) { position: absolute; left: 18px; top: 18px; font-size: 12px; color: var(--sage); }
  .document :global(.document-copy-status.error) { color: var(--coral); }
  .document :global(pre) { line-height: 1.65; padding: 58px 18px 18px; margin: 1em 0; }
  .document :global(pre code) { font-size: 12.5px; }
  .document :global(table) { display: block; max-width: 100%; overflow: auto; border-spacing: 0; margin: 1em 0; font-size: 0.9em; }
  .document :global(th), .document :global(td) { padding: 8px 12px; min-width: 90px; }
  .document :global(th) { background: var(--raise); }
  .document :global(tr:nth-child(even)) { background: rgba(255, 255, 255, 0.02); }
  .document :global(img) { display: block; max-width: 100%; height: auto; border-radius: 6px; margin: 1em 0; }
  .document :global(input[type="checkbox"]) { accent-color: var(--sage); margin-right: 6px; }
  .document :global(a:focus-visible) { outline: 2px solid var(--iris-edge); outline-offset: 3px; }
  @media (max-width: 600px) { .document { padding: 24px 20px 40px; font-size: 14px; } }
</style>
