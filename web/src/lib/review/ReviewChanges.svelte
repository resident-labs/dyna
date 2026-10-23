<script>
  // The Changes view of a session: git status for its working directory,
  // with each file's diff under a row that opens and closes. The owner can
  // comment on diff lines and send them to that session.
  // Staged, unstaged and untracked files are separate groups, as in git; a
  // file with both kinds of change shows in both.
  import { onMount, untrack } from 'svelte'
  import { api } from '../../api.js'
  import { ago } from '../ui/format.ts'
  import DiffView from '../files/DiffView.svelte'
  import SendBar from '../files/SendBar.svelte'
  import { addComment, formatComments } from '../files/diffComments.ts'
  import { code, setWrap } from '../files/codeprefs.svelte.ts'
  import { parseDiff, statusLetter, STATUS, split, base } from '../files/repo.ts'

  // sessionName is the session's display name; the send bar names it as the receiver.
  let { roomId, session, sessionName = '', version = 0, active = true } = $props()

  let status = $state(null)
  let error = $state('')
  let loading = $state(false)
  let diffs = $state({ staged: new Map(), unstaged: new Map() })
  let extra = $state({}) // untracked path -> parsed file | {error}
  let open = $state(new Set())
  let cut = $state(false)
  let pending = $state([])
  // Send the comment as a message to the session shown beside the workspace.
  const deliver = (comments) => api.sendToSession(session, formatComments(comments))
  let loaded = false
  $effect(() => {
    version
    if (!active) return
    if (!loaded) { loaded = true; untrack(load); return }
    const timer = setTimeout(() => untrack(load), 1000)
    return () => clearTimeout(timer)
  })

  let loadVersion = 0
  async function load() {
    const version = ++loadVersion
    loading = true
    try {
      const [st, un, sd] = await Promise.all([
        api.gitStatus(roomId, session),
        api.gitDiff(roomId, session, '', false),
        api.gitDiff(roomId, session, '', true),
      ])
      if (version !== loadVersion) return
      extra = {}
      const byPath = (d) => new Map(parseDiff(d?.text).map((f) => [f.path, f]))
      diffs = { unstaged: byPath(un), staged: byPath(sd) }
      cut = !!(un?.truncated || sd?.truncated || st?.truncated)
      const first = status === null
      status = st
      error = ''
      // A short list starts open; a long one starts closed.
      if (first && groups.reduce((n, g) => n + g.files.length, 0) <= 4) {
        open = new Set(groups.flatMap((g) => g.files.map((f) => `${g.id}:${f.path}`)))
      }
      for (const g of groups) if (g.id === 'untracked') {
        for (const f of g.files) if (open.has(`${g.id}:${f.path}`)) fetchUntracked(f)
      }
    } catch (e) {
      if (version === loadVersion) error = String(e?.message || e)
    } finally {
      if (version === loadVersion) loading = false
    }
  }

  onMount(() => {
    const again = () => active && document.visibilityState === 'visible' && load()
    window.addEventListener('focus', again)
    return () => window.removeEventListener('focus', again)
  })

  let groups = $derived.by(() => {
    const files = status?.files ?? []
    return [
      { id: 'staged', title: 'Staged', hint: 'Ready to commit', files: files.filter((f) => f.staged && !f.untracked) },
      { id: 'unstaged', title: 'Changes', hint: 'Not staged', files: files.filter((f) => (f.unstaged || (!f.staged && !f.untracked)) && !f.untracked) },
      { id: 'untracked', title: 'Untracked', hint: 'New to git', files: files.filter((f) => f.untracked) },
    ].filter((g) => g.files.length)
  })
  let total = $derived((status?.files ?? []).length)

  function diffOf(g, f) {
    if (g.id === 'untracked') return extra[f.path]
    return diffs[g.id].get(f.path) ?? null
  }

  // An untracked file has no bulk diff; fetch its preview once per refresh.
  function fetchUntracked(f) {
    if (extra[f.path]) return
    const version = loadVersion
    api.gitDiff(roomId, session, f.path, false).then(
      (d) => {
        if (version !== loadVersion) return
        const file = parseDiff(d?.text)[0]
        const empty = !file || (file.kind !== 'binary' && file.hunks.length === 0)
        extra = { ...extra, [f.path]: empty ? { path: f.path, kind: 'added', hunks: [], adds: 0, dels: 0, empty: true } : file }
      },
      (e) => { if (version === loadVersion) extra = { ...extra, [f.path]: { error: String(e?.message || e) } } },
    )
  }

  async function toggle(g, f) {
    const k = `${g.id}:${f.path}`
    const next = new Set(open)
    if (next.has(k)) next.delete(k)
    else {
      next.add(k)
      if (g.id === 'untracked') fetchUntracked(f)
    }
    open = next
  }

  let allOpen = $derived(groups.length > 0 && groups.every((g) => g.files.every((f) => open.has(`${g.id}:${f.path}`))))
  function setAll(on) {
    if (!on) return (open = new Set())
    for (const g of groups) for (const f of g.files) if (!open.has(`${g.id}:${f.path}`)) toggle(g, f)
  }
</script>

<section class="changes">
  {#if error}
    <p class="err">{error}</p>
  {:else if !status}
    <p class="quiet">Reading git status…</p>
  {:else}
    <header class="head">
      <div class="branch">
        <svg viewBox="0 0 20 20" width="18" height="18" aria-hidden="true"><circle cx="6" cy="4.5" r="2" fill="none" stroke="currentColor" stroke-width="1.5" /><circle cx="6" cy="15.5" r="2" fill="none" stroke="currentColor" stroke-width="1.5" /><circle cx="14.5" cy="7" r="2" fill="none" stroke="currentColor" stroke-width="1.5" /><path d="M6 6.5v7M14.5 9c0 3-3 3.5-8.2 4.6" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" /></svg>
        <span class="mono name">{status.branch || 'detached HEAD'}</span>
        {#if status.upstream}<span class="up mono" title="Upstream">{status.upstream}</span>{/if}
        {#if status.ahead}<span class="ab" title="{status.ahead} commit{status.ahead === 1 ? '' : 's'} ahead of upstream">↑{status.ahead}</span>{/if}
        {#if status.behind}<span class="ab" title="{status.behind} commit{status.behind === 1 ? '' : 's'} behind upstream">↓{status.behind}</span>{/if}
      </div>
      <p class="sub">
        <span class="mono" title={status.root}>{base(status.root)}</span>{#if status.worktree}<span class="tag">worktree</span>{/if}
        · {total ? `${total} changed file${total === 1 ? '' : 's'}` : 'clean'}
      </p>
      <span class="spacer"></span>
      {#if total}
        <button class="tool" class:on={code.wrap} aria-pressed={code.wrap} onclick={() => setWrap(!code.wrap)} title={code.wrap ? 'Show long lines on one line' : 'Wrap long lines'}>Wrap</button>
        <button class="tool" onclick={() => setAll(!allOpen)}>{allOpen ? 'Collapse all' : 'Expand all'}</button>
      {/if}
      <button class="tool icon reload" onclick={load} disabled={loading} title="Reload" aria-label="Reload">
        <svg class:spin={loading} viewBox="0 0 20 20" width="16" height="16" aria-hidden="true"><path d="M16 10a6 6 0 1 1-1.8-4.3M16 3.5v3.2h-3.2" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" /></svg>
      </button>
    </header>

    <div class="scroll">
      {#if cut}<p class="cut">The diff is large, so the view shows only its first part. Run <span class="mono">git diff</span> for the rest.</p>{/if}
      {#if !total}
        <div class="clean">
          <svg viewBox="0 0 24 24" width="34" height="34" aria-hidden="true"><circle cx="12" cy="12" r="9" fill="none" stroke="currentColor" stroke-width="1.6" /><path d="m8 12.3 2.6 2.6L16.2 9.3" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" /></svg>
          <strong>Working tree clean</strong>
          <p>No file differs from the last commit{status.branch ? ` on ${status.branch}` : ''}.</p>
        </div>
      {/if}
      {#each groups as g (g.id)}
        <h2>{g.title}<span>{g.files.length} · {g.hint}</span></h2>
        <div class="files">
          {#each g.files as f (f.path)}
            {@const k = `${g.id}:${f.path}`}
            {@const d = diffOf(g, f)}
            {@const l = statusLetter(f, g.id)}
            {@const [dir, name] = split(f.path)}
            <div class="file" class:open={open.has(k)}>
              <div class="row">
                <button class="hit" onclick={() => toggle(g, f)} aria-expanded={open.has(k)} title="{STATUS[l].word}: {f.path}">
                  <svg class="chev" viewBox="0 0 12 12" width="11" height="11" aria-hidden="true"><path d="m4 2.5 3.5 3.5L4 9.5" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" /></svg>
                  <span class="letter {STATUS[l].tone}" aria-label={STATUS[l].word}>{l}</span>
                  <span class="path"><bdi><span class="dir">{dir}</span><span class="fname">{name}</span>{#if f.originalPath}<span class="from"> from {f.originalPath}</span>{/if}</bdi></span>
                  {#if d && !d.error && (d.adds || d.dels)}
                    <span class="counts">{#if d.adds}<span class="plus">+{d.adds}</span>{/if}{#if d.dels}<span class="minus">−{d.dels}</span>{/if}</span>
                  {/if}
                </button>
              </div>
              {#if open.has(k)}
                <div class="body">
                  {#if d?.error}
                    <p class="note err">{d.error}</p>
                  {:else if !d && g.id === 'untracked'}
                    <p class="note">Loading…</p>
                  {:else if !d}
                    <p class="note">{l === 'D' ? 'Deleted.' : 'No diff for this file.'}</p>
                  {:else if d.empty}
                    <p class="note">Empty file.</p>
                  {:else}
                    <DiffView file={d} oncomment={(c) => (pending = addComment(pending, c))} />
                  {/if}
                </div>
              {/if}
            </div>
          {/each}
        </div>
      {/each}
      <SendBar bind:pending author={sessionName || session} {deliver} />

      {#if status.commits?.length}
        <h2>Recent commits<span>{status.commits.length}</span></h2>
        <ol class="commits">
          {#each status.commits as c (c.hash)}
            <li>
              <span class="hash mono">{c.shortHash}</span>
              <span class="subject">{c.subject}</span>
              <span class="by">{c.author} · {ago(c.time)}</span>
            </li>
          {/each}
        </ol>
      {/if}
    </div>
  {/if}
</section>

<style>
  .changes { flex: 1; display: flex; flex-direction: column; min-height: 0; min-width: 0; }
  .head { display: flex; align-items: center; flex-wrap: wrap; gap: 4px 12px; padding: 12px 16px 10px; border-bottom: 1px solid var(--line-soft); flex: none; }
  .branch { display: flex; align-items: center; gap: 8px; color: var(--muted); min-width: 0; }
  .branch .name { color: var(--ink); font-weight: 600; font-size: 14px; }
  .up { font-size: 12px; color: var(--faint); }
  .ab { font-size: 12px; font-weight: 600; color: var(--ink-2); background: var(--raise); border-radius: 10px; padding: 0 7px; font-variant-numeric: tabular-nums; }
  .sub { margin: 0; font-size: 12.5px; color: var(--faint); display: flex; align-items: center; gap: 6px; }
  .tag { font-size: 10.5px; font-weight: 600; color: var(--iris); background: var(--iris-wash); border-radius: 4px; padding: 0 5px; }
  .spacer { flex: 1; }
  .tool { font-size: 12px; font-weight: 600; color: var(--muted); padding: 5px 10px; border-radius: 6px; background: var(--raise); }
  .tool:not(:disabled) { transition: color var(--motion-fast) var(--ease), background-color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .tool:hover:not(:disabled) { color: var(--ink); background: var(--raise-hover); }
  }
  .tool:active:not(:disabled) { background: var(--select); }
  .tool.icon { padding: 5px 7px; display: grid; place-items: center; }
  .tool.on { color: var(--iris); background: var(--iris-wash); }
  .spin { animation: spin 0.9s linear infinite; }
  @keyframes spin { to { transform: rotate(360deg); } }
  .scroll { flex: 1; overflow-y: auto; padding: 4px 16px 32px; }
  .cut { margin: 12px 0 0; font-size: 12.5px; color: var(--gold); background: var(--gold-wash); border-radius: 6px; padding: 6px 10px; }
  h2 { display: flex; align-items: baseline; gap: 8px; font-size: 12px; font-weight: 700; color: var(--muted); text-transform: uppercase; letter-spacing: 0.06em; margin: 20px 0 8px; }
  h2 span { text-transform: none; letter-spacing: 0; font-weight: 500; color: var(--faint); }
  .files { display: flex; flex-direction: column; gap: 6px; }
  .file { background: var(--side); border: 1px solid var(--line-soft); border-radius: 10px; overflow: clip; }
  .file.open { border-color: var(--line); }
  .file.open .row { position: sticky; top: 0; z-index: 2; background: var(--side); }
  .row { display: flex; align-items: center; }
  .hit { flex: 1; display: flex; align-items: center; gap: 10px; min-width: 0; padding: 9px 12px; text-align: left; transition: background-color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .hit:hover { background: var(--hover); }
  }
  .hit:active { background: var(--select); }
  .hit:focus-visible { outline-offset: -2px; }
  .chev { color: var(--faint); flex: none; transition: transform var(--motion-fast); }
  .file.open .chev { transform: rotate(90deg); }
  .letter { flex: none; width: 20px; height: 20px; border-radius: 5px; display: grid; place-items: center; font-family: var(--mono); font-size: 11.5px; font-weight: 700; }
  .letter.gold { color: var(--gold); background: rgba(230, 192, 123, 0.13); }
  .letter.sage { color: var(--sage); background: rgba(151, 201, 149, 0.13); }
  .letter.coral { color: var(--coral); background: rgba(239, 138, 128, 0.13); }
  .letter.iris { color: var(--iris); background: var(--iris-wash); }
  .path { flex: 1; min-width: 0; font-family: var(--mono); font-size: 13px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; direction: rtl; text-align: left; }
  .dir { color: var(--faint); }
  .fname { color: var(--ink); }
  .from { color: var(--faint); }
  .counts { flex: none; display: flex; gap: 8px; font-family: var(--mono); font-size: 12px; font-variant-numeric: tabular-nums; }
  .plus { color: var(--sage); }
  .minus { color: var(--coral); }
  .body { border-top: 1px solid var(--line-soft); }
  .note { margin: 0; padding: 12px 14px; font-size: 13px; color: var(--faint); background: #19181a; }
  .note.err, .err { color: var(--coral); }
  .err, .quiet { padding: 16px 20px; }
  .quiet { color: var(--faint); font-size: 13.5px; }
  .clean { margin: 40px auto 8px; max-width: 360px; display: flex; flex-direction: column; align-items: center; gap: 6px; text-align: center; color: var(--sage); }
  .clean strong { color: var(--ink); font-size: 16px; }
  .clean p { margin: 0; color: var(--muted); font-size: 14px; }
  .commits { list-style: none; margin: 0; padding: 0; background: var(--side); border: 1px solid var(--line-soft); border-radius: 10px; overflow: hidden; }
  .commits li { display: grid; grid-template-columns: auto 1fr auto; gap: 12px; align-items: baseline; padding: 8px 14px; font-size: 13.5px; }
  .commits li + li { border-top: 1px solid var(--line-soft); }
  .hash { color: var(--iris); font-size: 12px; }
  .subject { color: var(--ink-2); min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .by { color: var(--faint); font-size: 12px; white-space: nowrap; }

  @media (max-width: 760px) {
    .head { padding: 8px 12px; }
    .branch { flex: 1 1 calc(100% - 60px); }
    .reload { order: 1; }
    .sub { order: 2; flex: 1; }
    .head .tool:not(.reload) { order: 3; }
    .head .spacer { display: none; }
    .tool { padding: 10px 12px; }
    .tool.icon { width: 44px; height: 44px; }
    .scroll { padding: 0 8px 24px; }
    .hit { min-height: 48px; }
    .commits li { grid-template-columns: auto 1fr; }
    .by { grid-column: 2; }
  }
</style>
