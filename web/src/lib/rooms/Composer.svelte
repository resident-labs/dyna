<script>
  import { speaker } from '../ui/posts.ts'
  import { tick, onDestroy } from 'svelte'
  import { short, roomLabel } from '../ui/format.ts'
  import { api } from '../../api.ts'
  import ComposerBox from '../ui/ComposerBox.svelte'
  import { attachmentDraft } from '../files/attachmentDraft.ts'
  import { mentionOptions, matchMentions, fileMentionDirectory, fileMentionOptions, fileMentionText } from './mentions.ts'
  import { loadReferences, matchReferences, referenceKey, referenceLabel } from './references.ts'
  import ReferenceChips from './ReferenceChips.svelte'

  // commands are app actions typed as "/name [arg]": [{name, hint, arg?,
  // options?() -> [{value, hint}], pickRuns?, run(arg) -> error text or
  // nothing}]. A profile with the same name wins, so "/<profile> <task>"
  // always starts a session.
  let { room, label = '', prepareText = (text) => text, choices = [], profiles = [], commands = [], reply = null, onopenfile, draft = $bindable(''), onsend, onslash, oncancelreply } = $props()

  // An asynchronous recipient lookup must not outlive its channel composer.
  let active = true
  onDestroy(() => { active = false })

  let forwarding = $state(null)
  export function forward(post) { if (busy) return; forwarding = post; field?.focus() }

  let references = $state([])
  let referenceOptions = $state([])
  let referenceLoading = $state(false)
  let fileEntries = $state([])
  let fileLoading = $state(false)
  let fileError = $state('')
  const fileListings = new Map()
  const fileDirectory = $derived(token && !token.slash && !token.arg && !token.reference ? fileMentionDirectory(token.text) : null)
  $effect(() => {
    const directory = fileDirectory
    const id = room?.id
    if (directory === null || !id) return
    let cancelled = false
    fileLoading = true
    fileError = ''
    fileEntries = []
    const key = id + ':' + directory
    if (!fileListings.has(key)) fileListings.set(key, api.listEntries(id, '', directory).catch((error) => { fileListings.delete(key); throw error }))
    fileListings.get(key).then((listing) => {
      if (!cancelled) fileEntries = listing.entries ?? []
    }).catch((error) => {
      if (!cancelled) fileError = String(error?.message || error)
    }).finally(() => { if (!cancelled) fileLoading = false })
    return () => { cancelled = true }
  })
  let referenceLoaded = false
  async function fetchReferences() {
    if (referenceLoading || referenceLoaded) return
    referenceLoading = true
    const id = room?.id
    try {
      const result = await loadReferences(id)
      if (active && room?.id === id) { referenceOptions = result; referenceLoaded = true }
    } catch (e) { if (active) error = String(e?.message || e) }
    finally { if (active) referenceLoading = false }
  }
  $effect(() => {
    const query = token?.reference ? token.text.replace(/^#/, '').trim() : ''
    if (!query) return
    let cancelled = false
    const timer = setTimeout(async () => {
      try {
        const found = await loadReferences(room?.id, query)
        if (!active || cancelled) return
        const merged = new Map(referenceOptions.map((reference) => [referenceKey(reference), reference]))
        found.forEach((reference) => merged.set(referenceKey(reference), reference))
        referenceOptions = [...merged.values()]
      } catch (e) { if (active && !cancelled) error = String(e?.message || e) }
    }, 200)
    return () => { cancelled = true; clearTimeout(timer) }
  })

  export function addReference(reference) {
    if (!references.some((item) => referenceKey(item) === referenceKey(reference))) references = [...references, reference]
    field?.focus()
  }
  const selectedProfile = $derived(profiles.find((profile) => draft.startsWith('/' + profile.name + ' ') || draft === '/' + profile.name))
  const composerMode = $derived(command ? 'Command' : selectedProfile ? 'Start session' : 'Message')
  async function openCommands() {
    if (!draft.startsWith('/')) draft = '/' + draft
    await tick()
    field?.focus()
    field?.setSelectionRange(1, 1)
    findToken()
  }
  async function openReferences() {
    const pos = field?.selectionStart ?? draft.length
    const prefix = pos && !/\s/.test(draft[pos - 1]) ? ' #' : '#'
    draft = draft.slice(0, pos) + prefix + draft.slice(pos)
    await tick()
    field?.focus()
    field?.setSelectionRange(pos + prefix.length, pos + prefix.length)
    findToken()
  }

  let field = $state()
  let busy = $state(false)
  let error = $state('')
  let note = $state('')
  let pick = $state(0)
  let completionList = $state()
  const completionID = $props.id()
  const optionID = (option) => completionID + '-option-' + (option.file ? 'file-' : option.reference ? 'reference-' : 'text-') + encodeURIComponent(option.value)
  let preview = $state(null) // roomsvc.Draft for the current text: who it wakes
  let token = $state(null) // {start, end, text} of the @token at the caret
  const attachments = attachmentDraft({ room: () => room?.id, error: (message) => (error = message), focus: () => field?.focus(), noun: 'post' })
  let failed = $derived($attachments.failed)
  let canSend = $derived(!busy && !$attachments.uploading && !failed && (draft.trim() !== '' || $attachments.ready.length > 0 || !!forwarding || references.length > 0))
  export function addFiles(files) { attachments.addFiles(files) }
  onDestroy(() => attachments.destroy())

  // App commands a profile does not shadow, and the one the draft names.
  let appCommands = $derived(commands.filter((c) => !profiles.some((p) => p.name === c.name)))
  let command = $derived.by(() => {
    const m = draft.match(/^\/([\w-]+)(?:\s+([\s\S]*))?$/)
    const c = m && appCommands.find((x) => x.name === m[1])
    return c ? { c, arg: (m[2] ?? '').trim(), spaced: /^\/[\w-]+\s/.test(draft) } : null
  })

  let commandLine = $derived(command ? `/${command.c.name}${command.c.arg && !command.arg ? ` ‹${command.c.arg}›` : ''} · ${command.c.hint}` : '')

  // A token is "@name" anywhere, "/profile" or "/command" at the very start
  // of the draft (only a draft's first word can start a session), or the
  // argument of a command that offers choices.
  let options = $derived(
    token?.reference ? matchReferences(referenceOptions, token.text).map((reference) => ({ value: referenceKey(reference), hint: reference.title + ' · ' + reference.state, label: referenceLabel(reference), reference })) :
    token?.arg ? token.arg.options() :
    token?.slash ? [
      ...profiles.map((p) => ({ value: '/' + p.name, group: 'Start a session with', hint: [p.harness, p.model].filter(Boolean).join(' · ') + (p.default ? ' · default' : '') })),
      ...appCommands.map((c) => ({ value: '/' + c.name, group: 'Commands', arg: c.arg, hint: c.hint })),
    ] :
    [...mentionOptions(choices).map((option) => ({ ...option, group: 'Sessions' })), ...fileMentionOptions(fileEntries, token?.text || '@')])
  let matches = $derived(token ? (token.reference ? options : token.arg ? options.filter((o) => `${o.value} ${o.hint}`.toLowerCase().includes(token.text.toLowerCase())).slice(0, 8) : token.slash ? matchMentions(options, token.text, 20) : [...matchMentions(options.filter((option) => !option.file), token.text, 6), ...options.filter((option) => option.file)]) : [])
  // Keep the selected option inside this menu without scrolling the feed.
  $effect(() => {
    const options = matches
    if (pick >= options.length) {
      pick = Math.max(0, options.length - 1)
      return
    }
    if (!options[pick]) return
    tick().then(() => {
      const selected = matches[pick]
      if (!selected) return
      const menu = completionList
      const row = menu?.querySelector('[id="' + optionID(selected) + '"]')
      if (!menu || !row) return
      const bounds = menu.getBoundingClientRect()
      const item = row.getBoundingClientRect()
      const top = item.top - bounds.top - menu.clientTop
      const bottom = item.bottom - bounds.top - menu.clientTop
      if (top < 0) menu.scrollTop += top
      else if (bottom > menu.clientHeight) menu.scrollTop += bottom - menu.clientHeight
    })
  })

  let spawn = $derived(preview?.spawn ?? null)

  // The Go parser decides who a draft goes to; ask it shortly after typing stops.
  $effect(() => {
    const text = draft
    const prepare = prepareText
    const id = room?.id
    const files = $attachments.items.length
    const refs = references.length
    if (!id || (!text.trim() && !files && !refs)) { preview = null; return }
    let cancelled = false
    const timer = setTimeout(async () => {
      try {
        const prepared = await prepare(text)
        const result = await api.preview(id, prepared)
        if (!cancelled) preview = result
      } catch {
        if (!cancelled) preview = null
      }
    }, 150)
    return () => { cancelled = true; clearTimeout(timer) }
  })
  let wakeLine = $derived(
    !preview ? '' :
    spawn ? (spawn.error || `New session @${spawn.name || 'agent-…'} · ${spawn.profile}${spawn.model ? ' (' + spawn.model + ')' : ''} · in ${spawn.dir}`) :
    preview.self ? 'To root only · external orchestrators watching this room' :
    !preview.wakes?.length ? `To ${preview.mentions.join(' ')} · wakes nobody` :
    `To ${preview.wakes.join(', ')} · ${preview.wakes.length} wake`
  )


  function findToken() {
    const pos = field.selectionStart
    const before = draft.slice(0, pos)
    // Folder completion already knows the start, including any spaces in
    // its path. Keep that boundary while the user filters this folder.
    if (token?.file) {
      const prefix = token.text.slice(0, token.text.lastIndexOf('/') + 1)
      const text = before.slice(token.start)
      if (text.startsWith(prefix) && !/\s@|[\r\n]/.test(text.slice(prefix.length))) {
        token = { ...token, end: pos, text }
        pick = 0
        return
      }
    }
    const start = Math.max(before.lastIndexOf(' '), before.lastIndexOf('\n'), before.lastIndexOf('\t')) + 1
    const text = before.slice(start)
    const lead = draft.match(/^\/([\w-]+)\s+/)
    const withArg = lead && appCommands.find((c) => c.name === lead[1] && c.options)
    if (start === 0 && text.startsWith('/') && !text.startsWith('//')) {
      if (!token?.slash) onslash?.() // the list may have changed since launch
      token = { start, end: pos, text, slash: true }
    }
    else if (withArg && pos >= lead[0].length && !draft.slice(lead[0].length, pos).includes(' ')) token = { start: lead[0].length, end: pos, text: draft.slice(lead[0].length, pos), arg: withArg }
    else if (text.startsWith('#')) { token = { start, end: pos, text, reference: true }; fetchReferences() }
    else token = text.startsWith('@') ? { start, end: pos, text } : null
    pick = 0
  }

  async function complete(option) {
    if (!token) return
    const choice = option.value
    const file = option.file
    if (file) {
      const text = fileMentionText(file)
      const start = token.start
      const caret = start + text.length
      draft = draft.slice(0, start) + text + draft.slice(token.end)
      token = text.startsWith('@') ? { start, end: caret, text, file: true } : null
      pick = 0
      await tick()
      field?.focus()
      field?.setSelectionRange(caret, caret)
      return
    }
    if (token?.reference) {
      const reference = option.reference
      if (!reference) return
      addReference(reference)
      const caret = token.start
      draft = draft.slice(0, token.start) + draft.slice(token.end)
      token = null
      await tick()
      field?.focus()
      field?.setSelectionRange(caret, caret)
      return
    }
    // Picking a room in "/room" runs it at once.
    if (token.arg?.pickRuns) {
      const c = token.arg
      token = null
      draft = ''
      return runCommand(c, choice)
    }
    const next = draft.slice(0, token.start) + choice + ' ' + draft.slice(token.end)
    const caret = token.start + choice.length + 1
    draft = next
    token = null
    await tick()
    field.focus()
    field.setSelectionRange(caret, caret)
  }

  async function runCommand(c, arg) {
    error = ''
    busy = true
    try {
      const err = await c.run(arg)
      if (err) error = err
      else draft = ''
    } catch (e) {
      error = String(e?.message || e)
    } finally {
      busy = false
      await tick()
      field?.focus()
    }
  }

  async function send() {
    if (command && !forwarding) {
      if (references.length) { error = 'Remove references before running a command.'; return }
      return runCommand(command.c, command.arg)
    }
    if (!canSend) return
    error = ''
    busy = true
    try {
      const atts = $attachments.atts
      const text = await prepareText(draft)
      if (!active) return
      const sent = await onsend(text, atts, forwarding?.id ?? '', references)
      if (sent?.kind === 'spawn') {
        const m = sent.spawn.member
        note = sent.spawn.warning ? `Started @${m.name}, but ${sent.spawn.warning.replace(/^the session started, but /, '')}` : `Started @${m.name} · ${m.profile}`
        setTimeout(() => (note = ''), 4000)
        return
      }
      const res = sent?.post
      if (res?.failed?.length) {
        error = res.failed.map((f) => `${short(f.to, room.name)}: ${f.error || 'delivery failed'}`).join(' · ')
      } else {
        const n = res?.deliveries?.length || 0
        note = n ? `Delivered to ${n} recipient${n === 1 ? '' : 's'}` : 'Posted'
        setTimeout(() => (note = ''), 2400)
      }
      if (res?.message?.id) {
        // The app keeps the sent draft's recipients (res.sticky) for the
        // next post; see App.send.
        attachments.clear()
        forwarding = null
        references = []
      }
    } catch (e) {
      const msg = String(e?.message || e)
      error = msg
    } finally {
      busy = false
      await tick()
      if (active) field?.focus()
    }
  }

  function keydown(e) {
    if (e.key === 'Escape' && token) { e.preventDefault(); token = null; return }
    if (matches.length) {
      if (e.key === 'ArrowDown') { e.preventDefault(); pick = (pick + 1) % matches.length; return }
      if (e.key === 'ArrowUp') { e.preventDefault(); pick = (pick - 1 + matches.length) % matches.length; return }
      // Enter on a typed command that needs nothing more runs it.
      if (e.key === 'Enter' && !e.shiftKey && command && !command.c.arg && matches[pick].value === '/' + command.c.name) { e.preventDefault(); token = null; send(); return }
      if (e.key === 'Tab' || (e.key === 'Enter' && !e.shiftKey)) { e.preventDefault(); complete(matches[pick]); return }
      if (e.key === 'Escape') { e.preventDefault(); token = null; return }
    }
    if (e.key === 'Escape' && reply) { e.preventDefault(); oncancelreply(); return }
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault()
      send()
    }
  }

  export function focus() {
    field?.focus()
  }
</script>

<div class="composer">
  {#if token?.reference && !matches.length}
    <div class="pop" role="status">{referenceLoading ? 'Loading PRs and issues…' : 'No matching PRs or issues.'}</div>
  {/if}
  {#if matches.length}
    <div class="pop" id={completionID} role="listbox" aria-label="Composer suggestions" bind:this={completionList}>
      {#if !matches[0].group}<div class="pophead">{token?.reference ? 'Attach a PR or issue' : token?.arg ? token.arg.listTitle || token.arg.arg : 'Mention'}</div>{/if}
      {#each matches as m, i}
        {#if m.group && m.group !== matches[i - 1]?.group}<div class="pophead">{m.group}</div>{/if}
        <button id={optionID(m)} class="opt" class:reference-opt={!!m.reference} title={m.hint} class:on={i === pick} role="option" aria-selected={i === pick} onmousedown={(e) => e.preventDefault()} onclick={() => complete(m)}>
          {#if m.reference}
            <span class="reference-title">{m.reference.title}</span>
            <span class="reference-meta">
              <span class="mono">{m.label}</span>
              {#if m.reference.kind === 'room_issue'}<span class="room-tag">room</span>{/if}
              <span>{m.reference.state.toLowerCase()}</span>
            </span>
          {:else}
            <span class="mono">{m.label || m.value}{#if m.arg}<span class="argph"> ‹{m.arg}›</span>{/if}</span>
            <span class="kind">{m.hint}</span>
          {/if}
        </button>
      {/each}
    </div>
  {/if}
  {#if reply}
    <div class="replybar">
      <span>Replying to <strong class="hue-{reply.fromHue}">{speaker(reply, room.name)}</strong></span>
      <button onclick={oncancelreply} aria-label="Cancel reply">
        <svg viewBox="0 0 16 16" width="14" height="14" aria-hidden="true"><path d="M4 4l8 8M12 4l-8 8" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" /></svg>
      </button>
    </div>
  {/if}
  {#if fileDirectory !== null && (fileLoading || fileError || !matches.length)}
    <div class="file-status" role="status">{fileLoading ? 'Loading files…' : fileError ? 'Files unavailable: ' + fileError : 'No matching files or sessions.'}</div>
  {/if}
  <ComposerBox onreference={openReferences} {room} {forwarding} onremoveforward={() => (forwarding = null)} {onopenfile} {attachments} bind:draft bind:field placeholder={`Message #${label || roomLabel(room)}`} {busy} {canSend} joined={!!reply} bad={!!(error || (spawn?.error && !command))} spawn={!!(spawn && !spawn.error)} oninput={() => { findToken(); error = '' }} onclick={findToken} onkeydown={keydown} onsend={send} mode={composerMode} model={selectedProfile?.model || ''} {completionID} expanded={!!matches.length} activeOption={matches[pick] ? optionID(matches[pick]) : undefined}>
    {#snippet context()}<ReferenceChips {references} disabled={busy} onremove={(reference) => (references = references.filter((item) => referenceKey(item) !== referenceKey(reference)))} />{/snippet}
    {#snippet tools()}
      <button class="command-chip" type="button" aria-label="Browse commands" title="Browse sessions and commands" disabled={busy} onclick={openCommands}>/</button>
    {/snippet}
  </ComposerBox>
  <div class="foot">
    {#if error}
      <span class="err">{error}</span>
    {:else if failed}
      <span class="err">A file did not save. Retry it or remove it before you send.</span>
    {:else if command}
      <span class="go">{commandLine} · <kbd>Enter</kbd> to run</span>
    {:else if spawn?.error}
      <span class="err">{spawn.error}</span>
    {:else if spawn}
      <span class="go">{wakeLine} · <kbd>Enter</kbd> to start</span>
    {:else if wakeLine}
      <span class:warn={!preview?.wakes?.length}>{wakeLine} · <kbd>Enter</kbd> to send</span>
    {:else if note}
      <span class="ok">{note}</span>
    {:else}
      <span><kbd>Enter</kbd> to send · <kbd>Shift</kbd>+<kbd>Enter</kbd> new line · <span class="mono">@</span> for files and sessions · <span class="mono">#</span> for PRs and issues · <span class="mono">/</span> for sessions and commands</span>
    {/if}
  </div>
</div>

<style>
  .file-status { padding: 5px var(--s2); font-size: var(--t-sm); color: var(--muted); }
  .command-chip { display: grid; place-items: center; width: 30px; height: 30px; border-radius: 6px; color: var(--muted); font-family: var(--mono); transition: color var(--motion-fast) var(--ease), background-color var(--motion-fast) var(--ease); }
  .command-chip:hover { background: var(--hover); color: var(--ink); }
  .command-chip:active { background: var(--select); }
  .composer { position: relative; padding: 0 var(--s4) 6px; flex: none; }
  /* A phone: 16 px text stops iOS zooming on focus; wider tap targets. */
  @media (max-width: 760px) {
    .composer { padding: 0 var(--s2) var(--s1); }
    .command-chip { display: none; }
    .opt { min-height: 44px; }
    .foot { display: none; }
    .foot:has(.err, .warn, .go) { display: block; }
  }
  .foot { min-height: 22px; padding: 3px var(--s1) 0; font-size: var(--t-sm); color: var(--faint); }
  .err { color: var(--coral); }
  .warn { color: var(--gold); }
  .ok { color: var(--sage); }
  .go { color: var(--sage); }
  kbd {
    font-family: var(--sans);
    font-size: var(--t-cap);
    font-weight: 600;
    color: var(--muted);
  }
  .replybar {
    display: flex;
    align-items: center;
    justify-content: space-between;
    background: #29282c;
    border-radius: 8px 8px 0 0;
    padding: 6px var(--s3) 6px var(--s4);
    font-size: var(--t-ui);
    color: var(--muted);
  }
  .replybar strong { font-weight: 600; }
  .replybar button { color: var(--muted); display: grid; place-items: center; width: 22px; height: 22px; border-radius: 50%; transition: color var(--motion-fast) var(--ease), background-color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .replybar button:hover { color: var(--ink); background: var(--hover); }
  }
  .replybar button:active { background: var(--select); }
  .pop {
    position: absolute;
    left: 16px;
    right: 16px;
    bottom: calc(100% - 4px);
    background: var(--side);
    border: 1px solid var(--line);
    border-radius: 8px;
    padding: 6px;
    box-shadow: 0 8px 24px rgba(0, 0, 0, 0.4);
    z-index: 10;
    max-height: min(420px, 60dvh);
    overflow-y: auto;
  }
  .argph { color: var(--faint); }
  .pophead { font-size: var(--t-sm); font-weight: 600; color: var(--muted); padding: 2px var(--s2) 6px; }
  .opt {
    width: 100%;
    display: flex;
    justify-content: space-between;
    align-items: center;
    gap: var(--s2);
    text-align: left;
    padding: 6px var(--s2);
    border-radius: 5px;
    color: var(--ink-2);
  }
  .opt.reference-opt { display: grid; justify-content: stretch; gap: 3px; }
  .reference-title { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: var(--t-ui); }
  .reference-meta { display: flex; align-items: center; gap: var(--s2); min-width: 0; color: var(--muted); font-size: var(--t-sm); }
  .opt .reference-meta .mono { font-size: inherit; }
  .room-tag { padding: 0 4px; border: 1px solid var(--line); border-radius: 3px; font-size: var(--t-cap); }
  .opt.on { background: var(--select); color: var(--ink); }
  .opt .mono { font-size: var(--t-ui); min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .kind { font-size: var(--t-sm); color: var(--faint); max-width: 48%; flex-shrink: 0; text-align: right; overflow: hidden; display: -webkit-box; -webkit-box-orient: vertical; -webkit-line-clamp: 2; line-clamp: 2; }
</style>
