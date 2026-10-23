<script>
  // The commits of one file, or of the whole repository when `path` is empty.
  // A side panel on a wide screen, a bottom sheet on a phone. A file's rows
  // can be selected to show that version; repository rows are only a list.
  import { dialog } from '../ui/dialog.ts'
  import { api } from '../../api.js'
  import { ago } from '../ui/format.ts'
  import Row from '../ui/Row.svelte'
  import Chip from '../ui/Chip.svelte'
  import LoadOlder from '../ui/LoadOlder.svelte'

  let { roomId, session = '', path = '', narrow = false, selected = null, onselect = null, onclose } = $props()

  function modal(node, narrow) {
    let active
    function update(enabled) {
      active?.destroy()
      active = enabled ? dialog(node, {
        close: () => onclose(),
        initial: () => node.querySelector('.x'),
      }) : null
    }
    update(narrow)
    return { update, destroy() { active?.destroy() } }
  }

  let commits = $state([])
  let cursor = $state('')
  let earlier = $state(false)
  let loading = $state(true)
  let error = $state('')

  async function load(before = '') {
    const p = path
    loading = true
    try {
      const page = await api.gitHistory(roomId, session, p, before, 50)
      if (p !== path) return
      commits = before ? [...commits, ...page.commits] : page.commits
      cursor = page.cursor
      earlier = page.earlier
      error = ''
    } catch (e) {
      if (p === path) error = String(e?.message || e)
    } finally {
      if (p === path) loading = false
    }
  }
  $effect(() => {
    path
    commits = []
    earlier = false
    load()
  })
</script>

<div class="history-wrap" class:backdrop={narrow}>
<aside class="history" class:sheet={narrow} role={narrow ? 'dialog' : undefined} aria-modal={narrow ? 'true' : undefined} tabindex="-1" use:modal={narrow} aria-label="History">
  <header>
    <strong>History</strong>
    <span class="scope" title={path}>{path || 'whole repository'}</span>
    <button class="x" onclick={onclose} aria-label="Close History">
      <svg viewBox="0 0 16 16" width="14" height="14" aria-hidden="true"><path d="M4 4l8 8M12 4l-8 8" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" /></svg>
    </button>
  </header>
  <div class="list">
    {#if error}<p class="err">{error}</p>{/if}
    {#each commits as c (c.hash)}
      {#snippet lead()}<span class="hash mono">{c.shortHash}</span>{/snippet}
      {#snippet meta()}
        <span>{c.author}</span>
        <span title={c.time}>{ago(c.time)}</span>
        {#if c.added || c.deleted}<span class="lines"><span class="add">+{c.added}</span> <span class="del">−{c.deleted}</span></span>{/if}
        {#if c.moved}<Chip outline title={c.path}>renamed</Chip>{/if}
      {/snippet}
      <Row text={c.subject} {lead} {meta} selected={onselect ? selected?.hash === c.hash : undefined} onclick={onselect ? () => onselect(selected?.hash === c.hash ? null : c) : null} />
    {:else}
      {#if loading}<p class="quiet">Loading history…</p>
      {:else if !error}<p class="quiet">No commits yet.</p>{/if}
    {/each}
    {#if commits.length}<LoadOlder older={earlier} {loading} onclick={() => load(cursor)} />{/if}
  </div>
</aside>
</div>

<style>
  .history { width: 340px; flex: none; display: flex; flex-direction: column; min-height: 0; border-left: 1px solid var(--line-soft); background: var(--side); }
  header { display: flex; align-items: center; gap: 8px; padding: 12px 12px 8px 16px; flex: none; }
  header strong { font-weight: 600; font-size: 13.5px; color: var(--ink); }
  .scope { flex: 1; min-width: 0; font-size: 12px; color: var(--faint); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .x { width: 28px; height: 28px; display: grid; place-items: center; border-radius: 6px; color: var(--muted); flex: none; transition: background-color var(--motion-fast) var(--ease), color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .x:hover { background: var(--hover); color: var(--ink); }
  }
  .x:active { background: var(--select); }
  .list { flex: 1; overflow-y: auto; padding: 0 6px 12px; }
  .hash { font-size: 11.5px; color: var(--iris); }
  .lines { display: inline-flex; gap: 4px; }
  .add { color: var(--sage); }
  .del { color: var(--coral); }
  .quiet { color: var(--faint); font-size: 13px; padding: 12px; }
  .err { color: var(--coral); font-size: 13px; padding: 12px; }
  .history-wrap { display: contents; }
  .history-wrap.backdrop { position: fixed; inset: 0; z-index: 55; background: rgba(8, 8, 10, 0.5); display: flex; align-items: flex-end; }
  .sheet { width: 100%; max-height: 70dvh; border-left: 0; border-top: 1px solid var(--line); border-radius: 14px 14px 0 0; box-shadow: 0 -12px 40px #0006; }
  .sheet .x { width: 44px; height: 44px; }
</style>
