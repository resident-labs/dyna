<script lang="ts">
  import { speaker } from '../ui/posts.ts'
  import { tick, onDestroy } from 'svelte'
  import { short, roomLabel } from '../ui/format.ts'
  import { errorText } from '../errorText.ts'
  import { api } from '../../api.ts'
  import ComposerBox from '../ui/ComposerBox.svelte'
  import { attachmentDraft } from '../files/attachmentDraft.ts'
  import { commandOptions, matchCommand } from './slashCompletion.ts'
  import { mentionOptions, matchMentions, fileMentionDirectory, fileMentionOptions, fileMentionText } from './mentions.ts'
  import { loadReferences, matchReferences, referenceKey, referenceLabel } from './references.ts'
  import { modelLabel, profileHint } from './profileLabels.ts'
  import ReferenceChips from './ReferenceChips.svelte'
  import type { Reference } from './references.ts'
  import type { Action } from '../ui/actions.ts'
  import type { ProfileChoice, Room, Post, Draft, SendResult, Attachment } from '../api/room.ts'
  import type { DirectoryEntries } from '../api/file.ts'
  import type { RepoEntry } from '../files/repo.ts'

  // A slash command the composer completes and runs: an action's slash
  // metadata plus the handler that runs it (ui/actions.ts).
  type SlashCommand = { id: string; run: (arg?: string) => unknown } & NonNullable<Action['slash']>

  // One row of the completion menu: a mention, a file, a reference or a
  // command, whichever the token at the caret asks for.
  interface MenuOption {
    value: string
    hint: string
    label?: string
    group?: string
    arg?: string
    file?: RepoEntry
    reference?: Reference
  }

  // The @token at the caret: where it starts and ends, and what it asks for.
  interface Token {
    start: number
    end: number
    text: string
    slash?: boolean
    arg?: SlashCommand
    reference?: boolean
    file?: boolean
  }

  interface Props {
    room: Room
    label?: string
    prepareText?: (text: string) => string | Promise<string>
    choices?: string[]
    profiles?: ProfileChoice[]
    // commands are app actions typed as "/name [arg]": [{name, hint, arg?,
    // options?() -> [{value, hint}], pickRuns?, run(arg) -> error text or
    // nothing}]. A profile with the same name wins, so "/<profile> <task>"
    // always starts a session.
    commands?: SlashCommand[]
    reply?: Post | null
    onopenfile?: (attachment: Attachment & { roomId?: string; postId?: string }) => void
    draft?: string
    onsend: (text: string, atts: { id: string; name: string }[], forwardOf: string, references: Reference[]) => Promise<SendResult | undefined> | SendResult | undefined
    onslash?: () => void
    oncancelreply?: () => void
    onpending?: (pending: boolean) => void
  }
  let { room, label = '', prepareText = (text) => text, choices = [], profiles = [], commands = [], reply = null, onopenfile, draft = $bindable(''), onsend, onslash, oncancelreply = () => {}, onpending = () => {} }: Props = $props()

  // An asynchronous recipient lookup must not outlive its composer.
  let active = true
  onDestroy(() => { active = false })

  let forwarding = $state<Post | null>(null)
  export function forward(post: Post) { if (busy) return; forwarding = post; field?.focus() }

  let references = $state<Reference[]>([])
  let token = $state<Token | null>(null) // {start, end, text} of the @token at the caret
  let referenceOptions = $state<Reference[]>([])
  let referenceLoading = $state(false)
  let fileEntries = $state<RepoEntry[]>([])
  let fileLoading = $state(false)
  let fileError = $state('')
  const fileListings = new Map<string, Promise<DirectoryEntries>>()
  // The workspace answers with a kind string; the file completions use the
  // repo's three kinds.
  const repoEntries = (entries: DirectoryEntries['entries']): RepoEntry[] =>
    (entries ?? []).map((e) => ({ path: e.path, name: e.name, kind: e.kind === 'dir' ? 'dir' : e.kind === 'symlink' ? 'symlink' : 'file' }))
  const fileDirectory = $derived(token && !token.slash && !token.arg && !token.reference ? fileMentionDirectory(token.text) : null)
  $effect(() => {
    const directory = fileDirectory
    const id = room.id
    if (directory === null || !id) return
    let cancelled = false
    fileLoading = true
    fileError = ''
    fileEntries = []
    const key = id + ':' + directory
    if (!fileListings.has(key)) fileListings.set(key, api.listEntries(id, '', directory).catch((error) => { fileListings.delete(key); throw error }))
    fileListings.get(key)?.then((listing) => {
      if (!cancelled) fileEntries = repoEntries(listing.entries)
    }).catch((error) => {
      if (!cancelled) fileError = errorText(error)
    }).finally(() => { if (!cancelled) fileLoading = false })
    return () => { cancelled = true }
  })
  let referenceLoaded = false
  async function fetchReferences() {
    if (referenceLoading || referenceLoaded) return
    referenceLoading = true
    const id = room.id
    try {
      const result = await loadReferences(id)
      if (active && room.id === id) { referenceOptions = result; referenceLoaded = true }
    } catch (e) { if (active) error = errorText(e) }
    finally { if (active) referenceLoading = false }
  }
  $effect(() => {
    const query = token?.reference ? token.text.replace(/^#/, '').trim() : ''
    if (!query) return
    let cancelled = false
    const timer = setTimeout(async () => {
      try {
        const found = await loadReferences(room.id, query)
        if (!active || cancelled) return
        const merged = new Map(referenceOptions.map((reference) => [referenceKey(reference), reference]))
        found.forEach((reference) => merged.set(referenceKey(reference), reference))
        referenceOptions = [...merged.values()]
      } catch (e) { if (active && !cancelled) error = errorText(e) }
    }, 200)
    return () => { cancelled = true; clearTimeout(timer) }
  })

  export function addReference(reference: Reference) {
    if (!references.some((item) => referenceKey(item) === referenceKey(reference))) references = [...references, reference]
    field?.focus()
  }
  const selectedProfile = $derived(profiles.find((profile) => draft.startsWith('/' + profile.name + ' ') || draft === '/' + profile.name))
  async function openCommands() {
    if (!draft.startsWith('/')) draft = '/' + draft
    await tick()
    field?.focus()
    field?.setSelectionRange(1, 1)
    findToken()
  }
  async function openReferences() {
    const pos = field?.selectionStart ?? draft.length
    const prefix = pos && !/\s/.test(draft[pos - 1] ?? '') ? ' #' : '#'
    draft = draft.slice(0, pos) + prefix + draft.slice(pos)
    await tick()
    field?.focus()
    field?.setSelectionRange(pos + prefix.length, pos + prefix.length)
    findToken()
  }

  let field = $state<HTMLTextAreaElement>()
  let busy = $state(false)
  let error = $state('')
  let note = $state('')
  let pick = $state(0)
  let completionList = $state<HTMLElement>()
  const completionID = $props.id()
  const optionID = (option: MenuOption) => completionID + '-option-' + (option.file ? 'file-' : option.reference ? 'reference-' : 'text-') + encodeURIComponent(option.value)
  let preview = $state<Draft | null>(null) // roomsvc.Draft for the current text: who it wakes
  const attachments = attachmentDraft({ room: () => room.id, error: (message) => (error = message), focus: () => field?.focus(), noun: 'post' })
  let failed = $derived($attachments.failed)
  let canSend = $derived(!busy && !$attachments.uploading && !failed && (draft.trim() !== '' || $attachments.ready.length > 0 || !!forwarding || references.length > 0))
  export function addFiles(files: ArrayLike<File> | null | undefined) { attachments.addFiles(files) }
  $effect(() => { onpending(busy || $attachments.items.length > 0 || !!forwarding || references.length > 0) })
  onDestroy(() => { attachments.destroy(); onpending(false) })

  // App commands a profile does not shadow, and the one the draft names.
  let roomCommands = $state<import('../../api.ts').SlashCommand[]>([])
  let commandLoad: Promise<boolean> | null = null
  let commandRoom = ''
  $effect(() => { room.id; roomCommands = []; commandLoad = null; commandRoom = '' })
  function loadCommands(): Promise<boolean> {
    const roomID = room.id
    if (commandLoad && commandRoom === roomID) return commandLoad
    commandRoom = roomID
    commandLoad = api.commands(roomID).then((list) => {
      if (room.id === roomID && active) roomCommands = list
      return true
    }).catch((e) => {
      if (room.id === roomID && active) { error = errorText(e); commandLoad = null }
      return false
    })
    return commandLoad
  }
  let appCommands = $derived(commands.filter((c) => !profiles.some((p) => p.name === c.name)))
  let availableRoomCommands = $derived(roomCommands.filter((c) => (c.source === 'skill' || c.source === 'mcp-prompt') && !profiles.some((p) => p.name === c.name) && !appCommands.some((a) => a.name === c.name)))
  let roomCommand = $derived(matchCommand(availableRoomCommands, draft))
  let command = $derived.by(() => {
    const m = draft.match(/^\/([\w-]+)(?:\s+([\s\S]*))?$/)
    const c = m && appCommands.find((x) => x.name === m[1])
    return c ? { c, arg: (m[2] ?? '').trim(), spaced: /^\/[\w-]+\s/.test(draft) } : null
  })

  let commandLine = $derived(command ? `/${command.c.name}${command.c.arg && !command.arg ? ` ‹${command.c.arg}›` : ''} · ${command.c.hint}` : '')
  const composerMode = $derived(command ? 'Command' : selectedProfile ? 'Start session' : 'Message')

  // A token is "@name" anywhere, "/profile" or "/command" at the very start
  // of the draft (only a draft's first word can start a session), or the
  // argument of a command that offers choices.
  let options = $derived<MenuOption[]>(
    token?.reference ? matchReferences(referenceOptions, token.text).map((reference) => ({ value: referenceKey(reference), hint: reference.title + ' · ' + reference.state, label: referenceLabel(reference), reference })) :
    token?.arg ? token.arg.options?.() ?? [] :
    token?.slash ? [
      ...profiles.map((p) => ({ value: '/' + p.name, group: 'Start a session with', hint: profileHint(p) })),
      ...appCommands.map((c) => ({ value: '/' + c.name, group: 'Commands', arg: c.arg, hint: c.hint })),
      ...commandOptions(availableRoomCommands),
    ] :
    [...mentionOptions(choices).map((option) => ({ ...option, group: 'Sessions' })), ...fileMentionOptions(fileEntries, token?.text || '@')]
  )
  let matches = $derived.by((): MenuOption[] => {
    const t = token
    if (!t) return []
    if (t.reference) return options
    if (t.arg) return options.filter((o) => `${o.value} ${o.hint}`.toLowerCase().includes(t.text.toLowerCase())).slice(0, 8)
    if (t.slash) return matchMentions(options, t.text, 20)
    return [...matchMentions(options.filter((option) => !option.file), t.text, 6), ...options.filter((option) => option.file)]
  })
  // One line for every token kind that finds nothing, so "/cl" and "@xx"
  // never open an empty popup with no explanation.
  let emptyLine = $derived.by((): string => {
    const t = token
    if (!t || matches.length) return ''
    if (fileDirectory !== null && (fileLoading || fileError)) return ''
    if (t.reference) return referenceLoading ? 'Loading PRs and issues…' : `No PRs or issues match “${t.text}”.`
    if (t.slash) return `No session or command matches “${t.text}”.`
    if (t.arg) return `No choice matches “${t.text}”.`
    return `No sessions or files match “${t.text}”.`
  })
  // The option the menu marks as active, for aria-activedescendant.
  let activeOption = $derived.by(() => {
    const option = matches[pick]
    return option ? optionID(option) : undefined
  })
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
    const id = room.id
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
    spawn ? (spawn.error || `New session @${spawn.name || 'agent-…'} · ${spawn.profile}${spawn.model ? ' (' + modelLabel(spawn.model) + ')' : ''} · in ${spawn.dir}`) :
    preview.self ? 'To root only · external orchestrators watching this room' :
    !preview.wakes?.length ? `To ${(preview.mentions ?? []).join(' ')} · wakes nobody` :
    `To ${preview.wakes.join(', ')} · ${preview.wakes.length} wake`
  )


  function findToken() {
    const pos = field?.selectionStart ?? 0
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
    const withArg = lead ? appCommands.find((c) => c.name === lead[1] && c.options) : undefined
    const argStart = lead ? lead[0].length : 0
    if (start === 0 && text.startsWith('/') && !text.startsWith('//')) {
      if (!token?.slash) {
        onslash?.()
        commandLoad = null
        void loadCommands()
      }
      token = { start, end: pos, text, slash: true }
    }
    else if (withArg && pos >= argStart && !draft.slice(argStart, pos).includes(' ')) token = { start: argStart, end: pos, text: draft.slice(argStart, pos), arg: withArg }
    else if (text.startsWith('#')) { token = { start, end: pos, text, reference: true }; fetchReferences() }
    else token = text.startsWith('@') ? { start, end: pos, text } : null
    pick = 0
  }

  async function complete(option?: MenuOption) {
    if (!token || !option) return
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
    if (token.reference) {
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
    field?.focus()
    field?.setSelectionRange(caret, caret)
  }

  async function runCommand(c: SlashCommand, arg: string) {
    error = ''
    busy = true
    try {
      const err = await c.run(arg)
      if (err) error = String(err)
      else draft = ''
    } catch (e) {
      error = errorText(e)
    } finally {
      busy = false
      await tick()
      field?.focus()
    }
  }

  async function send() {
    if (busy) return
    if (draft.startsWith('/') && !draft.startsWith('//') && !command && !selectedProfile) {
      const original = draft
      busy = true
      const loaded = await loadCommands()
      await tick()
      busy = false
      if (!loaded || !active || draft !== original) return
    }
    if (roomCommand && !forwarding) {
      if (roomCommand.item.source === 'skill' && !roomCommand.text) { error = 'Type a message for the skill.'; return }
      if (references.length || $attachments.items.length || reply) { error = 'Send the command without files, references, or a reply.'; return }
      error = ''
      busy = true
      try {
        const text = roomCommand.text ? await prepareText(roomCommand.text) : ''
        if (!active) return
        const result = await api.runCommand(room.id, roomCommand.item.name, text)
        if (result.failed?.length) error = result.failed.map((f) => `${short(f.to, room.name)}: ${f.error || 'delivery failed'}`).join(' · ')
        else { note = result.deliveries?.length ? `Delivered to ${result.deliveries.length} recipient${result.deliveries.length === 1 ? '' : 's'}` : 'Posted'; setTimeout(() => (note = ''), 2400) }
        if (result.message.id) draft = ''
      }
      catch (e) { error = errorText(e) }
      finally { busy = false; await tick(); field?.focus() }
      return
    }
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
      const msg = errorText(e)
      error = msg
    } finally {
      busy = false
      await tick()
      if (active) field?.focus()
    }
  }

  function keydown(e: KeyboardEvent) {
    if (e.key === 'Escape' && token) { e.preventDefault(); token = null; return }
    const selected = matches[pick]
    if (matches.length) {
      if (e.key === 'ArrowDown') { e.preventDefault(); pick = (pick + 1) % matches.length; return }
      if (e.key === 'ArrowUp') { e.preventDefault(); pick = (pick - 1 + matches.length) % matches.length; return }
      // Enter on a typed command that needs nothing more runs it.
      if (e.key === 'Enter' && !e.shiftKey && ((command && !command.c.arg && selected?.value === '/' + command.c.name) || (roomCommand?.item.source === 'mcp-prompt' && !roomCommand.item.args.length && selected?.value === '/' + roomCommand.item.name))) { e.preventDefault(); token = null; send(); return }
      if (e.key === 'Tab' || (e.key === 'Enter' && !e.shiftKey)) { e.preventDefault(); complete(selected); return }
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
  {#if emptyLine}
    <div class="pop status" role="status">{emptyLine}</div>
  {/if}
  {#if matches.length}
    <div class="pop" id={completionID} role="listbox" aria-label="Composer suggestions" bind:this={completionList}>
      {#if !matches[0]?.group}<div class="pophead">{token?.reference ? 'Attach a PR or issue' : token?.arg ? token.arg.listTitle || token.arg.arg : 'Mention'}</div>{/if}
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
  {#if fileDirectory !== null && (fileLoading || fileError)}
    <div class="file-status" role="status">{fileLoading ? 'Loading files…' : 'Files unavailable: ' + fileError}</div>
  {/if}
  <ComposerBox onreference={openReferences} {room} {forwarding} onremoveforward={() => (forwarding = null)} {onopenfile} {attachments} bind:draft bind:field placeholder={`Message ${label || roomLabel(room)}`} {busy} {canSend} joined={!!reply} bad={!!(error || (spawn?.error && !command && !roomCommand))} spawn={!!(spawn && !spawn.error)} oninput={() => { findToken(); error = '' }} onclick={findToken} onkeydown={keydown} onsend={send} mode={composerMode} model={selectedProfile?.model || ''} {completionID} expanded={!!matches.length} {activeOption}>
    {#snippet context()}<ReferenceChips {references} disabled={busy} onremove={(reference) => (references = references.filter((item) => referenceKey(item) !== referenceKey(reference)))} />{/snippet}
    {#snippet tools()}
      <button class="command-chip" type="button" aria-label="Browse commands" title="Type / for sessions and commands" disabled={busy} onclick={openCommands}>/</button>
    {/snippet}
  </ComposerBox>
  {#if error || failed || command || roomCommand || spawn || wakeLine || note}
  <div class="foot">
    {#if error}
      <span class="err">{error}</span>
    {:else if failed}
      <span class="err">A file did not save. Retry it or remove it before you send.</span>
    {:else if command}
      <span class="go">{commandLine} · <kbd>Enter</kbd> to run</span>
    {:else if roomCommand}
      <span class="go">/{roomCommand.item.name} · {roomCommand.item.description} · <kbd>Enter</kbd> to send</span>
    {:else if spawn?.error}
      <span class="err">{spawn.error}</span>
    {:else if spawn}
      <span class="go">{wakeLine} · <kbd>Enter</kbd> to start</span>
    {:else if wakeLine}
      <span class:warn={!preview?.wakes?.length}>{wakeLine} · <kbd>Enter</kbd> to send</span>
    {:else if note}
      <span class="ok">{note}</span>
    {/if}
  </div>
  {/if}
</div>

<style>
  .file-status { padding: 5px var(--s2); font-size: var(--t-sm); color: var(--muted); }
  .pop.status { padding: 8px var(--s3); font-size: var(--t-sm); color: var(--muted); }
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
