<script>
  import { speaker } from '../ui/posts.js'
  import { tick, onDestroy } from 'svelte'
  import { short, roomLabel } from '../ui/format.js'
  import { api } from '../../api.js'
  import MessageInput from '../ui/MessageInput.svelte'
  import { attachmentDraft } from '../files/attachmentDraft.ts'
  import { mentionOptions, matchMentions } from './mentions.js'

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

  let field = $state()
  let busy = $state(false)
  let error = $state('')
  let note = $state('')
  let pick = $state(0)
  let preview = $state(null) // roomsvc.Draft for the current text: who it wakes
  let token = $state(null) // {start, end, text} of the @token at the caret
  const attachments = attachmentDraft({ room: () => room?.id, error: (message) => (error = message), focus: () => field?.focus(), noun: 'post' })
  let failed = $derived($attachments.failed)
  let canSend = $derived(!busy && !$attachments.uploading && !failed && (draft.trim() !== '' || $attachments.ready.length > 0 || !!forwarding))
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
    token?.arg ? token.arg.options() :
    token?.slash ? [
      ...profiles.map((p) => ({ value: '/' + p.name, group: 'Start a session with', hint: [p.harness, p.model].filter(Boolean).join(' · ') + (p.default ? ' · default' : '') })),
      ...appCommands.map((c) => ({ value: '/' + c.name, group: 'Commands', arg: c.arg, hint: c.hint })),
    ] :
    mentionOptions(choices))
  let matches = $derived(token ? (token.arg ? options.filter((o) => `${o.value} ${o.hint}`.toLowerCase().includes(token.text.toLowerCase())).slice(0, 8) : matchMentions(options, token.text, token.slash ? 20 : 8)) : [])
  let spawn = $derived(preview?.spawn ?? null)

  // The Go parser decides who a draft goes to; ask it shortly after typing stops.
  $effect(() => {
    const text = draft
    const prepare = prepareText
    const id = room?.id
    const files = $attachments.items.length
    if (!id || (!text.trim() && !files)) { preview = null; return }
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
    const start = Math.max(before.lastIndexOf(' '), before.lastIndexOf('\n'), before.lastIndexOf('\t')) + 1
    const text = before.slice(start)
    const lead = draft.match(/^\/([\w-]+)\s+/)
    const withArg = lead && appCommands.find((c) => c.name === lead[1] && c.options)
    if (start === 0 && text.startsWith('/') && !text.startsWith('//')) {
      if (!token?.slash) onslash?.() // the list may have changed since launch
      token = { start, end: pos, text, slash: true }
    }
    else if (withArg && pos >= lead[0].length && !draft.slice(lead[0].length, pos).includes(' ')) token = { start: lead[0].length, end: pos, text: draft.slice(lead[0].length, pos), arg: withArg }
    else token = text.startsWith('@') ? { start, end: pos, text } : null
    pick = 0
  }

  async function complete(choice) {
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
    if (command && !forwarding) return runCommand(command.c, command.arg)
    if (!canSend) return
    error = ''
    busy = true
    try {
      const atts = $attachments.atts
      const text = await prepareText(draft)
      if (!active) return
      const sent = await onsend(text, atts, forwarding?.id ?? '')
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
    if (matches.length) {
      if (e.key === 'ArrowDown') { e.preventDefault(); pick = (pick + 1) % matches.length; return }
      if (e.key === 'ArrowUp') { e.preventDefault(); pick = (pick - 1 + matches.length) % matches.length; return }
      // Enter on a typed command that needs nothing more runs it.
      if (e.key === 'Enter' && !e.shiftKey && command && !command.c.arg && matches[pick].value === '/' + command.c.name) { e.preventDefault(); token = null; send(); return }
      if (e.key === 'Tab' || (e.key === 'Enter' && !e.shiftKey)) { e.preventDefault(); complete(matches[pick].value); return }
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
  {#if matches.length}
    <div class="pop" role="listbox">
      {#if !matches[0].group}<div class="pophead">{token?.arg ? token.arg.listTitle || token.arg.arg : 'Mention'}</div>{/if}
      {#each matches as m, i}
        {#if m.group && m.group !== matches[i - 1]?.group}<div class="pophead">{m.group}</div>{/if}
        <button class="opt" class:on={i === pick} role="option" aria-selected={i === pick} onmousedown={(e) => { e.preventDefault(); complete(m.value) }}>
          <span class="mono">{m.value}{#if m.arg}<span class="argph"> ‹{m.arg}›</span>{/if}</span>
          <span class="kind">{m.hint}</span>
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
  <MessageInput {room} {forwarding} onremoveforward={() => (forwarding = null)} {onopenfile} {attachments} bind:draft bind:field placeholder={`Message #${label || roomLabel(room)}`} {busy} {canSend} joined={!!reply} bad={!!(error || (spawn?.error && !command))} spawn={!!(spawn && !spawn.error)} oninput={() => { findToken(); error = '' }} onclick={findToken} onkeydown={keydown} onsend={send} />
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
      <span><kbd>Enter</kbd> to send · <kbd>Shift</kbd>+<kbd>Enter</kbd> new line · <span class="mono">@</span> to mention · <span class="mono">/</span> for sessions and commands</span>
    {/if}
  </div>
</div>

<style>
  .composer { position: relative; padding: 0 var(--s4) 6px; flex: none; }
  /* A phone: 16 px text stops iOS zooming on focus; wider tap targets. */
  @media (max-width: 760px) {
    .composer { padding: 0 var(--s2) var(--s1); }
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
    padding: 6px var(--s2);
    border-radius: 5px;
    color: var(--ink-2);
  }
  .opt.on { background: var(--select); color: var(--ink); }
  .opt .mono { font-size: var(--t-ui); }
  .kind { font-size: var(--t-sm); color: var(--faint); }
</style>
