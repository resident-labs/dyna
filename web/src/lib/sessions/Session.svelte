<script lang="ts">
  import { isFinished } from '../rooms/memberOrder.ts'
  import { sessionStatus } from './status.ts'
  import { tick, onDestroy, onMount } from 'svelte'
  import { dialog } from '../ui/dialog.js'
  import { threadHeaderSwipe } from './threadHeaderSwipe.ts'
  import Avatar from '../ui/Avatar.svelte'
  import EmptyState from '../ui/EmptyState.svelte'
  import Skeleton from '../ui/Skeleton.svelte'
  import Confirm from '../ui/Confirm.svelte'
  import HistoryList from '../rooms/HistoryList.svelte'
  import DmMessage from './DmMessage.svelte'
  import { createSelection } from '../ui/selection.svelte.ts'
  import DmRows from './DmRows.svelte'
  import { dmView, groupMessages, threadList } from './dm.ts'
  import { api } from '../../api.js'
  import { statusMeta } from '../prs/pr.ts'
  import { short, ago } from '../ui/format.ts'
  import MessageInput from '../ui/MessageInput.svelte'
  import { unreadableOutput, unreadableNotice } from '../settings/support.ts'
  import { attachmentDraft } from '../files/attachmentDraft.ts'
  import { showReasoning, showCompaction } from '../ui/prefs.ts'
  import { fade, fly } from 'svelte/transition'
  import { cubicOut } from 'svelte/easing'
  import { arrivals, motion, BASE } from '../ui/motion.ts'
  import { errorText } from '../errorText.ts'
  import type { Attachment, Member, PostResult, Room } from '../api/room.ts'
  import type { QueueItem, Session as SessionData } from '../api/session.ts'
  import type { ShellJob } from '../api/terminal.ts'
  import type { ProfileView } from '../settings/support.ts'
  import type { Row } from '../prs/prlist.ts'
  import type { DmRow, MessageRow, Receipt, Thread, ThreadPost } from './dm.ts'

  interface Props {
    readOnly?: boolean
    onreopen?: ((member: Member) => Promise<void>) | null
    reopening?: string
    ondismisssession?: ((member: Member) => Promise<void>) | null
    dismissing?: string
    session?: SessionData | null
    error?: string
    room: Room
    onharness?: ((name?: string) => void) | null
    onsend: (text: string, attachments: { id: string; name: string }[], forwardId: string) => Promise<PostResult | undefined>
    queue?: QueueItem[]
    onunqueue?: ((pending: Receipt) => Promise<void>) | null
    ondismiss?: ((sessionId: string, postId: string) => Promise<void>) | null
    focusedTurn?: number
    onrecent?: (() => void) | null
    onearlier?: ((near?: () => boolean) => Promise<boolean>) | null
    onnewer?: ((near?: () => boolean) => Promise<boolean>) | null
    onpresent?: (() => Promise<boolean>) | null
    members?: Member[]
    prs?: Row[]
    jobs?: ShellJob[]
    contextError?: string
    onopenfile: (attachment: Attachment) => void
    onpost?: ((postId: string) => void) | null
    onissue?: ((number: number) => void) | null
    onchild: (member: Member) => void
    onpr: (pr: Row) => void
    onjob: (job: ShellJob) => void
    threadsOpen?: boolean
    contextOpen?: boolean
    selectedThread?: string
    draft?: string
    readingHistory?: boolean
  }
  let { readOnly = false, onreopen = null, reopening = '', ondismisssession = null, dismissing = '', session = null, error = '', room, onharness = null, onsend, queue = [], onunqueue = null, ondismiss = null, focusedTurn = 0, onrecent = null, onearlier = null, onnewer = null, onpresent = null, members = [], prs = [], jobs = [], contextError = '', onopenfile, onpost = null, onissue = null, onchild, onpr, onjob, threadsOpen = $bindable(false), contextOpen = $bindable(false), selectedThread = $bindable(''), draft = $bindable(''), readingHistory = $bindable(false) }: Props = $props()
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
  let list = $derived(threadList(dm.threads, children))
  let shellsOpen = $state(false)
  let allRecent = $state(false)
  let threadRows = $derived(groupMessages((selected?.posts || []).map((p): MessageRow => ({ kind: 'message', key: p.id, author: p.from, ts: p.ts, text: p.text, post: p, delivery: p.delivery, pending: p.pending }))))
  let isNewInThread = $derived(threadArrived(selected?.key, threadRows.map((r) => r.key)))
  let ownJobs = $derived(jobs.filter((j) => j.owner === m?.id || j.owner === dm.address))
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
  let field = $state<{ focus(): void }>()
  let busy = $state(false)
  let sendError = $state('')
  let stuck = $state(true)
  const attachments = attachmentDraft({ room: () => room?.id, error: (message) => (sendError = message), focus: () => field?.focus(), disabled: () => !!m?.closed, noun: 'message' })
  let failed = $derived($attachments.failed)
  let canSend = $derived(!busy && !$attachments.uploading && !failed && !m?.closed && (draft.trim() !== '' || $attachments.ready.length > 0 || !!forwarding))
  export function addFiles(files: ArrayLike<File>) { attachments.addFiles(files) }
  onDestroy(() => attachments.destroy())

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

  async function send() {
    if (!canSend) return
    busy = true
    sendError = ''
    try {
      const res = await onsend(draft, $attachments.atts, forwarding?.id ?? '')
      if (res?.failed?.length) sendError = res.failed.map((f) => f.error || 'delivery failed').join(' · ')
      if (res?.message?.id) {
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
        <p class="working"><span class="dot" aria-hidden="true"></span>{m.name} is {sessionStatus(m)}</p>
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
      <MessageInput {room} {forwarding} onremoveforward={() => (forwarding = null)} {onopenfile} {attachments} bind:draft bind:field placeholder={m.closed ? `${m.name} is closed` : `Message @${m.name}`} {busy} {canSend} bad={!!sendError} closed={!!m.closed} oninput={() => { sendError = '' }} onkeydown={keydown} onsend={send} />
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
            {#snippet children(row: MessageRow)}<DmMessage {row} {room} onforward={readOnly ? null : forward} member={m} address={dm.address} {members} {onopenfile} {onpost} {removing} ondismiss={readOnly ? null : askDismiss} inThread selection={threadSelection} enter={isNewInThread(row.key)} />{/snippet}
            </HistoryList>
            {/key}
          </div>
        {:else}
          <header class="panel-head" use:threadHeaderSwipe={{ enabled: threadOverlay, back: backFromThread }}><strong>Threads<small>{m.name} · {dm.threads.length} thread{dm.threads.length === 1 ? '' : 's'}</small></strong><button onclick={() => (threadsOpen = false)} aria-label="Close threads">✕</button></header>
          <div class="thread-list">
            {#each allRecent ? list.recent : list.recent.slice(0, 3) as thread (thread.key)}{@render threadRow(thread)}{/each}
            {#if list.recent.length > 3}<button class="context-row more" onclick={() => (allRecent = !allRecent)}>{allRecent ? 'Show fewer' : `Show ${list.recent.length - 3} more`}</button>{/if}
            {#if !dm.threads.length && !children.length}<EmptyState title="No threads yet">Posts between agents appear here.</EmptyState>{/if}
            {#if children.length}
              <h3>Children</h3>
              {#each list.children as { member: child, thread } (child.id)}
                <div class="child-row">
                  <button class="context-row" title={`Open ${child.name}'s messages`} onclick={() => onchild?.(child)}><Avatar name={child.name} emoji={child.emoji} hue={child.hue} size={24} state={child.state} status={child.status} ring="var(--side)" /><span>{child.name}</span><small>{sessionStatus(child)}</small></button>
                  {#if thread}<button class="thread-count" title={`Posts between ${m.name} and ${child.name}`} onclick={() => openThread(thread.key)}>{thread.posts.length} · {ago(thread.posts.at(-1)?.ts)} ›</button>{/if}
                </div>
              {/each}
            {/if}
            {#if list.shells.length}
              <button class="context-row shells" aria-expanded={shellsOpen} onclick={() => (shellsOpen = !shellsOpen)}><span class="symbol">{shellsOpen ? '▾' : '▸'}</span><span>Shell jobs</span><small>{list.shells.length} · {ago(list.shells[0]?.posts.at(-1)?.ts)}</small></button>
              {#if shellsOpen}{#each list.shells as thread (thread.key)}{@render threadRow(thread)}{/each}{/if}
            {/if}
          </div>
        {/if}
      </aside>
      </div>
    {/if}
    {#if contextOpen}
      <button class="context-scrim" aria-label="Close context" onclick={() => (contextOpen = false)}></button>
      <div class="context-pop" role="dialog" aria-label={`Around ${m.name}`}>
        {#if contextError}<p class="err">{contextError}</p>{/if}
        {#if prs.length}<h3>Pull requests</h3>{/if}
        {#each prs as pr (pr.key)}<button class="context-row" onclick={() => { contextOpen = false; onpr?.(pr) }}><span class="symbol">⑂</span><span>#{pr.number} · {pr.title || pr.repo}</span><small>{statusMeta(pr).label}</small></button>{/each}
        {#if ownJobs.length}<h3>Shell jobs</h3>{/if}
        {#each ownJobs as job (job.id)}<button class="context-row" onclick={() => { contextOpen = false; onjob?.(job) }}><span class="symbol">▶</span><span>{job.name || job.id}</span><small>{job.state}</small></button>{/each}
        {#if !prs.length && !ownJobs.length}<EmptyState title="No tracked PRs or shell jobs" />{/if}
      </div>
    {/if}
  {/if}
</section>

{#snippet threadRow(thread: Thread)}
  {@const person = members.find((p) => p.address === thread.key)}
  <button class="context-row" onclick={() => openThread(thread.key)}><Avatar name={short(thread.key, room.name)} emoji={person?.emoji} hue={person?.hue} size={24} /><span>{short(thread.key, room.name)}</span><small>{thread.posts.length} · {ago(thread.posts.at(-1)?.ts)}</small></button>
{/snippet}

{#if dismissConfirm}
  <Confirm title="Dismiss pending message?" lines={[`Message ${dismissConfirm.post.id} from ${dismissConfirm.post.from}.`, 'This session will not receive the message. History stays available. Active delivery cannot be recalled.']} action="Dismiss message" busy={!!removing} error={queueError} onconfirm={dismissPending} oncancel={() => { dismissConfirm = null; queueError = '' }} />
{/if}

<style>
  .session { flex: 1; display: flex; min-height: 0; min-width: 0; position: relative; }
  .conversation { flex: 1; display: flex; flex-direction: column; min-height: 0; min-width: 0; }
  .err { color: var(--muted); padding: var(--s4) var(--s5); }
  .err { color: var(--coral); }

  .log { position: relative; overflow-anchor: none; flex: 1; overflow-y: auto; padding: var(--s2) 0 var(--s4); }
  .older { margin: var(--s2) 0 0; font-size: var(--t-sm); color: var(--faint); text-align: center; }
  .older button { color: var(--ink-2); text-decoration: underline; padding: var(--s2); }
  .notice { color: var(--gold); padding: var(--s2) var(--s4); margin: 0; font-size: var(--t-ui); border-bottom: 1px solid var(--line-soft); }
  .notice button { color: inherit; text-decoration: underline; font: inherit; }
  .working { display: flex; align-items: center; gap: var(--s2); margin: 14px var(--s4) 0 56px; font-size: var(--t-ui); color: var(--gold); }
  .dot { width: 8px; height: 8px; border-radius: 50%; background: var(--gold); animation: pulse 1.6s ease-in-out infinite; }
  @keyframes pulse { 50% { opacity: 0.35; } }

  .composer { padding: 0 var(--s4) 6px; flex: none; }
  .foot { min-height: 22px; padding: 3px var(--s1) 0; font-size: var(--t-sm); color: var(--faint); }
  .err-t { color: var(--coral); }
  kbd { font-family: var(--sans); font-size: var(--t-cap); font-weight: 600; color: var(--muted); }
  .thread-panel { width: 360px; flex: none; background: var(--side); border-left: 1px solid var(--line-soft); display: flex; flex-direction: column; min-height: 0; }
  .panel-head { display: flex; align-items: center; gap: var(--s2); min-height: 52px; padding: 0 14px; border-bottom: 1px solid var(--line-soft); }
  .panel-head strong { flex: 1; overflow-wrap: anywhere; }
  .panel-head small { display: block; font-weight: 400; font-size: var(--t-cap); color: var(--faint); }
  .panel-head .back { width: 24px; font-size: var(--t-head); }
  .panel-head button { color: var(--muted); width: 36px; height: 44px; }
  .thread-log { position: relative; overflow-anchor: none; flex: 1; min-height: 80px; overflow-y: auto; padding-bottom: var(--s3); }
  .thread-list { flex: 1; overflow-y: auto; padding: var(--s2); }
  h3 { margin: 6px; font-size: var(--t-cap); text-transform: uppercase; letter-spacing: .04em; color: var(--faint); }
  .context-row { display: flex; align-items: center; gap: var(--s2); padding: 5px 6px; width: 100%; border-radius: 6px; text-align: left; color: var(--muted); font-size: var(--t-ui); transition: background-color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .context-row:hover { background: var(--hover); }
  }
  .context-row:active { background: var(--select); }
  .context-row > span:not(.symbol) { flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .context-row small { color: var(--faint); font-size: var(--t-cap); }
  .context-row.more { color: var(--faint); font-size: var(--t-sm); padding-left: 38px; }
  h3:not(:first-child), .shells { margin-top: 14px; }
  .child-row { display: flex; align-items: center; gap: var(--s1); }
  .thread-count { flex: none; padding: var(--s1) var(--s2); border-radius: 6px; font-size: var(--t-cap); color: var(--faint); white-space: nowrap; transition: background-color var(--motion-fast) var(--ease), color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .thread-count:hover { background: var(--hover); color: var(--ink); }
  }
  .thread-count:active { background: var(--select); }
  .symbol { width: 24px; flex: none; text-align: center; }
  .thread-wrap { display: contents; }
  .thread-wrap.overlay { display: block; position: absolute; inset: 0; z-index: 31; }
  .thread-wrap.overlay .panel-head { touch-action: pan-y; }
  .thread-scrim { display: none; }
  .context-scrim { position: absolute; inset: 0; z-index: 33; background: transparent; }
  .context-pop { position: absolute; top: 8px; right: 12px; z-index: 34; width: min(320px, 90%); max-height: 70%; overflow-y: auto; padding: 6px; background: var(--side); border: 1px solid var(--line-soft); border-radius: 8px; box-shadow: 0 8px 24px #0008; }
  @media (max-width: 1099px) {
    .thread-panel { position: absolute; top: 0; bottom: 0; right: 0; z-index: 32; width: min(360px, 90%); box-shadow: -4px 0 24px #0006; }
    .thread-scrim { display: block; position: absolute; inset: 0; background: #0006; z-index: 31; }
  }
  @media (max-width: 760px) {
    .context-row { min-height: 44px; }
  }
</style>
