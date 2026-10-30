<script lang="ts">
  import { isFinished } from '../rooms/memberOrder.ts'
  import { sessionStatus } from './status.ts'
  import { tick, onDestroy, onMount } from 'svelte'
  import { dialog } from '../ui/dialog.js'
  import { threadHeaderSwipe } from './threadHeaderSwipe.ts'
  import EmptyState from '../ui/EmptyState.svelte'
  import Skeleton from '../ui/Skeleton.svelte'
  import Confirm from '../ui/Confirm.svelte'
  import HistoryList from '../rooms/HistoryList.svelte'
  import DmMessage from './DmMessage.svelte'
  import { createSelection } from '../ui/selection.svelte.ts'
  import DmRows from './DmRows.svelte'
  import ThreadChip from './ThreadChip.svelte'
  import { dmView, groupMessages, listedThreadCount, plain, threadPanel, threadPosts } from './dm.ts'
  import { api } from '../../api.ts'
  import { short, ago } from '../ui/format.ts'
  import ComposerBox from '../ui/ComposerBox.svelte'
  import { commandOptions, matchCommand } from '../rooms/slashCompletion.ts'
  import { matchMentions } from '../rooms/mentions.ts'
  import type { SlashCommand } from '../../api.ts'
  import { unreadableOutput, unreadableNotice } from '../settings/support.ts'
  import { attachmentDraft } from '../files/attachmentDraft.ts'
  import { showReasoning, showCompaction } from '../ui/prefs.ts'
  import { fade, fly } from 'svelte/transition'
  import { cubicOut } from 'svelte/easing'
  import { arrivals, motion, BASE } from '../ui/motion.ts'
  import { errorText } from '../errorText.ts'
  import type { Attachment, Member, PostResult, Room, SendResult } from '../api/room.ts'
  import type { QueueItem, Session as SessionData } from '../api/session.ts'
  import type { ShellJob } from '../api/terminal.ts'
  import type { ProfileView } from '../settings/support.ts'
  import type { DmRow, MessageRow, PanelRow, Receipt, ThreadPost } from './dm.ts'

  interface Props {
    readOnly?: boolean
    onstop?: ((member: Member) => void) | null
    onreopen?: ((member: Member) => Promise<void>) | null
    reopening?: string
    ondismisssession?: ((member: Member) => Promise<void>) | null
    dismissing?: string
    session?: SessionData | null
    error?: string
    room: Room
    onharness?: ((name?: string) => void) | null
    onsend: (text: string, attachments: { id: string; name: string }[], forwardId: string) => Promise<PostResult | SendResult | undefined>
    queue?: QueueItem[]
    onunqueue?: ((pending: Receipt) => Promise<void>) | null
    ondismiss?: ((sessionId: string, postId: string) => Promise<void>) | null
    focusedTurn?: number
    onrecent?: (() => void) | null
    onearlier?: ((near?: () => boolean) => Promise<boolean>) | null
    onnewer?: ((near?: () => boolean) => Promise<boolean>) | null
    onpresent?: (() => Promise<boolean>) | null
    members?: Member[]
    jobs?: ShellJob[]
    onopenfile: (attachment: Attachment) => void
    onpost?: ((postId: string) => void) | null
    onissue?: ((issue: string) => void) | null
    onchild: (member: Member) => void
    // A member row of the thread panel opens that session; the parent row and
    // the sibling rows use the same way in as a child row.
    onsession?: ((member: Member) => void) | null
    onjob: (job: ShellJob) => void
    threadsOpen?: boolean
    selectedThread?: string
    draft?: string
    readingHistory?: boolean
    onpending?: ((pending: boolean) => void) | null
  }
  let { readOnly = false, onstop = null, onreopen = null, reopening = '', ondismisssession = null, dismissing = '', session = null, error = '', room, onharness = null, onsend, queue = [], onunqueue = null, ondismiss = null, focusedTurn = 0, onrecent = null, onearlier = null, onnewer = null, onpresent = null, members = [], jobs = [], onopenfile, onpost = null, onissue = null, onchild, onsession = onchild, onjob, threadsOpen = $bindable(false), selectedThread = $bindable(''), draft = $bindable(''), readingHistory = $bindable(false), onpending = null }: Props = $props()
  // One selected post for the message list and one for the open thread (postSelect.ts).
  const listSelection = createSelection()
  const threadSelection = createSelection()

  let m = $derived(session?.member)
  let turns = $derived(session?.turns || [])
  let children = $derived(members.filter((child) => !!m?.id && child.parent === m.id))
  let dm = $derived(dmView(session, room, { reasoning: $showReasoning, compaction: $showCompaction }, queue, children))
  // Rows that arrive while the DM is open rise in; the rows it opened with stay still.
  const arrived = arrivals(), threadArrived = arrivals()
  let isNew = $derived(arrived(m?.id, dm.rows.map((r) => r.key)))
  let selected = $derived(dm.threads.find((t) => t.key === selectedThread) ?? (selectedThread ? { key: selectedThread, posts: [] } : null))
  // The panel keeps the threads it groups, and the threads it leaves to the
  // Children, Parent and Siblings sections are gone from Other threads.
  let panel = $derived(threadPanel(dm.threads, members, m ?? {}))
  // The panel header counts the threads the panel lists, so its number always
  // matches the rows below it. A closed child's thread leaves with it.
  let listed = $derived(listedThreadCount(dm.threads, members, m ?? {}))
  let folded = $state({ siblings: true, others: true })
  let threadRows = $derived(groupMessages((selected?.posts || []).map((p): MessageRow => ({ kind: 'message', key: p.id, author: p.from, ts: p.ts, text: p.text, post: p, delivery: p.delivery, pending: p.pending }))))
  let isNewInThread = $derived(threadArrived(selected?.key, threadRows.map((r) => r.key)))
  let threadScroller = $state<HTMLElement>()
  let threadStuck = $state(true)
  $effect(() => {
    if (!threadsOpen) return
    selected?.posts?.length
    if (threadStuck && !session?.newer) tick().then(() => { if (threadScroller) threadScroller.scrollTop = threadScroller.scrollHeight })
  })
  function openThread(key: string) { selectedThread = key; threadsOpen = true }
  let threadOverlay = $state(false)
  onMount(() => {
    const query = matchMedia('(max-width: 1099px)')
    const update = () => { threadOverlay = query.matches }
    update()
    query.addEventListener('change', update)
    return () => query.removeEventListener('change', update)
  })
  function threadModal(node: HTMLElement, enabled: boolean) {
    let active: ReturnType<typeof dialog> | undefined
    const update = (modal: boolean) => {
      if (modal && !active) active = dialog(node, {
        close: () => { threadsOpen = false },
        initial: () => node.querySelector<HTMLButtonElement>('.panel-head button:last-child'),
      })
      else if (!modal && active) { active.destroy(); active = undefined }
    }
    update(enabled)
    return { update, destroy: () => active?.destroy() }
  }
  function backFromThread() {
    if (selectedThread) selectedThread = ''
    else threadsOpen = false
  }

  // The profile's running output health: a live turn whose stdout Dyna
  // cannot read. Poll the one profile while the turn runs. Errors stay quiet.
  let check = $state<ProfileView | null>(null)
  $effect(() => {
    const name = m?.profile
    const live = m?.state === 'live'
    if (!name) return
    let stop = false
    const load = () => api.configCheckProfile(name, false).then((c: ProfileView) => { if (!stop) check = c }, () => { if (!stop) check = null })
    load()
    if (!live) return () => { stop = true }
    const timer = setInterval(load, 6000)
    return () => { stop = true; clearInterval(timer) }
  })
  let unreadable = $derived(unreadableOutput(m, check))
  let scroller = $state<HTMLElement>()
  let forwarding = $state<ThreadPost | null>(null)
  function forward(post: ThreadPost) { if (busy) return; forwarding = post; threadsOpen = false; field?.focus() }
  let field = $state<HTMLTextAreaElement>()
  let busy = $state(false)
  let sendError = $state('')
  let stuck = $state(true)
  const attachments = attachmentDraft({ room: () => room?.id, error: (message) => (sendError = message), focus: () => field?.focus(), disabled: () => !!m?.closed, noun: 'message' })
  let failed = $derived($attachments.failed)
  let canSend = $derived(!busy && !$attachments.uploading && !failed && !m?.closed && (draft.trim() !== '' || $attachments.ready.length > 0 || !!forwarding))
  export function addFiles(files: ArrayLike<File>) { attachments.addFiles(files) }
  $effect(() => { onpending?.(busy || $attachments.items.length > 0 || !!forwarding) })
  onDestroy(() => { attachments.destroy(); onpending?.(false) })

  let reopenError = $state('')
  $effect(() => { m?.id; reopenError = '' })
  async function dismiss() {
    if (!m) return
    const id = m.id
    reopenError = ''
    try { await ondismisssession?.(m) }
    catch (e) { if (m?.id === id) reopenError = `Could not dismiss this session: ${errorText(e)}` }
  }
  async function reopen() {
    if (!m) return
    const id = m.id
    reopenError = ''
    try { await onreopen?.(m) }
    catch (e) { if (m?.id === id) reopenError = `Could not reopen this session: ${errorText(e)}` }
  }

  let lastKey = ''
  let removing = $state('')
  let queueError = $state('')
  let dismissConfirm = $state<{ post: Receipt; session: string } | null>(null)
  // Pending posts in the transcript and in the thread panel share one confirm.
  let askDismiss = $derived(ondismiss ? (post: Receipt) => { if (m?.id) dismissConfirm = { post, session: m.id }; queueError = '' } : null)
  async function dismissPending() {
    if (!dismissConfirm || !ondismiss) return
    const { post, session: id } = dismissConfirm
    removing = post.id
    queueError = ''
    try {
      await ondismiss(id, post.id)
      dismissConfirm = null
    } catch (e) { queueError = errorText(e) }
    finally { removing = '' }
  }
  async function unqueue(q: Receipt) {
    removing = q.id
    queueError = ''
    try {
      await onunqueue?.(q)
    } catch (e) {
      queueError = errorText(e)
    } finally {
      removing = ''
    }
  }

  $effect(() => { readingHistory = !stuck || (threadsOpen && !threadStuck) })

  // Follow the bottom while new items arrive, unless the reader scrolled up.
  $effect(() => {
    const t = turns.at(-1)
    const key = `${m?.id}:${turns.length}:${t?.items?.length}:${t?.status}:${session?.posts?.length}:${queue.map((q) => `${q.id}:${q.state}`).join(',')}:${t?.items?.at(-1)?.text}:${t?.items?.at(-1)?.aside}`
    if (key === lastKey) return
    lastKey = key
    if (stuck && !session?.newer) tick().then(() => scroller && (scroller.scrollTop = scroller.scrollHeight))
  })


  async function present() {
    const loaded = await onpresent?.()
    if (loaded !== false) { stuck = true; threadStuck = true }
    return loaded
  }

  let commands = $state<SlashCommand[]>([])
  let commandLoad: Promise<boolean> | null = null
  let menu = $state(false)
  let pick = $state(0)
  const completionID = $props.id()
  let matches = $derived(menu ? matchMentions(commandOptions(commands), draft.slice(0, field?.selectionStart ?? draft.length), 20) : [])
  $effect(() => { room.id; commands = []; commandLoad = null; menu = false })
  function loadCommands() {
    const id = room.id
    if (!commandLoad) commandLoad = api.commands(id).then((list) => {
      if (room.id === id) commands = list
      return true
    }).catch((e) => {
      if (room.id === id) { sendError = errorText(e); commandLoad = null }
      return false
    })
    return commandLoad
  }
  function findCommand() {
    const text = draft.slice(0, field?.selectionStart ?? draft.length)
    const open = /^\/[^\s]*$/.test(text) && !text.startsWith('//')
    if (open && !menu) { commandLoad = null; void loadCommands() }
    menu = open
    pick = 0
  }
  async function complete(value: string) {
    draft = value + ' ' + draft.slice(field?.selectionStart ?? draft.length)
    menu = false
    await tick()
    field?.focus()
    field?.setSelectionRange(value.length + 1, value.length + 1)
  }

  async function send() {
    if (!canSend) return
    if (draft.startsWith('/') && !draft.startsWith('//') && !forwarding) {
      const original = draft
      const target = dm.address
      busy = true
      const loaded = await loadCommands()
      await tick()
      busy = false
      if (!loaded || draft !== original || dm.address !== target) return
      const command = matchCommand(commands, draft)
      if (command) {
        if (command.item.source === 'skill' && !command.text) { sendError = 'Type a message for the skill.'; return }
        if ($attachments.items.length) { sendError = 'Send the command without files.'; return }
        busy = true
        sendError = ''
        try {
          const result = await api.runCommand(room.id, command.item.name, command.text, target)
          if (result.kind === 'run') { draft = ''; stuck = true }
          else {
            if (result.post.failed?.length) sendError = result.post.failed.map((f) => f.error || 'delivery failed').join(' · ')
            if (result.post.message.id) { draft = ''; stuck = true }
          }
        } catch (e) { sendError = errorText(e) }
        finally { busy = false; await tick(); field?.focus() }
        return
      }
    }
    busy = true
    sendError = ''
    try {
      const res = await onsend(draft, $attachments.atts, forwarding?.id ?? '')
      // A send either posted or started a session; only a post has a message.
      const posted = res && 'message' in res ? res : null
      if (posted?.failed?.length) sendError = posted.failed.map((f) => f.error || 'delivery failed').join(' · ')
      if (posted?.message?.id) {
        attachments.clear()
        forwarding = null
        draft = ''
        stuck = true
      }
    } catch (e) {
      sendError = errorText(e)
    } finally {
      busy = false
      await tick()
      field?.focus()
    }
  }

  function keydown(e: KeyboardEvent) {
    if (menu && e.key === 'Escape') { e.preventDefault(); menu = false; return }
    if (matches.length) {
      if (e.key === 'ArrowDown') { e.preventDefault(); pick = (pick + 1) % matches.length; return }
      if (e.key === 'ArrowUp') { e.preventDefault(); pick = (pick - 1 + matches.length) % matches.length; return }
      if (e.key === 'Tab' || (e.key === 'Enter' && !e.shiftKey)) { e.preventDefault(); const selected = matches[pick]; if (selected) void complete(selected.value); return }
    }
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault()
      send()
    }
  }

  export function focus() {
    field?.focus()
  }
</script>

<section class="session">
  {#if error}
    <p class="err">{error}</p>
  {:else if !session}
    <Skeleton label="Loading session" />
  {:else}
    {@const m = session.member}
    <div class="conversation">

    {#if unreadable}
      <p class="notice" role="status">{unreadableNotice} {#if onharness}<button onclick={() => onharness(m.profile)}>Open profile {m.profile}</button>{/if}</p>
    {/if}

    <div class="log" bind:this={scroller} onloadcapture={(event) => { if (stuck && !session?.newer) event.currentTarget.scrollTop = event.currentTarget.scrollHeight }}>
      {#if focusedTurn}
        <p class="older">Showing turn {focusedTurn}. {#if onrecent}<button onclick={onrecent}>Show recent turns</button>{/if}</p>
      {/if}
      {#key m.id + ':' + focusedTurn}
      <HistoryList bind:atBottom={stuck} {scroller} items={dm.rows} key={(r: DmRow) => r.key} estimate={(r: DmRow) => r.kind === 'message' ? 96 : 40} newer={!focusedTurn && session.newer} onnewer={focusedTurn ? null : onnewer} onpresent={focusedTurn || !onpresent ? null : present} earlier={!focusedTurn && session.earlier} onearlier={focusedTurn ? null : onearlier} onhistory={() => (stuck = false)} beginning={focusedTurn ? '' : 'Beginning of @' + m.name}>
        {#snippet children(row: DmRow)}
          <DmRows {dm} {jobs} {onjob} rows={[row]} {room} selection={listSelection} onforward={readOnly ? null : forward} member={m} {members} {isNew} selectedKey={threadsOpen ? selected?.key ?? '' : ''} onthread={openThread} {onopenfile} {onpost} {onissue} {removing} onunqueue={onunqueue && !readOnly ? unqueue : null} ondismiss={readOnly ? null : askDismiss} />
        {/snippet}
      </HistoryList>
      {/key}
      {#if m.state === 'live'}
        <p class="working"><span class="dot" aria-hidden="true"></span><span>{m.name} is {sessionStatus(m)}</span>{#if onstop && !readOnly}<button onclick={() => onstop(m)}>Stop</button>{/if}</p>
      {/if}
    </div>

    {#if !readOnly}
    <div class="composer">
      {#if queueError && !dismissConfirm}<p class="err" role="alert">{queueError}</p>{/if}
      {#if isFinished(m) && ondismisssession}
        <p class="notice"><span>This session finished and is dormant. A coordination post wakes it. Notices do not. Dismiss closes it.</span> <button onclick={dismiss} disabled={!!dismissing || !!reopening}>{dismissing === m.id ? 'Dismissing…' : 'Dismiss'}</button></p>
      {/if}
      {#if m.closed && m.resumable && onreopen}
        <p class="notice"><span>Reopen this session to send another message.</span> <button onclick={reopen} disabled={!!reopening || !!dismissing}>{reopening === m.id ? 'Reopening…' : 'Reopen'}</button></p>
      {/if}
      {#if reopenError}<p class="err" role="alert">{reopenError}</p>{/if}
      {#if menu && matches.length}
        <div class="command-menu" id={completionID} role="listbox" aria-label="Composer suggestions">
          {#each matches as option, i}
            {#if option.group !== matches[i - 1]?.group}<div class="command-group">{option.group}</div>{/if}
            <button id={completionID + '-' + i} role="option" aria-selected={i === pick} title={option.hint} onmousedown={(e) => e.preventDefault()} onclick={() => complete(option.value)}>
              <span>{option.value}{#if option.arg} ‹{option.arg}›{/if}</span><span>{option.hint}</span>
            </button>
          {/each}
        </div>
      {/if}
      <ComposerBox {completionID} expanded={!!matches.length} activeOption={matches.length ? completionID + '-' + pick : undefined} model={m.profile || m.model || ''} mode="Direct message" {room} {forwarding} onremoveforward={() => (forwarding = null)} {onopenfile} {attachments} bind:draft bind:field placeholder={m.closed ? `${m.name} is closed` : `Message @${m.name}`} {busy} {canSend} bad={!!sendError} closed={!!m.closed} oninput={() => { sendError = ''; findCommand() }} onclick={findCommand} onkeydown={keydown} onsend={send} />
      <div class="foot">
        {#if sendError}
          <span class="err-t">{sendError}</span>
        {:else if failed}
          <span class="err-t">A file did not save. Retry it or remove it before you send.</span>
        {:else if m.closed}
          <span>This session is closed. Its transcript is read-only.</span>
        {:else if m.state === 'live'}
          <span>Your message shows its delivery status in the transcript. <kbd>Enter</kbd> to send · <kbd>Shift</kbd>+<kbd>Enter</kbd> new line</span>
        {:else if m.role === 'root' && !m.id}
          <span>Your message goes to @root, whoever holds the seat. <kbd>Enter</kbd> to send · <kbd>Shift</kbd>+<kbd>Enter</kbd> new line</span>
        {:else}
          <span>Your message starts turn {m.turns + 1}. <kbd>Enter</kbd> to send · <kbd>Shift</kbd>+<kbd>Enter</kbd> new line</span>
        {/if}
      </div>
    </div>
    {/if}
    </div>
    {#if threadsOpen}
      <div class="thread-wrap" class:overlay={threadOverlay}>
      <button class="thread-scrim" aria-label="Close threads" onclick={() => (threadsOpen = false)} transition:fade={{ duration: motion(BASE) }}></button>
      <aside class="thread-panel" role={threadOverlay ? 'dialog' : undefined} aria-modal={threadOverlay ? true : undefined} tabindex="-1" use:threadModal={threadOverlay} aria-label="Session threads" transition:fly={{ x: 24, duration: motion(BASE), easing: cubicOut }}>
        {#if selected}
          {@const count = selected.posts.length}
          <header class="panel-head" use:threadHeaderSwipe={{ enabled: threadOverlay, back: backFromThread }}><button class="back" onclick={() => (selectedThread = '')} aria-label="Back to threads">‹</button><strong>{short(selected.key, room.name)}<small>with {m.name} · {count} message{count === 1 ? '' : 's'}</small></strong><button onclick={() => (threadsOpen = false)} aria-label="Close threads">✕</button></header>
          <div class="thread-log" bind:this={threadScroller}>
            {#key m.id + ':' + selected.key + ':' + focusedTurn}
            <HistoryList bind:atBottom={threadStuck} fill={false} scroller={threadScroller} items={threadRows} key={(r: DmRow) => r.key} estimate={() => 96} newer={!focusedTurn && session.newer} onnewer={focusedTurn ? null : onnewer} onpresent={focusedTurn || !onpresent ? null : present} earlier={!focusedTurn && session.earlier} onearlier={focusedTurn ? null : onearlier} onhistory={() => (threadStuck = false)} beginning={focusedTurn ? '' : 'Beginning of thread with ' + short(selected.key, room.name)}>
            {#snippet children(row: MessageRow)}<DmMessage {row} {room} onforward={readOnly ? null : forward} member={m} address={dm.address} {members} fullText {onopenfile} {onpost} {removing} ondismiss={readOnly ? null : askDismiss} inThread selection={threadSelection} enter={isNewInThread(row.key)} />{/snippet}
            </HistoryList>
            {/key}
          </div>
        {:else}
          <header class="panel-head" use:threadHeaderSwipe={{ enabled: threadOverlay, back: backFromThread }}><strong>Threads<small>{m.name} · {listed} thread{listed === 1 ? '' : 's'}</small></strong><button onclick={() => (threadsOpen = false)} aria-label="Close threads">✕</button></header>
          <div class="thread-list">
            {#if panel.children.length}
              <h3>Children<small>{panel.children.length}</small></h3>
              {#each panel.children as row (row.member.id ?? row.member.address)}{@render memberRow(row)}{/each}
            {/if}
            {#if panel.parent}
              <h3>Parent</h3>
              {@render memberRow(panel.parent)}
            {/if}
            {#if panel.siblings.length}
              <h3><button class="section" aria-expanded={!folded.siblings} onclick={() => (folded = { ...folded, siblings: !folded.siblings })}><span class="chev" aria-hidden="true">{folded.siblings ? '▸' : '▾'}</span>Siblings<small>{panel.siblings.length}</small></button></h3>
              {#if !folded.siblings}
                {#each panel.siblings as row (row.member.id ?? row.member.address)}{@render memberRow(row)}{/each}
              {/if}
            {/if}
            {#if panel.others.length}
              <h3><button class="section" aria-expanded={!folded.others} onclick={() => (folded = { ...folded, others: !folded.others })}><span class="chev" aria-hidden="true">{folded.others ? '▸' : '▾'}</span>Other threads<small>{panel.others.length}</small></button></h3>
              {#if !folded.others}
                {#each panel.others as thread (thread.key)}
                  {@const person = members.find((p) => p.address === thread.key)}
                  <div class="panel-row"><ThreadChip glyph="▱" name={short(thread.key, room.name)} emoji={person?.emoji} hue={person?.hue} badge={`${thread.posts.length} message${thread.posts.length === 1 ? '' : 's'}`} snippet={plain(thread.posts.at(-1)?.text)} time={ago(thread.posts.at(-1)?.ts)} selected={selectedThread === thread.key} onopen={() => openThread(thread.key)} /></div>
                {/each}
              {/if}
            {/if}
            {#if !panel.children.length && !panel.parent && !panel.siblings.length && !panel.others.length}<EmptyState title="No threads yet">Posts between agents appear here.</EmptyState>{/if}
          </div>
        {/if}
      </aside>
      </div>
    {/if}
  {/if}
</section>

{#snippet memberRow(row: PanelRow<Member>)}
  {@const posts = threadPosts(row)}
  <div class="panel-row">
    <ThreadChip name={row.member.name} emoji={row.member.emoji} hue={row.member.hue} state={row.member.state} status={row.member.status} note={sessionStatus(row.member)} title={`Open ${row.member.name}'s messages`} onopen={() => onsession?.(row.member)} />
    {#if row.thread && posts}
      {@const thread = row.thread}
      <button class="thread-count" title={`Posts between ${session?.member.name ?? ''} and ${row.member.name}`} onclick={() => openThread(thread.key)}>{posts} post{posts === 1 ? '' : 's'}<span class="when"> · {ago(thread.posts.at(-1)?.ts)}</span> ›</button>
    {:else}
      <span class="thread-empty" title={`No posts between ${session?.member.name ?? ''} and ${row.member.name} yet`}>No posts</span>
    {/if}
  </div>
{/snippet}

{#if dismissConfirm}
  <Confirm title="Dismiss pending message?" lines={[`Message ${dismissConfirm.post.id} from ${dismissConfirm.post.from}.`, 'This session will not receive the message. History stays available. Active delivery cannot be recalled.']} action="Dismiss message" busy={!!removing} error={queueError} onconfirm={dismissPending} oncancel={() => { dismissConfirm = null; queueError = '' }} />
{/if}

<style>
  .session { flex: 1; display: flex; min-height: 0; min-width: 0; position: relative; }
  .conversation { flex: 1; display: flex; flex-direction: column; min-height: 0; min-width: 0; }
  .err { color: var(--muted); padding: var(--s4) var(--s5); }
  .err { color: var(--coral); }

  .log { position: relative; overflow-anchor: none; flex: 1; min-width: 0; overflow-y: auto; padding: var(--s2) 0 var(--s4); }
  .older { margin: var(--s2) 0 0; font-size: var(--t-sm); color: var(--faint); text-align: center; }
  .older button { color: var(--ink-2); text-decoration: underline; padding: var(--s2); }
  .notice { color: var(--gold); padding: var(--s2) var(--s4); margin: 0; font-size: var(--t-ui); border-bottom: 1px solid var(--line-soft); }
  .notice button { color: inherit; text-decoration: underline; font: inherit; }
  .working { display: flex; align-items: center; gap: var(--s2); margin: 14px var(--s4) 0 56px; font-size: var(--t-ui); color: var(--gold); }
  .working button { flex: none; padding: var(--s2) var(--s1); color: var(--coral); }
  .dot { flex: none; width: 8px; height: 8px; border-radius: 50%; background: var(--gold); animation: pulse 1.6s ease-in-out infinite; }
  @keyframes pulse { 50% { opacity: 0.35; } }

  .composer { position: relative; padding: 0 var(--s4) 6px; flex: none; }
  .command-menu { position: absolute; left: 16px; right: 16px; bottom: calc(100% - 4px); background: var(--side); border: 1px solid var(--line); border-radius: 8px; padding: 6px; box-shadow: 0 8px 24px #0006; z-index: 10; max-height: min(420px, 60dvh); overflow-y: auto; }
  .command-group { padding: 6px 8px; color: var(--faint); font-size: var(--t-cap); }
  .command-menu button { display: flex; width: 100%; justify-content: space-between; gap: var(--s2); padding: 8px; text-align: left; color: var(--ink); }
  .command-menu button[aria-selected="true"] { background: var(--select); }
  .command-menu button span:last-child { color: var(--muted); }
  .foot { min-height: 22px; padding: 3px var(--s1) 0; font-size: var(--t-sm); color: var(--faint); }
  .err-t { color: var(--coral); }
  kbd { font-family: var(--sans); font-size: var(--t-cap); font-weight: 600; color: var(--muted); }
  .thread-panel { width: 360px; flex: none; background: var(--side); border-left: 1px solid var(--line-soft); display: flex; flex-direction: column; min-height: 0; }
  .panel-head { display: flex; align-items: center; gap: var(--s2); min-height: 52px; padding: 0 14px; border-bottom: 1px solid var(--line-soft); }
  .panel-head strong { flex: 1; overflow-wrap: anywhere; }
  .panel-head small { display: block; font-weight: 400; font-size: var(--t-cap); color: var(--faint); }
  .panel-head .back { width: 24px; font-size: var(--t-head); }
  .panel-head button { color: var(--muted); width: 36px; height: 44px; }
  .thread-log { position: relative; overflow-anchor: none; flex: 1; min-width: 0; min-height: 80px; overflow-y: auto; padding-bottom: var(--s3); }
  .thread-list { flex: 1; overflow-y: auto; padding: var(--s2); }
  h3 { margin: 6px; font-size: var(--t-cap); text-transform: uppercase; letter-spacing: .04em; color: var(--faint); }
  h3 small { margin-left: var(--s1); font-weight: 400; }
  .section { display: flex; align-items: center; gap: var(--s1); font-size: var(--t-cap); font-weight: 600; text-transform: uppercase; letter-spacing: .04em; color: var(--faint); padding: 2px 0; transition: color var(--motion-fast) var(--ease); }
  .section small { font-weight: 400; color: var(--faint); transition: color var(--motion-fast) var(--ease); }
  .section .chev { font-size: var(--t-sm); }
  @media (hover: hover) {
    .section:hover, .section:hover small { color: var(--ink); }
  }
  h3:not(:first-child) { margin-top: 14px; }
  .panel-row { display: flex; align-items: center; gap: var(--s1); }
  .panel-row :global(.thread-chip) { flex: 1; }
  .thread-count { flex: none; padding: var(--s1) var(--s2); border-radius: 6px; font-size: var(--t-cap); color: var(--faint); white-space: nowrap; transition: background-color var(--motion-fast) var(--ease), color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .thread-count:hover { background: var(--hover); color: var(--ink); }
  }
  .thread-count:active { background: var(--select); }
  .thread-empty { flex: none; padding: var(--s1) var(--s2); font-size: var(--t-cap); color: var(--faint); white-space: nowrap; }
  .thread-wrap { display: contents; }
  .thread-wrap.overlay { display: block; position: absolute; inset: 0; z-index: 31; }
  .thread-wrap.overlay .panel-head { touch-action: pan-y; }
  .thread-scrim { display: none; }
  @media (max-width: 1099px) {
    .thread-panel { position: absolute; top: 0; bottom: 0; right: 0; z-index: 32; width: min(360px, 90%); box-shadow: -4px 0 24px #0006; }
    .thread-scrim { display: block; position: absolute; inset: 0; background: #0006; z-index: 31; }
  }
  @media (max-width: 760px) {
    .panel-row :global(.thread-chip) { min-height: 44px; }
    .thread-count .when { display: none; }
  }
</style>
