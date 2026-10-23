<script lang="ts">
  // The files a pull request changes, each with its diff under a row that
  // opens and closes. It loads the diff once per PR page.
  import { api } from '../../api.js'
  import DiffView from '../files/DiffView.svelte'
  import { parseDiff, diffTotals, split } from '../files/repo.ts'
  import { code, setWrap } from '../files/codeprefs.svelte.ts'
  import SendBar from '../files/SendBar.svelte'
  import { addComment, type DiffComment } from '../files/diffComments.ts'
  import type { DiffFile } from '../files/repo.ts'
  import { errorText } from '../errorText.ts'

  interface Props {
    roomId: string
    number: number
    changeId?: string
    author?: string
    onsent?: () => void | Promise<void>
  }
  // With changeId (an open, tracked change), the owner can comment on lines and send them to the author.
  let { roomId, number, changeId = '', author = '', onsent = () => {} }: Props = $props()
  // The parsed files, or the error that stopped the diff.
  let diff = $state<{ files?: DiffFile[]; truncated?: boolean; error?: string } | null>(null)
  let openFiles = $state(new Set<string>())
  let pending = $state<DiffComment[]>([])
  const deliver = async (comments: DiffComment[]) => {
    await api.sendDiffComments(roomId, changeId, comments)
    await onsent()
  }

  $effect(() => {
    const n = number
    let cancelled = false
    diff = null
    openFiles = new Set()
    api.githubDiff(roomId, n).then((res: { text?: string; truncated?: boolean } | null) => {
      if (cancelled) return
      const files = parseDiff(res?.text)
      diff = { files, truncated: !!res?.truncated }
      if (files.length <= 4) openFiles = new Set(files.map((f) => f.path))
    }).catch((e: unknown) => {
      if (!cancelled) diff = { error: errorText(e) }
    })
    return () => { cancelled = true }
  })
  let files = $derived(diff?.files ?? [])
  let totals = $derived(diffTotals(files))
  function toggleFile(path: string) {
    const next = new Set(openFiles)
    next.has(path) ? next.delete(path) : next.add(path)
    openFiles = next
  }
</script>

{#if !diff}
  <p class="quiet">Loading the diff…</p>
{:else if diff.error}
  <p class="err">{diff.error}</p>
{:else}
  <div class="dtools">
    <div class="sum"><b>{totals.files}</b> {totals.files === 1 ? 'file' : 'files'} changed<span class="plus">+{totals.adds}</span><span class="minus">−{totals.dels}</span></div>
    {#if diff.truncated}<span class="cut">The diff is large, so only its first part shows.</span>{/if}
    <span class="spacer"></span>
    <button class="tool" class:on={code.wrap} aria-pressed={code.wrap} onclick={() => setWrap(!code.wrap)}>Wrap</button>
    <button class="tool" onclick={() => (openFiles = openFiles.size === files.length ? new Set() : new Set(files.map((f) => f.path)))}>{openFiles.size === files.length ? 'Collapse all' : 'Expand all'}</button>
  </div>
  <div class="files">
    {#each files as f (f.path)}
      {@const [dir, name] = split(f.path)}
      <div class="file" class:open={openFiles.has(f.path)}>
        <button class="fhit" onclick={() => toggleFile(f.path)} aria-expanded={openFiles.has(f.path)}>
          <svg class="chev" viewBox="0 0 12 12" width="11" height="11" aria-hidden="true"><path d="m4 2.5 3.5 3.5L4 9.5" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" /></svg>
          <span class="path"><bdi><span class="dir">{dir}</span><span class="fname">{name}</span></bdi></span>
          <span class="counts">{#if f.adds}<span class="plus">+{f.adds}</span>{/if}{#if f.dels}<span class="minus">−{f.dels}</span>{/if}</span>
        </button>
        {#if openFiles.has(f.path)}<div class="fbody"><DiffView file={f} oncomment={changeId ? (c: DiffComment) => (pending = addComment(pending, c)) : null} /></div>{/if}
      </div>
    {/each}
  </div>
  <SendBar bind:pending {author} {deliver} />
{/if}

<style>
  .sum { display: flex; align-items: baseline; gap: 8px; font-size: 13.5px; color: var(--muted); }
  .sum b { color: var(--ink); }
  .sum .plus, .sum .minus { font-family: var(--mono); font-size: 12.5px; }
  .dtools { display: flex; align-items: center; gap: 6px; margin: 10px 0 8px; }
  .spacer { flex: 1; }
  .cut { font-size: 12.5px; color: var(--gold); }
  .tool { font-size: 12px; font-weight: 600; color: var(--muted); padding: 5px 10px; border-radius: 6px; background: var(--raise); }
  .tool:hover { color: var(--ink); background: var(--raise-hover); }
  .tool.on { color: var(--iris); background: var(--iris-wash); }
  .files { display: flex; flex-direction: column; gap: 6px; }
  .file { background: var(--side); border: 1px solid var(--line-soft); border-radius: 10px; overflow: clip; }
  .file.open { border-color: var(--line); }
  .file.open .fhit { position: sticky; top: 0; z-index: 2; background: var(--side); }
  .fhit { width: 100%; display: flex; align-items: center; gap: 10px; padding: 9px 12px; text-align: left; }
  .fhit:hover { background: var(--hover); }
  .chev { color: var(--faint); flex: none; transition: transform 0.12s; }
  .file.open .chev { transform: rotate(90deg); }
  .path { flex: 1; min-width: 0; font-family: var(--mono); font-size: 13px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; direction: rtl; text-align: left; }
  .dir { color: var(--faint); }
  .fname { color: var(--ink); }
  .counts { flex: none; display: flex; gap: 6px; font-family: var(--mono); font-size: 11.5px; font-variant-numeric: tabular-nums; }
  .plus { color: var(--sage); }
  .minus { color: var(--coral); }
  .fbody { border-top: 1px solid var(--line-soft); }
  .quiet { color: var(--faint); font-size: 13.5px; padding: 12px 2px; }
  .err { color: var(--coral); padding: 12px 2px; overflow-wrap: anywhere; }
  @media (max-width: 760px) { .tool { padding: 10px 12px; } }
</style>
