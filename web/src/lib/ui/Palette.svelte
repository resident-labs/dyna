<script lang="ts">
  import { dialog as modal } from './dialog.ts'
  // The command palette: one box for rooms, messages and commands, on the desktop and
  // on a phone. "New room" pushes a path view: type a folder path, see the
  // folders below it, and make a room for the repo there. Backspace on an
  // empty input goes back one view.
  //
  // Props:
  //   rooms     roomsvc.Room[] (already ordered; archived last)
  //   commands  [{ id, label, hint?, terms?, icon?, danger?, run }] the
  //             app's own commands; icon is an SVG path on a 20 px grid.
  //             "New room" is built in.
  //   targets   paletteTargets.ts rows: the open room's sessions, runs and
  //             PRs, shown once the owner types
  //   onroom    (roomId, created?) a room row was chosen
  //   ontarget  (target) a session, run or PR row was chosen
  //   onclose   the palette should close
  import { onMount, tick } from 'svelte'
  import { api } from '../../api.ts'
  import { isPathText, splitPath, folderRows, parentOf, atRoot, showHome, matches } from './palette.ts'
  import { findTargets, GROUPS } from './paletteTargets.ts'
  import { roomLabel, stamp } from './format.ts'
  import { isFeedSearch } from '../api/guards.ts'
  import { speaker } from './posts.ts'
  import { snippet } from './searchSnippet.ts'
  import { errorText } from '../errorText.ts'
  import type { BrowseEntry, PathCheck } from '../api/file.ts'
  import type { FeedSearch, Post, Room } from '../api/room.ts'
  import type { Target } from './paletteTargets.ts'

  // One command the app offers here; "New room" is one of them.
  interface Command {
    id: string
    label: string
    hint?: string
    terms?: string
    icon?: string
    danger?: boolean
    run: (arg?: string) => unknown
  }

  // A row of the root list, or of the path list.
  type Row =
    | { kind: 'room'; r: Room }
    | { kind: 'target'; t: Target }
    | { kind: 'message'; p: Post }
    | { kind: 'cmd'; c: Command }
  type PathRow = { kind: 'up' } | { kind: 'dir'; f: BrowseEntry }

  interface Listing {
    dir: string
    entries: BrowseEntry[]
    error: string
  }
  // check says what the typed path is, or why it could not be read.
  type PathCheckState = { path: string; error?: string } & Partial<PathCheck>
  interface SearchResults extends FeedSearch {
    roomId: string
    query: string
  }
  interface Status {
    tone: 'ok' | 'warn' | 'err'
    text: string
  }

  interface Props {
    rooms?: Room[]
    commands?: Command[]
    targets?: Target[]
    roomId?: string
    roomName?: string
    initial?: string
    // path, when set, opens straight in the New room path view with that
    // text ("" for the home folder).
    path?: string | null
    onroom: (roomId: string, created?: boolean) => unknown
    ontarget: (target: Target) => unknown
    onmessage: (postId: string) => unknown
    onclose: () => void
  }
  let { rooms = [], commands = [], targets = [], roomId = '', roomName = '', initial = '', path = null, onroom, ontarget, onmessage, onclose }: Props = $props()

  let step = $state<'root' | 'path' | 'confirm'>('root') // root | path | confirm
  let text = $state(initial)
  let pick = $state(0)
  let input = $state<HTMLInputElement | undefined>()
  let list = $state<HTMLDivElement | undefined>()
  let home = $state('')
  let listing = $state<Listing | null>(null)
  let check = $state<PathCheckState | null>(null)
  let busy = $state(false)
  let error = $state('')
  let target = $state('') // the path being confirmed
  let name = $state('')
  let githubAccess = $state<'read-only' | 'read-write'>('read-only')
  let results = $state<SearchResults | null>(null)
  let searchError = $state('')
  let searching = $state(false)
  let asked = ''
  let searchToken = 0
  let searchTimer: ReturnType<typeof setTimeout> | undefined

  const newRoom: Command = { id: 'new-room', label: 'New room', hint: 'Type a folder path', terms: 'add create project folder path repo', icon: 'M10 4.5v11M4.5 10h11', run: openPath }
  // A command without its own icon gets a small chevron.
  const CMD = 'm8 5.5 4.5 4.5L8 14.5'
  // A person for a session, a play mark for a run, a branch for a PR.
  const TARGET_ICON: Record<Target['kind'], string> = {
    session: 'M10 9.5a3 3 0 1 0 0-6 3 3 0 0 0 0 6ZM4.5 16.5c.6-3 2.8-4.7 5.5-4.7s4.9 1.7 5.5 4.7',
    run: 'M6.5 4.5v11l9-5.5Z',
    pr: 'M6 4.5a1.5 1.5 0 1 1-3 0 1.5 1.5 0 0 1 3 0ZM6 15.5a1.5 1.5 0 1 1-3 0 1.5 1.5 0 0 1 3 0ZM17 15.5a1.5 1.5 0 1 1-3 0 1.5 1.5 0 0 1 3 0ZM4.5 6v8M15.5 14V8.5a2.5 2.5 0 0 0-2.5-2.5h-3M11.5 4 9.5 6l2 2',
  }
  const all = $derived([newRoom, ...commands])

  // ---- root view ----
  const roomRows = $derived(rooms.filter((r) => !text.trim() || matches(text, `${roomLabel(r)} ${r.name}`, r.repo)))
  const cmdRows = $derived(all.filter((c) => !text.trim() || matches(text, c.label, c.terms)))
  const targetRows = $derived(findTargets(text, targets))
  const messageQuery = $derived(step === 'root' && roomId ? text.trim() : '')
  const messageRows = $derived(results?.roomId === roomId && results?.query === messageQuery ? results.posts : [])
  const rows = $derived<Row[]>([...roomRows.map((r): Row => ({ kind: 'room', r })), ...targetRows.map((t): Row => ({ kind: 'target', t })), ...messageRows.map((p): Row => ({ kind: 'message', p })), ...cmdRows.map((c): Row => ({ kind: 'cmd', c }))])

  // Search the whole room log. Changing the query, room or view invalidates
  // an old request before the next typing pause starts.
  $effect(() => {
    const query = messageQuery
    const id = roomId
    searchToken++
    results = null
    searchError = ''
    searching = !!query
    asked = ''
    if (query) searchTimer = setTimeout(() => searchMessages(query, id), 200)
    return () => { searchToken++; clearTimeout(searchTimer) }
  })

  async function searchMessages(query = messageQuery, id = roomId) {
    clearTimeout(searchTimer)
    if (!query || !id) return
    const token = ++searchToken
    asked = query
    searching = true
    searchError = ''
    try {
      const found: unknown = await api.searchFeed(id, query)
      if (token !== searchToken) return
      if (!isFeedSearch(found)) throw new Error('The server sent search results this page cannot read.')
      results = { roomId: id, query, ...found }
      pick = Math.min(pick, Math.max(0, rows.length - 1))
    } catch (e) {
      if (token === searchToken) searchError = errorText(e)
    } finally {
      if (token === searchToken) searching = false
    }
  }

  // ---- path view ----
  const pathMode = $derived(step === 'path' && isPathText(text))
  const parts = $derived(splitPath(text))
  const folders = $derived(pathMode && listing?.dir === parts.dir ? folderRows(listing.entries, parts.filter) : [])
  const pathRows = $derived<PathRow[]>([
    ...(pathMode && parts.filter === '' && !atRoot(parts.dir) ? [{ kind: 'up' as const }] : []),
    ...folders.map((f): PathRow => ({ kind: 'dir', f })),
  ])
  // pick -1 means no row is highlighted: Enter submits the typed path.
  const count = $derived(step === 'root' ? rows.length : step === 'path' ? pathRows.length : 0)

  onMount(async () => {
    try {
      home = (await api.browseInfo())?.home ?? ''
    } catch {}
    if (path !== null) {
      openPath()
      if (path) {
        text = path.endsWith('/') ? path : path + '/'
        focusEnd()
      }
      return
    }
  })

  function openPath() {
    step = 'path'
    error = ''
    pick = -1
    listing = null
    check = null
    text = home ? showHome(home + '/', home) : '~/'
    focusEnd()
  }

  async function focusEnd() {
    await tick()
    input?.focus()
    input?.setSelectionRange(text.length, text.length)
  }

  function back() {
    if (step === 'confirm') {
      step = 'path'
      error = ''
      pick = -1
      focusEnd()
      return
    }
    step = 'root'
    text = ''
    pick = 0
    error = ''
    focusEnd()
  }

  // List the folder the text points into. A stale answer is dropped.
  let listToken = 0
  $effect(() => {
    if (!pathMode) return
    const dir = parts.dir
    if (listing?.dir === dir) return
    const token = ++listToken
    api
      .browseDirs(dir)
      .then((b) => { if (token === listToken) listing = { dir, entries: b.entries ?? [], error: '' } })
      .catch((e) => { if (token === listToken) listing = { dir, entries: [], error: errorText(e) } })
  })

  // Say what the typed path is, after a short pause in typing.
  let checkToken = 0
  $effect(() => {
    if (!pathMode) return
    const path = text
    const token = ++checkToken
    const t = setTimeout(() => {
      api
        .inspectPath(path)
        .then((c) => { if (token === checkToken) check = { path, ...c } })
        .catch((e) => { if (token === checkToken) check = { path, error: errorText(e) } })
    }, 200)
    return () => clearTimeout(t)
  })

  // Typing never keeps a stale highlight: root rows start at the first row,
  // path rows start with none, so Enter submits what was typed.
  function typed() {
    error = ''
    pick = step === 'root' ? 0 : -1
  }

  async function scrollPick() {
    await tick()
    list?.querySelector<HTMLElement>('[aria-selected=true]')?.scrollIntoView({ block: 'nearest' })
  }

  function move(d: number) {
    if (!count) return
    pick = pick < 0 && d < 0 ? count - 1 : Math.min(count - 1, Math.max(0, pick + d))
    scrollPick()
  }

  function runRow(row: Row | PathRow | undefined) {
    if (!row) return
    if (row.kind === 'room') {
      onroom(row.r.id)
      onclose()
    } else if (row.kind === 'target') {
      onclose()
      ontarget(row.t)
    } else if (row.kind === 'message') {
      onclose()
      onmessage(row.p.id)
    } else if (row.kind === 'cmd') {
      if (row.c.id === 'new-room') return void row.c.run()
      onclose()
      row.c.run()
    } else if (row.kind === 'up') {
      text = parentOf(parts.dir, home)
      pick = -1
      focusEnd()
    } else if (row.kind === 'dir') {
      text = parts.dir + row.f.name + '/'
      pick = -1
      focusEnd()
    }
  }

  // submit looks at the typed path. A repo that already has a room opens it.
  // Anything else goes to the confirm view, where the name can be edited.
  async function submit() {
    if (!pathMode || busy) return
    const path = text.trim()
    busy = true
    error = ''
    try {
      const c = await api.inspectPath(path)
      if (c.roomId) {
        onroom(c.roomId)
        onclose()
        return
      }
      check = { path, ...c }
      target = path
      name = c.name ?? ''
      githubAccess = 'read-only'
      step = 'confirm'
      await tick()
      input?.blur()
    } catch (e) {
      error = errorText(e)
    } finally {
      busy = false
    }
  }

  async function create(gitInit: boolean) {
    busy = true
    error = ''
    try {
      const made = await api.createRoom({ path: target, name: name.trim(), gitInit, githubAccess })
      onroom(made.id, true)
      onclose()
    } catch (e) {
      error = errorText(e)
    } finally {
      busy = false
    }
  }

  function keydown(e: KeyboardEvent) {
    if (e.isComposing || e.keyCode === 229) return
    if (step === 'confirm') return
    if (e.key === 'ArrowDown') {
      e.preventDefault()
      move(1)
    } else if (e.key === 'ArrowUp') {
      e.preventDefault()
      move(-1)
    } else if (e.key === 'Backspace' && text === '' && step !== 'root') {
      e.preventDefault()
      back()
    } else if (e.key === 'Enter') {
      e.preventDefault()
      if (step === 'path') {
        if ((e.ctrlKey || e.metaKey) || pick < 0) submit()
        else runRow(pathRows[pick])
      } else if (rows[pick]) runRow(rows[pick])
      else if (messageQuery && (asked !== messageQuery || searchError)) searchMessages()
    }
  }

  const placeholder = $derived(step === 'root' ? (roomId ? 'Search sessions, messages and commands' : 'Search rooms and commands') : 'Enter a folder path, for example ~/code/my-app')
  const status = $derived.by((): Status | null => {
    if (!pathMode || !check || check.path !== text) return null
    if (check.error) return { tone: 'err', text: check.error }
    if (check.state === 'repo') return { tone: 'ok', text: `Git repository${check.repo ? ' · ' + showHome(check.repo, home) : ''}` }
    if (check.state === 'notgit') return { tone: 'warn', text: 'Not a git repository' }
    return { tone: 'warn', text: 'This folder does not exist yet' }
  })
</script>


<!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
<div class="backdrop">
  <!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
  <div class="card" role="dialog" aria-modal="true" aria-label="Command palette" tabindex="-1" onclick={(e) => e.stopPropagation()} use:modal={{ close: onclose, initial: () => input }}>
    {#if step === 'confirm'}
      <div class="confirm">
        <button class="back" onclick={back}><svg viewBox="0 0 20 20" width="16" height="16" aria-hidden="true"><path d="M12.5 4.5 7 10l5.5 5.5" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" /></svg>Back</button>
        <h2>New room</h2>
        <p class="path mono">{showHome(target, home)}</p>
        {#if check?.state === 'repo'}
          <p class="note ok">Git repository{check.repo ? ' · ' + showHome(check.repo, home) : ''}</p>
        {:else if check?.state === 'notgit'}
          <p class="note warn">Not a git repository</p>
          <p class="help">A room needs a repository. Dyna can run <span class="mono">git init</span> in this folder.</p>
        {:else}
          <p class="note warn">This folder does not exist</p>
          <p class="help">Dyna can create it, run <span class="mono">git init</span> in it, and make the room.</p>
        {/if}
        <label class="lab" for="room-name">Room name</label>
        <!-- svelte-ignore a11y_autofocus -->
        <input id="room-name" class="field" bind:value={name} placeholder="from the folder name" autofocus autocomplete="off" autocapitalize="off" spellcheck="false" onkeydown={(e) => { if (e.key === 'Enter') { e.preventDefault(); create(check?.state !== 'repo') } }} />
        <label class="lab" for="room-github-access">GitHub access</label>
        <select id="room-github-access" class="field" bind:value={githubAccess} disabled={busy} aria-describedby="room-github-help">
          <option value="read-only">Read-only</option>
          <option value="read-write">Read and write</option>
        </select>
        <p id="room-github-help" class="help">Read-only lets Dyna read pull requests, checks and issues. Read and write also lets Dyna push, comment, change pull requests and merge.</p>
        {#if error}<p class="err">{error}</p>{/if}
        <div class="actions">
          {#if check?.state === 'repo'}
            <button class="go" disabled={busy} onclick={() => create(false)}>{busy ? 'Working…' : 'Create room'}</button>
          {:else if check?.state === 'notgit'}
            <button class="go" disabled={busy} onclick={() => create(true)}>{busy ? 'Working…' : 'Make it a git repository and create room'}</button>
          {:else}
            <button class="go" disabled={busy} onclick={() => create(true)}>{busy ? 'Working…' : 'Create folder and room'}</button>
          {/if}
        </div>
      </div>
      <div class="foot keyboard-help"><span class="keys"><span><kbd>Enter</kbd> Create room</span><span><kbd>Esc</kbd> Close</span></span></div>
    {:else}
      <div class="top">
        {#if step === 'path'}
          <button class="back" onclick={back} aria-label="Back to the palette"><svg viewBox="0 0 20 20" width="18" height="18" aria-hidden="true"><path d="M12.5 4.5 7 10l5.5 5.5" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" /></svg></button>
        {:else}
          <svg class="lens" viewBox="0 0 20 20" width="18" height="18" aria-hidden="true"><circle cx="8.8" cy="8.8" r="5.3" fill="none" stroke="currentColor" stroke-width="1.7" /><path d="m12.8 12.8 3.7 3.7" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" /></svg>
        {/if}
        <input
          bind:this={input}
          class="q"
          bind:value={text}
          oninput={typed}
          onkeydown={keydown}
          {placeholder}
          aria-label={placeholder}
          role="combobox"
          aria-expanded="true"
          aria-controls="palette-list"
          aria-activedescendant={pick >= 0 && pick < count ? `pal-${pick}` : undefined}
          aria-busy={step === 'root' && searching}
          autocomplete="off"
          autocapitalize="off"
          autocorrect="off"
          spellcheck="false"
          enterkeyhint={step === 'path' ? 'go' : 'search'}
        />
        <button class="x" onclick={onclose} aria-label="Close"><svg viewBox="0 0 16 16" width="14" height="14" aria-hidden="true"><path d="M4 4l8 8M12 4l-8 8" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" /></svg></button>
      </div>
      {#if status}<p class="status {status.tone}">{status.text}</p>{/if}
      {#if error}<p class="status err">{error}</p>{/if}
      <div class="list" id="palette-list" role="listbox" bind:this={list}>
        {#if step === 'root'}
          {#if roomRows.length}
            <div class="group">Rooms</div>
            {#each roomRows as r, i}
              <button id="pal-{i}" role="option" aria-selected={pick === i} class="row" class:on={pick === i} class:arch={r.archived} onmousemove={() => (pick = i)} onclick={() => runRow(rows[i])}>
                <span class="hash">#</span>
                <span class="name">{roomLabel(r)}</span>
                {#if r.repo}<span class="sub mono">{showHome(r.repo, home)}</span>{/if}
                <span class="end">
                  {#if r.running}<span class="live" title="{r.running} working"></span>{/if}
                  {#if r.pinned}<span class="tag">pinned</span>{/if}
                  {#if r.archived}<span class="tag">archived</span>{/if}
                  {#if r.unread}<span class="badge" aria-label="{r.unread} unread">{r.unread}</span>{/if}
                </span>
              </button>
            {/each}
          {/if}
          {#each targetRows as t, j (t.kind + t.key)}
            {@const i = roomRows.length + j}
            {#if t.kind !== targetRows[j - 1]?.kind}<div class="group">{GROUPS[t.kind]}</div>{/if}
            <button id="pal-{i}" role="option" aria-selected={pick === i} class="row" class:on={pick === i} onmousemove={() => (pick = i)} onclick={() => runRow(rows[i])}>
              <svg class="icon" viewBox="0 0 20 20" width="18" height="18" aria-hidden="true"><path d={TARGET_ICON[t.kind]} fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" /></svg>
              <span class="name">{t.label}</span>
              {#if t.hint}<span class="sub">{t.hint}</span>{/if}
            </button>
          {/each}
          {#if messageQuery}
            <div class="group">Messages · {roomName}</div>
            {#if searchError}
              <div class="failure"><p class="empty err" role="alert">{searchError}</p><button class="retry" onclick={() => searchMessages()}>Retry</button></div>
            {:else if searching}
              <p class="empty" role="status">Searching room messages…</p>
            {:else if !messageRows.length}
              <p class="empty">No messages match “{messageQuery}” in this room.</p>
            {/if}
            {#each messageRows as p, j (p.id)}
              {@const i = roomRows.length + targetRows.length + j}
              <button id="pal-{i}" role="option" aria-selected={pick === i} class="row message" class:on={pick === i} onmousemove={() => (pick = i)} onclick={() => runRow(rows[i])}>
                <span class="excerpt">{#each snippet(p.text, messageQuery) as part}{#if part.hit}<mark>{part.text}</mark>{:else}{part.text}{/if}{/each}</span>
                <span class="message-meta"><span class="hue-{p.fromHue}">{speaker(p, roomName)}</span><time>{stamp(p.ts)}</time></span>
              </button>
            {/each}
            {#if messageRows.length && results?.more}<p class="empty">Showing the newest {messageRows.length} matches. Add words to narrow the search.</p>{/if}
          {/if}
          {#if cmdRows.length}
            <div class="group">Commands</div>
            {#each cmdRows as c, j}
              {@const i = roomRows.length + targetRows.length + messageRows.length + j}
              <button id="pal-{i}" role="option" aria-selected={pick === i} class="row" class:on={pick === i} class:danger={c.danger} onmousemove={() => (pick = i)} onclick={() => runRow(rows[i])}>
                <svg class="icon" viewBox="0 0 20 20" width="18" height="18" aria-hidden="true"><path d={c.icon || CMD} fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" /></svg>
                <span class="name">{c.label}</span>
                {#if c.hint}<span class="sub">{c.hint}</span>{/if}
              </button>
            {/each}
          {/if}
          {#if !rows.length && !messageQuery}<p class="empty">Nothing matches “{text.trim()}”.</p>{/if}
        {:else}
          {#if !pathMode}
            <p class="empty">Start with <span class="mono">/</span> or <span class="mono">~/</span> to type a folder path.</p>
          {:else}
            <div class="group">Folders</div>
            {#if listing?.dir === parts.dir && listing.error}
              <p class="empty err">{listing.error}</p>
            {:else if listing?.dir !== parts.dir}
              <p class="empty">Reading…</p>
            {/if}
            {#each pathRows as row, i}
              <button id="pal-{i}" role="option" aria-selected={pick === i} class="row" class:on={pick === i} onmousemove={() => (pick = i)} onclick={() => runRow(row)}>
                {#if row.kind === 'up'}
                  <svg class="icon" viewBox="0 0 20 20" width="18" height="18" aria-hidden="true"><path d="M8 5 4.5 8.5 8 12M4.5 8.5H12a3.5 3.5 0 0 1 3.5 3.5v3" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" /></svg>
                  <span class="name">..</span>
                {:else}
                  <svg class="icon" viewBox="0 0 24 24" width="18" height="18" aria-hidden="true"><path d="M3.5 7.5a2 2 0 0 1 2-2h4l2 2.2h7a2 2 0 0 1 2 2v7.8a2 2 0 0 1-2 2h-13a2 2 0 0 1-2-2z" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linejoin="round" /></svg>
                  <span class="name">{row.f.name}</span>
                  {#if row.f.isGit}<span class="git">git</span>{/if}
                {/if}
              </button>
            {/each}
            {#if listing?.dir === parts.dir && !listing.error && !pathRows.length}
              <p class="empty">No folders here{parts.filter ? ' start with “' + parts.filter + '”' : ''}.</p>
            {/if}
          {/if}
        {/if}
      </div>
      <div class="foot">
        {#if step === 'path'}
          <span class="keys"><span><kbd>↑ ↓</kbd> Move</span><span><kbd>Enter</kbd> {pick < 0 ? 'Use path' : 'Open folder'}</span><span><kbd>Ctrl/⌘ Enter</kbd> Use path</span><span><kbd>Esc</kbd> Close</span>{#if text === ''}<span><kbd>Backspace</kbd> Back</span>{/if}</span>
          <button class="go" disabled={!pathMode || busy} onclick={submit}>Create room</button>
        {:else}
          <span class="keys"><span><kbd>↑ ↓</kbd> Move</span><span><kbd>Enter</kbd> Open</span><span><kbd>Esc</kbd> Close</span></span>
        {/if}
      </div>
    {/if}
  </div>
</div>

<style>
  .backdrop {
    position: fixed;
    inset: 0;
    z-index: 65; /* above Confirm (60), below the toast (70) */
    background: rgba(8, 8, 10, 0.62);
    display: flex;
    justify-content: center;
    align-items: flex-start;
    padding: 12vh var(--s4) var(--s4);
  }
  .card {
    width: min(620px, 100%);
    max-height: min(560px, 76vh);
    display: flex;
    flex-direction: column;
    background: var(--side);
    border: 1px solid var(--line);
    border-radius: 12px;
    box-shadow: 0 24px 64px rgba(0, 0, 0, 0.5);
    overflow: hidden;
  }
  .top { display: flex; align-items: center; gap: var(--s1); padding: 6px var(--s2) 6px var(--s3); border-bottom: 1px solid var(--line-soft); }
  .q {
    flex: 1;
    min-width: 0;
    height: 44px;
    background: none;
    border: 0;
    outline: none;
    color: var(--ink);
    font: inherit;
    font-size: var(--t-title);
  }
  .q::placeholder { color: var(--faint); }
  .back { color: var(--muted); display: inline-flex; align-items: center; gap: var(--s1); min-width: 32px; height: 44px; border-radius: 6px; padding: 0 6px; transition: color var(--motion-fast) var(--ease); }
  .lens { color: var(--faint); flex: none; margin: 0 var(--s1) 0 2px; }
  @media (hover: hover) {
    .back:hover { color: var(--ink); }
  }
  .x { display: none; color: var(--muted); width: 44px; height: 44px; border-radius: 6px; place-items: center; transition: color var(--motion-fast) var(--ease), background-color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .x:hover { color: var(--ink); background: var(--hover); }
  }
  .x:active {
    background: var(--select);
}
  .status { margin: 0; padding: 6px var(--s4); font-size: var(--t-sm); border-bottom: 1px solid var(--line-soft); color: var(--muted); }
  .status.ok { color: var(--sage); }
  .status.warn { color: var(--gold); }
  .status.err { color: var(--coral); }
  .list { overflow-y: auto; padding: 6px; min-height: 0; flex: 1; }
  .group { font-size: var(--t-cap); font-weight: 600; letter-spacing: 0.04em; text-transform: uppercase; color: var(--faint); padding: var(--s2) 10px var(--s1); }
  .row {
    width: 100%;
    display: flex;
    align-items: center;
    gap: 10px;
    min-height: 40px;
    padding: 6px 10px;
    border-radius: 6px;
    text-align: left;
    color: var(--ink-2);
    font-size: var(--t-body);
  }
  .row.on { background: var(--select); color: var(--ink); box-shadow: inset 0 0 0 1px var(--iris-edge); }
  .message { flex-direction: column; align-items: stretch; gap: 3px; }
  .excerpt { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .message-meta { display: flex; gap: var(--s2); color: var(--faint); font-size: var(--t-sm); }
  mark { background: var(--iris-wash); color: var(--iris); border-radius: 3px; }
  .failure { display: flex; align-items: center; justify-content: space-between; gap: var(--s2); }
  .retry { color: var(--iris); padding: 0 10px; min-height: 44px; }
  .row.arch { opacity: 0.6; }
  .row.danger { color: var(--coral); }
  .row.danger .icon { color: var(--coral); }
  .row.on .icon, .row.on .hash { color: var(--iris); }
  .live { width: 8px; height: 8px; border-radius: 50%; background: var(--gold); }
  .hash { color: var(--faint); font-size: var(--t-head); line-height: 1; width: 18px; text-align: center; flex: none; }
  .icon { color: var(--faint); flex: none; width: 18px; text-align: center; }
  .name { font-weight: 500; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; flex: 0 1 auto; min-width: 0; max-width: 60%; }
  .sub { color: var(--faint); font-size: var(--t-sm); min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; flex: 1; }
  .end { margin-left: auto; display: inline-flex; gap: var(--s2); align-items: center; }
  .tag { font-size: var(--t-cap); color: var(--faint); }
  .badge { min-width: 20px; height: 20px; padding: 0 6px; border-radius: 10px; background: var(--select); border: 1px solid var(--line); font-size: var(--t-cap); font-weight: 600; line-height: 18px; text-align: center; color: var(--ink); }
  .git { font-size: var(--t-cap); font-family: var(--mono); color: var(--sage); border: 1px solid color-mix(in srgb, var(--sage) 45%, transparent); border-radius: 4px; padding: 0 5px; line-height: 17px; }
  .empty { color: var(--muted); font-size: var(--t-ui); padding: 10px; margin: 0; }
  .empty.err { color: var(--coral); }
  .foot { display: flex; align-items: center; justify-content: space-between; gap: var(--s3); padding: var(--s2) var(--s3); border-top: 1px solid var(--line-soft); }
  .keyboard-help { flex-shrink: 0; }
  .keys { display: flex; flex-wrap: wrap; gap: 4px 12px; color: var(--faint); font-size: var(--t-sm); }
  .keys > span { white-space: nowrap; }
  kbd { font: inherit; font-family: var(--mono); font-size: var(--t-cap); background: var(--raise); border-radius: 4px; padding: 1px 5px; margin: 0 2px 0 6px; color: var(--muted); }
  kbd:first-child { margin-left: 0; }
  .go { background: var(--iris); color: #18161d; border-radius: 6px; padding: var(--s2) 14px; font-size: var(--t-body); font-weight: 500; min-height: 36px; }
  .go:disabled { opacity: 0.5; cursor: default; }
  .confirm { padding: var(--s3) 18px 18px; display: flex; flex-direction: column; gap: 6px; }
  .confirm .back { height: 32px; padding: 0 6px 0 0; align-self: flex-start; font-size: var(--t-body); }
  h2 { margin: 0; font-size: var(--t-head); font-weight: 600; }
  .path { margin: var(--s1) 0 0; color: var(--ink-2); font-size: var(--t-ui); word-break: break-all; }
  .note { margin: 0; font-size: var(--t-ui); }
  .note.ok { color: var(--sage); }
  .note.warn { color: var(--gold); }
  .help { margin: 0; color: var(--muted); font-size: var(--t-ui); }
  .lab { font-size: var(--t-sm); color: var(--muted); margin-top: var(--s2); }
  .field { background: var(--field); border: 1px solid var(--line); border-radius: 6px; color: var(--ink); font: inherit; font-size: var(--t-title); padding: 9px 10px; outline: none; }
  .field:focus { border-color: var(--iris-edge); }
  .err { color: var(--coral); margin: var(--s1) 0 0; font-size: var(--t-ui); }
  .actions { display: flex; justify-content: flex-end; margin-top: 10px; }
  .actions .go { min-height: 44px; }

  /* A phone: the palette is a sheet from the top; rows and buttons are at
     least 44 px, and the input is 16 px so the browser does not zoom. */
  @media (max-width: 760px) {
    .backdrop { padding: env(safe-area-inset-top) 0 0; }
    .card { width: 100%; max-height: 100dvh; height: 100dvh; border-radius: 0; border: 0; }
    .x { display: grid; place-items: center; }
    .row { min-height: 48px; font-size: var(--t-lead); }
    .keys, .keyboard-help { display: none; }
    .foot { padding-bottom: calc(8px + env(safe-area-inset-bottom)); }
    .foot .go, .actions .go { width: 100%; min-height: 44px; }
    .actions { display: block; }
  }
</style>
