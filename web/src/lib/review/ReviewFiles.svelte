<script module lang="ts">
  // Where each room or session was last: open folders, the open file, the
  // phone's current folder. It survives a switch to another view.
  interface SavedView { open: string[]; file: string; dir: string; hidden: boolean }
  const memory = new Map<string, SavedView>()
</script>

<script lang="ts">
  // The workspace Files tab: a folder tree next to the file viewer on a wide screen;
  // on a phone, one folder list at a time with a path bar, and the file
  // full width. Folders load one level at a time, when opened.
  import { filterLoaded } from '../files/filterLoaded.ts'
  import { untrack, onDestroy } from 'svelte'
  import { api } from '../../api.ts'
  import FileView from '../files/FileView.svelte'
  import { base, crumbs, isDir, parent, STATUS, statusLetter, treeRows, type Listing, type RepoEntry, type StatusCode, type TreeEntry, type TreeRow } from '../files/repo.ts'
  import { fileHash, parseFileHash } from '../prs/prlink.ts'
  import { routeHash } from '../ui/route.ts'
  import { ago } from '../ui/format.ts'
  import Chip from '../ui/Chip.svelte'
  import Document from '../files/Document.svelte'
  import History from '../files/History.svelte'
  import { markdownDocument } from '../files/preview.ts'
  import { errorText } from '../errorText.ts'
  import type { DirectoryEntry, GitCommit, GitFileStatus, GitStatusResult, WorkspaceFile } from '../api/file.ts'

  interface Props {
    roomId: string
    session?: string
    narrow?: boolean
    active?: boolean
    reveal?: string
    folder?: { path: string } | null
    onpr?: (number: number) => void
  }
  let { roomId, session = '', narrow = false, active = true, reveal = '', folder = null, onpr }: Props = $props()

  const key = untrack(() => `${roomId}|${session}`) // the view is keyed by room and session
  const saved: SavedView = memory.get(key) ?? { open: [''], file: '', dir: '', hidden: false }
  // A file link in the address opens that file, with its folders, in place of the saved view.
  const linked = untrack(() => {
    const l = parseFileHash(window.location.hash)
    return l?.roomId === roomId ? l : null
  })
  let open = $state(new Set(linked && !narrow ? [...saved.open, ...crumbs(parent(linked.file)).map((c) => c.path)] : saved.open))
  let file = $state(linked?.file ?? saved.file)
  let dir = $state(linked && narrow ? parent(linked.file) : saved.dir)
  let shown = $state({ path: linked?.file ?? '', at: linked?.at ?? '', was: linked?.was ?? '' }) // the version FileView shows, for the file at path
  let at = $derived(shown.path === file ? shown.at : '')
  let was = $derived(at ? shown.was : '')
  let hidden = $state(saved.hidden)
  let repoHistory = $state(false) // the whole-repository History panel on the home
  let lists = $state<Record<string, Listing>>({}) // folder -> {entries} | {error} | {loading}
  let root = $state('')
  let focusPath = $state('')
  let filter = $state('')
  let filterInput = $state<HTMLInputElement | undefined>()
  const term = $derived(filter.trim().toLocaleLowerCase())
  let treeEl = $state<HTMLElement | undefined>()

  let status = $state<GitStatusResult | null>(null)
  let statusError = $state('')
  let commits = $state<Record<string, GitCommit>>({})
  let commitError = $state('')
  let readme = $state<WorkspaceFile | null>(null)
  let readmeError = $state('')
  let revision = 0
  let batchActive = false
  let batchQueued = false
  let destroyed = false
  const visible = new Set<string>()
  const requested = new Set<string>()
  let observer: IntersectionObserver | undefined

  // Both layouts report visible paths to one bounded, serial batch loader.
  function observeCommit(node: HTMLElement) {
    observer ??= new IntersectionObserver(entries => {
      for (const entry of entries) {
        const path = entry.target instanceof HTMLElement ? entry.target.dataset.path : undefined
        if (!path) continue
        if (entry.isIntersecting) visible.add(path)
        else visible.delete(path)
      }
      queueBatch()
    })
    observer.observe(node)
    return { destroy() { observer?.unobserve(node); if (node.dataset.path) visible.delete(node.dataset.path) } }
  }

  function queueBatch() {
    if (batchQueued || batchActive || destroyed) return
    batchQueued = true
    queueMicrotask(() => { batchQueued = false; loadCommits() })
  }

  async function loadCommits() {
    if (batchActive || destroyed) return
    const paths = [...visible].filter(path => !requested.has(path) && !changes.get(path)?.untracked).slice(0, 100)
    if (!paths.length) return
    const version = revision
    for (const path of paths) requested.add(path)
    batchActive = true
    try {
      const result = await api.gitLastCommits(roomId, session, paths)
      if (!destroyed && version === revision) commits = { ...commits, ...(result.commits ?? {}) }
    } catch (e) {
      if (!destroyed && version === revision) commitError = errorText(e)
    } finally {
      batchActive = false
      queueBatch()
    }
  }

  async function loadStatus() {
    const version = revision
    try {
      const result = await api.gitStatus(roomId, session)
      if (!destroyed && version === revision) { status = result; statusError = '' }
    } catch (e) {
      if (!destroyed && version === revision) statusError = errorText(e)
    }
  }
  $effect(() => { untrack(loadStatus) })
  onDestroy(() => { destroyed = true; observer?.disconnect() })

  let changes = $derived(new Map<string, GitFileStatus>((status?.files ?? []).map((change): [string, GitFileStatus] => [change.path, change])))
  let latest = $derived(status?.commits?.[0])
  let rootList = $derived(lists[''])
  let readmePath = $derived(rootList?.entries?.find(entry => !isDir(entry) && /^readme\.md$/i.test(entry.name))?.path)
  $effect(() => {
    const path = readmePath
    const home = !file && (!narrow || !dir)
    if (!path || !home || !rootList || rootList.loading) return
    let current = true
    readme = null
    readmeError = ''
    api.readFile(roomId, session, path).then(result => {
      if (current) readme = result
    }, e => { if (current) readmeError = errorText(e) })
    return () => { current = false }
  })

  // The address follows the open file. A first sync only replaces the entry;
  // later changes add one, so Back returns to the earlier file or to home.
  let synced = false
  $effect(() => {
    if (!active) return
    const want = file ? fileHash(roomId, file, at, was, session) : ''
    untrack(() => {
      const l = window.location
      const has = parseFileHash(l.hash)
      if (want ? l.hash !== want : has) window.history[synced ? 'pushState' : 'replaceState'](null, '', want || l.pathname + l.search + routeHash({ room: roomId, view: 'general', session, turn: 0, run: '', review: 'files' }))
      synced = true
    })
  })
  // Back and Forward bring the address back to a file, a version or home.
  function follow() {
    if (!active || new URLSearchParams(window.location.hash.slice(1)).has('pr')) return
    const l = parseFileHash(window.location.hash)
    if (l && l.roomId !== roomId) return
    if (!l) file = ''
    else {
      if (narrow) dir = parent(l.file)
      else showFolder(parent(l.file))
      file = l.file
      shown = { path: l.file, at: l.at, was: l.was }
    }
  }
  $effect(() => {
    window.addEventListener('hashchange', follow)
    return () => window.removeEventListener('hashchange', follow)
  })

  $effect(() => { memory.set(key, { open: [...open], file, dir, hidden }) })

  async function load(path: string, force = false) {
    const known = lists[path]
    if (!force && known && !known.error) return
    lists = { ...lists, [path]: { loading: true, entries: known?.entries } }
    try {
      const res = await api.listEntries(roomId, session, path, hidden)
      root = res.root || root
      lists = { ...lists, [path]: { entries: sortEntries(res.entries ?? []) } }
    } catch (e) {
      lists = { ...lists, [path]: { error: errorText(e) } }
    }
  }

  // The API reports a free-form kind; the tree knows three.
  const repoKind = (kind: string): RepoEntry['kind'] => (kind === 'dir' || kind === 'symlink' ? kind : 'file')
  const sortEntries = (list: readonly DirectoryEntry[]): RepoEntry[] =>
    list.map((e) => ({ ...e, kind: repoKind(e.kind) })).sort((a, b) => ((isDir(b) ? 1 : 0) - (isDir(a) ? 1 : 0)) || a.name.localeCompare(b.name))

  // Load what the saved state shows, and again when "show hidden" flips.
  $effect(() => {
    hidden
    narrow
    untrack(() => {
      lists = {}
      for (const p of narrow ? [dir] : [...open]) load(p, true)
    })
  })
  $effect(() => {
    const d = dir
    if (narrow) untrack(() => load(d))
  })

  function refresh() {
    revision++
    commits = {}
    requested.clear()
    commitError = ''
    loadStatus()
    queueBatch()
    for (const p of Object.keys(lists)) load(p, true)
  }

  // The tree's visible rows; rows that did not change keep their objects.
  let rowCache = new Map<string, TreeRow>()
  let rows = $derived.by((): TreeRow[] => {
    const out = treeRows(lists, open, rowCache)
    rowCache = new Map(out.map((r) => [r.path, r]))
    return term ? filterLoaded(lists, term) : out
  })
  let items = $derived(rows.filter((r): r is TreeEntry => r.kind !== 'note'))

  $effect(() => {
    if (term && !items.some((row) => row.path === focusPath)) focusPath = items[0]?.path ?? ''
  })

  function clearFilter() {
    filter = ''
    filterInput?.focus()
  }

  function toggle(e: { path: string }) {
    const next = new Set(open)
    if (next.has(e.path)) next.delete(e.path)
    else {
      next.add(e.path)
      load(e.path)
    }
    open = next
  }

  // pick opens a row: a folder opens or closes, a file shows.
  function pick(e: RepoEntry & { isDir?: boolean }) {
    focusPath = e.path
    if (e.isDir ?? isDir(e)) {
      if (narrow) dir = e.path
      else if (term) { filter = ''; showFolder(e.path) }
      else toggle(e)
    } else file = e.path
  }

  // Show a folder: on a phone, list it; on a wide screen, open it and its
  // parents in the tree and put focus on it.
  function showFolder(path: string) {
    if (!path) file = ''
    if (narrow) {
      file = ''
      dir = path
      return
    }
    const next = new Set(open)
    for (const c of [{ path: '' }, ...crumbs(path)]) {
      next.add(c.path)
      load(c.path)
    }
    open = next
    focusPath = path
    setTimeout(() => treeEl?.querySelector<HTMLElement>(`[data-path="${CSS.escape(path)}"]`)?.focus(), 30)
  }

  // Arrow keys move through the tree; Right opens a folder or steps in,
  // Left closes it or steps to its parent; Enter and Space open.
  function keydown(ev: KeyboardEvent) {
    const i = items.findIndex((r) => r.path === focusPath)
    const at = (j: number) => {
      const r = items[Math.max(0, Math.min(items.length - 1, j))]
      if (!r) return
      focusPath = r.path
      treeEl?.querySelector<HTMLElement>(`[data-path="${CSS.escape(r.path)}"]`)?.focus()
    }
    const r = items[i]
    if (ev.key === 'ArrowDown') at(i + 1)
    else if (ev.key === 'ArrowUp') at(i - 1)
    else if (ev.key === 'Home') at(0)
    else if (ev.key === 'End') at(items.length - 1)
    else if (ev.key === 'ArrowRight' && r?.isDir) { if (r.expanded) at(i + 1); else if (term) pick(r); else toggle(r) }
    else if (ev.key === 'ArrowLeft' && r) {
      if (r.isDir && r.expanded && !term) toggle(r)
      else { const p = parent(r.path); if (p) at(items.findIndex((x) => x.path === p)) }
    } else if ((ev.key === 'Enter' || ev.key === ' ') && r) pick(r)
    else return
    ev.preventDefault()
  }

  // A small mark per file type, so a long list scans faster.
  function mark(name: string): { t: string; hue: number } {
    const ext = name.includes('.') ? name.split('.').pop()?.toLowerCase() ?? '' : ''
    if (/^(png|jpe?g|gif|webp|svg|ico|avif)$/.test(ext)) return { t: 'img', hue: 2 }
    if (/^(md|markdown|txt|rst)$/.test(ext)) return { t: 'doc', hue: 7 }
    if (/^(json|ya?ml|toml|ini|conf|lock|mod|sum|env)$/.test(ext)) return { t: 'cfg', hue: 6 }
    if (ext) return { t: 'code', hue: (ext.charCodeAt(0) + ext.length) % 8 }
    return { t: 'file', hue: -1 }
  }

  // reveal opens a file from elsewhere (the Changes view), with its folders.
  $effect(() => {
    const r = reveal
    if (!r) return
    untrack(() => {
      if (narrow) dir = parent(r)
      else showFolder(parent(r))
      file = r
      focusPath = r
    })
  })

  // folder shows a directory from elsewhere (the Library view); the root too.
  $effect(() => {
    const f = folder
    if (f) untrack(() => { file = ''; showFolder(f.path) })
  })

  let label = $derived(base(root) || 'repo')
  let phoneList = $derived(lists[dir])
  let phoneEntries = $derived((phoneList?.entries ?? []).filter((entry) => !term || entry.name.toLocaleLowerCase().includes(term)))
  $effect(() => { dir; untrack(() => { filter = '' }) })
</script>

{#snippet icon(e: RepoEntry & { isDir?: boolean })}
  {#if e.isDir ?? isDir(e)}
    <svg class="glyph dir" viewBox="0 0 20 20" width="16" height="16" aria-hidden="true"><path d="M2.5 5.5a1.5 1.5 0 0 1 1.5-1.5h3.6l1.6 1.8H16a1.5 1.5 0 0 1 1.5 1.5v7.2A1.5 1.5 0 0 1 16 16H4a1.5 1.5 0 0 1-1.5-1.5Z" fill="currentColor" /></svg>
  {:else}
    {@const m = mark(e.name)}
    <svg class="glyph file {m.hue >= 0 ? 'hue-' + m.hue : ''}" viewBox="0 0 20 20" width="16" height="16" aria-hidden="true"><path d="M5 2.8h6.2L15.5 7v9.2a1 1 0 0 1-1 1h-9.5a1 1 0 0 1-1-1V3.8a1 1 0 0 1 1-1Z" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linejoin="round" /><path d="M11 3v4.2h4.3" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linejoin="round" /></svg>
  {/if}
{/snippet}

{#snippet repository()}
  <header class="repository" aria-label="Repository">
    {#if status}
      <div class="summary">
        <Chip outline>{status.branch || 'Detached HEAD'}</Chip>
        {#if status.upstream || status.ahead || status.behind}
          <span class="sync">{status.ahead} ahead · {status.behind} behind</span>
        {/if}
        <span class="spacer"></span>
        <button class="count" aria-pressed={repoHistory} onclick={() => (repoHistory = !repoHistory)} title="Show the commits of the whole repository">{status.commitCount} commits</button>
      </div>
      {#if latest}
        {@const prNumber = latest.prNumber}
        <div class="latest">
          <strong>{latest.author}</strong>
          <span class="subject" title={latest.subject}>{latest.subject}</span>
          {#if prNumber && onpr}<button class="pr" onclick={() => onpr(prNumber)}>#{prNumber}</button>{/if}
          <span class="hash mono">{latest.shortHash}</span>
          <span class="age" title={latest.time}>{ago(latest.time)}</span>
        </div>
      {/if}
    {:else if statusError}
      <p class="quiet">Git information is unavailable. {statusError}</p>
    {:else}<p class="quiet">Loading repository…</p>{/if}
    {#if commitError}<p class="err commit-error">Commit details are unavailable. Reload to try again.</p>{/if}
  </header>
{/snippet}

{#snippet rowCommit(path: string)}
  {@const change = changes.get(path)}
  {#if change?.untracked}
    <em class="row-subject untracked">untracked</em>
  {:else}
    <span class="row-subject" title={commits[path]?.subject}>{commits[path]?.subject || ''}</span>
  {/if}
  <span class="age" title={change?.untracked ? undefined : commits[path]?.time}>{!change?.untracked && commits[path] ? ago(commits[path].time) : ''}</span>
  <span class="marker">
    {#if change}
      {@const letter = statusLetter(change)}
      <Chip tone={STATUS[letter].tone} outline title={STATUS[letter].word}>{letter === '?' ? 'U' : letter}</Chip>
    {/if}
  </span>
{/snippet}

{#snippet readmeView()}
  {#if readmePath}
    <section class="readme" aria-label="README">
      <h2>{readmePath}</h2>
      {#if readmeError}<p class="err">{readmeError}</p>
      {:else if !readme}<p class="quiet">Loading README…</p>
      {:else if readme.binary}<p class="quiet">The README is a binary file.</p>
      {:else}<Document html={markdownDocument(readme.text || '')} />{/if}
      {#if readme?.truncated}<p class="quiet">The README preview was truncated.</p>{/if}
    </section>
  {/if}
{/snippet}

{#snippet filterField()}
  <div class="filter">
    <input bind:this={filterInput} bind:value={filter} type="search" aria-label={narrow ? 'Filter this folder' : 'Filter loaded files'} placeholder={narrow ? 'Filter this folder' : 'Filter loaded files'} />
    {#if filter}<button onclick={clearFilter} aria-label="Clear file filter">Clear</button>{/if}
  </div>
  {#if term && !narrow}<p class="filter-help">Only loaded files and folders are included.</p>{/if}
{/snippet}

{#snippet buttons()}
  <button class="ibtn" class:on={hidden} onclick={() => (hidden = !hidden)} title={hidden ? 'Hide dot files' : 'Show dot files'} aria-pressed={hidden} aria-label="Show dot files">
    <svg viewBox="0 0 20 20" width="17" height="17" aria-hidden="true"><path d="M1.8 10S5 4.5 10 4.5 18.2 10 18.2 10 15 15.5 10 15.5 1.8 10 1.8 10Z" fill="none" stroke="currentColor" stroke-width="1.5" /><circle cx="10" cy="10" r="2.5" fill="none" stroke="currentColor" stroke-width="1.5" />{#if !hidden}<path d="M3 17 17 3" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" />{/if}</svg>
  </button>
  <button class="ibtn" onclick={refresh} title="Reload" aria-label="Reload">
    <svg viewBox="0 0 20 20" width="17" height="17" aria-hidden="true"><path d="M16 10a6 6 0 1 1-1.8-4.3M16 3.5v3.2h-3.2" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" /></svg>
  </button>
{/snippet}

{#if narrow}
  <section class="phone">
    {#if file}
      {#key file}
        <FileView {roomId} {session} path={file} rootLabel={label} {narrow} {at} {was} onat={(hash: string, old: string) => (shown = { path: file, at: hash, was: old })} onfolder={showFolder} onback={() => (file = '')} />
      {/key}
    {:else}
      <div class="pbar">
        <nav class="path" aria-label="Folder">
          <button class:cur={!dir} onclick={() => (dir = '')}>{label}</button>
          {#each crumbs(dir) as c}
            <span aria-hidden="true">/</span>
            <button class:cur={c.path === dir} onclick={() => (dir = c.path)}>{c.name}</button>
          {/each}
          {#if session}<span class="tag">worktree</span>{/if}
        </nav>
        {@render buttons()}
      </div>
      {@render repository()}
      {@render filterField()}
      <div class="list">
        {#if dir}
          <button class="row" onclick={() => (dir = parent(dir))}>
            <svg class="glyph" viewBox="0 0 20 20" width="16" height="16" aria-hidden="true"><path d="M12.5 4.5 7 10l5.5 5.5" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" /></svg>
            <span class="name">..</span>
          </button>
        {/if}
        {#if phoneList?.error}
          <p class="err">{phoneList.error}</p>
        {:else if !phoneList?.entries}
          <p class="quiet">Loading…</p>
        {:else if !phoneList.entries.length}
          <p class="quiet">This folder is empty.</p>
        {:else if !phoneEntries.length}
          <p class="quiet" role="status">No matches in this folder.</p>
        {:else}
          {#each phoneEntries as e (e.path)}
            <button class="row" data-path={e.path} use:observeCommit onclick={() => pick(e)}>
              {@render icon(e)}
              <span class="name" class:folder={isDir(e)}>{e.name}</span>
              {@render rowCommit(e.path)}
              {#if isDir(e)}
                <svg class="chev" viewBox="0 0 20 20" width="16" height="16" aria-hidden="true"><path d="m7.5 4.5 5.5 5.5-5.5 5.5" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" /></svg>

              {/if}
            </button>
          {/each}
        {/if}
        {#if !dir && !term}{@render readmeView()}{/if}
      </div>
      {#if repoHistory}<History {roomId} {session} narrow onclose={() => (repoHistory = false)} />{/if}
    {/if}
  </section>
{:else}
  <section class="split" class:home={!file}>
    <aside class="treepane">
      <div class="bar">
        <span class="root" title={root}>
          <svg viewBox="0 0 20 20" width="15" height="15" aria-hidden="true"><path d="M3 4.5h5l1.5 1.8H17v9.2H3Z" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linejoin="round" /></svg>
          <span class="mono">{label}</span>
          {#if session}<span class="tag">worktree</span>{/if}
        </span>
        <span class="spacer"></span>
        {@render buttons()}
      </div>
      {#if !file}{@render repository()}{/if}
      {@render filterField()}
      <div class="tree-scroll">
      {#if term && !items.length}<p class="quiet" role="status">No matches among loaded files.</p>{/if}
      <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
      <div class="tree" role="tree" aria-label="Files in {label}" bind:this={treeEl} onkeydown={keydown} tabindex="-1">
        {#each rows as r (r.path)}
          {#if r.kind === 'note'}
            <p class="note" class:err={r.error} style="padding-left:{14 + r.depth * 14}px">{r.text}</p>
          {:else}
            <button
              class="node"
              class:sel={r.path === file}
              role="treeitem"
              aria-selected={r.path === file}
              aria-level={r.depth + 1}
              aria-expanded={r.isDir ? r.expanded : undefined}
              data-path={r.path}
              use:observeCommit
              tabindex={r.path === (items.some((item) => item.path === focusPath) ? focusPath : items[0]?.path) ? 0 : -1}
              style="padding-left:{8 + r.depth * 14}px"
              title={r.path}
              onclick={(e) => { e.currentTarget.focus(); pick(r) }}
              onfocus={() => (focusPath = r.path)}
            >
              <span class="twist" class:open={r.expanded} aria-hidden="true">
                {#if r.isDir}<svg viewBox="0 0 12 12" width="10" height="10"><path d="m4 2.5 3.5 3.5L4 9.5" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" /></svg>{/if}
              </span>
              {@render icon(r)}
              <span class="name" class:folder={r.isDir}>{r.name}</span>
              {#if !file}{@render rowCommit(r.path)}{/if}
            </button>
          {/if}
        {/each}
      </div>
      {#if !file && !term}{@render readmeView()}{/if}
      </div>
    </aside>
    {#if repoHistory && !file}<History {roomId} {session} onclose={() => (repoHistory = false)} />{/if}
    {#if file}<div class="viewer">
        {#key file}
          <FileView {roomId} {session} path={file} rootLabel={label} {narrow} {at} {was} onat={(hash: string, old: string) => (shown = { path: file, at: hash, was: old })} onfolder={showFolder} />
        {/key}
    </div>{/if}
  </section>
{/if}

<style>
  .filter { display: flex; align-items: center; gap: 8px; margin: 8px 12px; border: 1px solid var(--line); border-radius: 6px; background: var(--field); }
  .filter:focus-within { border-color: var(--iris-edge); }
  .filter input { flex: 1; min-width: 0; width: 100%; padding: 10px; color: var(--ink); font: inherit; font-size: var(--t-body); border: 0; outline: 0; background: transparent; }
  .filter button { min-height: 44px; padding: 0 10px; color: var(--muted); font-size: var(--t-sm); }
  .filter-help { margin: 0 14px 8px; color: var(--muted); font-size: var(--t-sm); }
  .phone .filter input { font-size: var(--t-title); }

  .home .treepane { flex: 1; width: auto; min-width: 0; border-right: 0; background: transparent; }
  .repository { padding: 14px 20px; border-bottom: 1px solid var(--line-soft); }
  .summary, .latest { display: flex; align-items: center; gap: 12px; min-width: 0; }
  .latest { margin-top: 14px; font-size: 12.5px; color: var(--muted); }
  .latest strong { color: var(--ink-2); font-weight: 500; }
  .subject, .row-subject { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .subject { flex: 1; }
  .count { cursor: pointer; padding: 4px; border-radius: 4px; }
  .count[aria-pressed='true'] { color: var(--iris); }
  .sync, .count, .hash, .age { font-size: 12px; color: var(--faint); white-space: nowrap; }
  .pr { color: var(--iris); font-size: 12px; }
  .age { margin-left: auto; }
  .marker { width: 24px; display: flex; justify-content: flex-end; flex: none; }
  .row-subject { color: var(--faint); font-size: 12.5px; flex: 1; }
  .home .node { height: 36px; gap: 10px; border-bottom: 1px solid var(--line-soft); border-radius: 0; }
  .home .node .name { width: 28%; flex: none; }
  .readme { margin: 20px; border: 1px solid var(--line-soft); border-radius: 8px; overflow: hidden; }
  .readme h2 { margin: 0; padding: 12px 18px; font-size: 12.5px; font-weight: 500; border-bottom: 1px solid var(--line-soft); color: var(--muted); }
  .commit-error { font-size: 12px; }
  .phone .repository { padding: 12px 16px; }
  .phone .summary { flex-wrap: wrap; gap: 8px; }
  .phone .latest { flex-wrap: wrap; gap: 6px; }
  .phone .latest .subject { flex-basis: 65%; }
  .phone .row-subject { display: none; }
  .phone .row-subject.untracked { display: block; flex: none; font-size: 11px; }
  .phone .row .age { font-size: 11px; }
  .phone .readme { margin: 14px 6px; }
  .split { flex: 1; display: flex; min-height: 0; }
  .treepane { width: 280px; flex: none; display: flex; flex-direction: column; border-right: 1px solid var(--line-soft); background: #201f22; min-height: 0; }
  .bar { display: flex; align-items: center; gap: 4px; padding: 8px 8px 6px 14px; flex: none; }
  .root { display: inline-flex; align-items: center; gap: 7px; color: var(--muted); font-size: 13px; min-width: 0; }
  .root .mono { color: var(--ink-2); font-weight: 500; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .tag { font-size: 10.5px; font-weight: 600; color: var(--iris); background: var(--iris-wash); border-radius: 4px; padding: 0 5px; }
  .spacer { flex: 1; }
  .ibtn { width: 28px; height: 28px; display: grid; place-items: center; border-radius: 6px; color: var(--faint); flex: none; transition: color var(--motion-fast) var(--ease), background-color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .ibtn:hover { color: var(--ink); background: var(--hover); }
  }
  .ibtn:active { background: var(--select); }
  .ibtn.on { color: var(--iris); }
  .tree-scroll { flex: 1; overflow: auto; min-height: 0; }
  .tree { padding: 2px 6px 16px; outline: none; }
  .node { display: flex; align-items: center; gap: 5px; width: 100%; height: 28px; padding-right: 8px; border-radius: 5px; color: var(--muted); font-size: 13.5px; text-align: left; white-space: nowrap; transition: background-color var(--motion-fast) var(--ease), color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .node:hover { background: var(--hover); color: var(--ink); }
  }
  .node:active { background: var(--select); }
  .node.sel { background: var(--select); color: var(--ink); }
  .node:focus-visible { outline-offset: -2px; }
  .twist { width: 12px; display: grid; place-items: center; color: var(--faint); flex: none; transition: transform var(--motion-fast); }
  .twist.open { transform: rotate(90deg); }
  .glyph { flex: none; color: var(--faint); }
  .glyph.dir { color: #8a8378; }
  .glyph.file { opacity: 0.85; }
  .name { overflow: hidden; text-overflow: ellipsis; min-width: 0; }
  .name.folder { color: var(--ink-2); }
  .note { margin: 0; font-size: 12.5px; color: var(--faint); height: 26px; line-height: 26px; }
  .note.err, .err { color: var(--coral); }
  .viewer { flex: 1; display: flex; min-width: 0; min-height: 0; }

  .phone { flex: 1; display: flex; flex-direction: column; min-height: 0; }
  .pbar { display: flex; align-items: center; padding: 2px 2px 2px 0; border-bottom: 1px solid var(--line-soft); flex: none; }
  .phone .ibtn { width: 44px; height: 44px; }
  .path { flex: 1; min-width: 0; display: flex; align-items: center; gap: 2px; padding: 0 6px 0 10px; overflow-x: auto; white-space: nowrap; color: var(--faint); scrollbar-width: none; }
  .path::-webkit-scrollbar { display: none; }
  .path button { color: var(--muted); padding: 10px 4px; font-size: 14px; }
  .path button.cur { color: var(--ink); font-weight: 600; }
  .list { flex: 1; overflow-y: auto; padding: 4px 6px 16px; }
  .row { display: flex; align-items: center; gap: 12px; width: 100%; min-height: 48px; padding: 0 12px; border-radius: 8px; text-align: left; color: var(--ink-2); font-size: 15px; transition: background-color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .row:hover { background: var(--hover); }
  }
  .row:active { background: var(--select); }
  .row .glyph { width: 18px; height: 18px; }
  .row .name { flex: 1; }
  .chev { color: var(--faint); flex: none; }
  .quiet { color: var(--faint); font-size: 14px; padding: 12px; }
  .list .err { padding: 12px; }
</style>
