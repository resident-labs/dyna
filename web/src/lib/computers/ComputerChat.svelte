<script lang="ts">
  // The floating chat on the Computer tab. It reads the same session data and
  // sends through the same path as the Messages tab, so a message shows in
  // both places. Tucked: one pill. Composer: the pill widens into an input.
  // Chat: the recent thread grows upward. The rules live in chatState.ts.
  import { tick, untrack, onMount, onDestroy } from 'svelte'
  import DmRows from '../sessions/DmRows.svelte'
  import FloatWindow from '../ui/FloatWindow.svelte'
  import { clampRect, type Area, type Rect } from '../sessions/termDock.ts'
  import ComposerBox from '../ui/ComposerBox.svelte'
  import { dmView } from '../sessions/dm.ts'
  import { showReasoning, showCompaction } from '../ui/prefs.ts'
  import { ago } from '../ui/format.ts'
  import { errorText } from '../errorText.ts'
  import { computerLine, type ComputerView } from './summary.ts'
  import { clock } from './clock.svelte.ts'
  import { attachmentDraft } from '../files/attachmentDraft.ts'
  import { TUCKED, COMPOSER, CHAT, RECENT, chatClick, chatKey, chatDriving, lastLine, type ChatShape, type ChatWhere } from './chatState.ts'
  import type { AttachmentRef } from '../api/engine.ts'
  import type { Attachment, Member, PostResult, Room, SendResult } from '../api/room.ts'
  import type { Session } from '../api/session.ts'

  interface Props {
    // session: the transcript the app already holds. onsend sends to the
    // session, as the Messages tab does. driving: the owner has control.
    session?: Session | null
    room: Room
    members?: Member[]
    name?: string
    c?: ComputerView | null
    computerCount?: number
    driving?: boolean
    onsend: (text: string, atts: AttachmentRef[]) => Promise<PostResult | SendResult | undefined>
    onopenfile: (attachment: Attachment) => void
    onpost?: ((postId: string) => void) | null
    onmessages?: (() => void) | null
    draft?: string
    ondraft?: ((draft: string) => void) | null
    onpending?: ((pending: boolean) => void) | null
  }
  let { session = null, room, members = [], name = '', c = null, computerCount = 1, driving = false, onsend, onopenfile, onpost = null, onmessages = null, draft: saved = '', ondraft = null, onpending = null }: Props = $props()

  // shape is the chat's current form; the rules that move it are in chatState.ts.
  let shape = $state<ChatShape>(TUCKED)
  let dockEl = $state<HTMLDivElement | undefined>()
  let field = $state<HTMLElement | undefined>()
  let scroller = $state<HTMLDivElement | undefined>()
  let draft = $state(saved)
  $effect(() => { ondraft?.(draft) })
  let busy = $state(false)
  let sendError = $state('')
  const attachments = attachmentDraft({ room: () => room?.id, error: (m: string) => (sendError = m), focus: () => field?.focus(), disabled: () => !!m?.closed, noun: 'message' })
  $effect(() => { onpending?.(busy || $attachments.items.length > 0) })
  onDestroy(() => { attachments.destroy(); onpending?.(false) })

  const m = $derived(session?.member)
  const dm = $derived(session ? dmView(session, room, { reasoning: $showReasoning, compaction: $showCompaction }, [], members.filter((child) => !!m?.id && child.parent === m?.id)) : null)
  const last = $derived(lastLine(dm?.rows, dm?.address ?? ''))
  // The overlay shows the newest rows; the Messages tab has the rest.
  const shown = $derived(dm ? { ...dm, rows: dm.rows.slice(-RECENT) } : null)
  // Intent comes from computer state; window metadata is the fallback.
  const line = $derived(computerLine({ c }))
  const age = $derived.by(() => {
    // Refresh the app's shared relative time on each clock tick.
    clock.now
    return c?.latestStatus?.text?.trim() && Number.isFinite(Date.parse(c.latestStatus.at ?? '')) ? ago(c.latestStatus.at ?? '') : ''
  })
  const who = $derived(m?.name || name || 'the agent')
  const failed = $derived($attachments.failed)
  const canSend = $derived(!busy && !$attachments.uploading && !failed && !m?.closed && (draft.trim() !== '' || $attachments.ready.length > 0))

  // The dot shows when the agent did or said something since the chat was last open.
  let seen = $state<string | null>(null)
  $effect(() => {
    if (!session) return
    if (seen === null || shape !== TUCKED) seen = last?.key ?? ''
  })
  const unread = $derived(seen !== null && !!last && last.key !== seen)

  // Taking control tucks the chat, so nothing sits on the stream.
  $effect(() => { const d = driving; untrack(() => { shape = chatDriving(shape, d) }) })

  // Leaving tucked puts the cursor in the input.
  $effect(() => {
    if (shape !== TUCKED) tick().then(() => field?.focus())
  })
  // Follow the newest message.
  $effect(() => {
    shown?.rows.length; last?.key
    if (shape === CHAT) tick().then(() => scroller && (scroller.scrollTop = scroller.scrollHeight))
  })

  // The open chat is a floating window over the stream's frame; a phone keeps the bottom sheet.
  let phone = $state(false)
  let area = $state<Area>({ w: 0, h: 0 })
  let box = $state<Rect | null>(null)
  // The first place is the old one: bottom centre, 420 px wide.
  const placeChat = (a: Area): Rect => {
    const w = 420, h = Math.min(420, Math.floor(a.h * 0.62))
    return { w, h, x: Math.floor((a.w - w) / 2), y: a.h - h - 8 }
  }
  const winRect = $derived(clampRect(box ?? placeChat(area), area))
  const moveWin = (x: number, y: number, a: Area) => { box = clampRect({ ...winRect, x, y }, a) }
  const sizeWin = (w: number, h: number, a: Area) => { box = clampRect({ ...winRect, w, h }, a) }

  const go = (where: ChatWhere) => { shape = chatClick(shape, where) }
  // Stop the stream from taking a click on the chat, and tuck on a click on the stream.
  export function streamClick() { go('stream') }

  function onkey(e: KeyboardEvent) {
    const t = e.target instanceof Element ? e.target : null
    // A preview or other modal owns its keys, even when portaled outside the dock.
    if (t?.closest('[aria-modal="true"]')) return
    const r = chatKey(shape, {
      key: e.key, ctrl: e.ctrlKey, meta: e.metaKey, alt: e.altKey,
      editable: !!t?.closest('input, textarea, select, [contenteditable]'),
      inside: !!t && !!dockEl?.contains(t),
    }, driving)
    if (!r.handled) return
    e.preventDefault()
    e.stopImmediatePropagation()
    shape = r.state
  }
  onMount(() => {
    // Capture: the computer's key handler would otherwise take Ctrl+/ first.
    addEventListener('keydown', onkey, true)
    const narrow = matchMedia('(max-width: 760px)')
    const sync = () => { phone = narrow.matches }
    sync()
    narrow.addEventListener('change', sync)
    return () => { removeEventListener('keydown', onkey, true); narrow.removeEventListener('change', sync) }
  })

  async function send() {
    if (!canSend) return
    busy = true
    sendError = ''
    try {
      const res = await onsend(draft, $attachments.atts)
      // A post carries a message; a spawn or a refused send does not.
      const post = res && 'message' in res ? res : null
      if (post?.failed?.length) sendError = post.failed.map((f) => f.error || 'delivery failed').join(' · ')
      if (post?.message?.id) {
        attachments.clear()
        draft = ''
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
  export function addFiles(files: ArrayLike<File> | null | undefined) { attachments.addFiles(files) }
  const placeholder = $derived(m?.closed ? `${who} is closed` : `Message @${who}`)
</script>

{#snippet chatBody()}
  <p class="intent">{line}</p>
  <div class="thread" bind:this={scroller}>
    {#if shown && m}<DmRows compact dm={shown} {room} member={m} {members} onthread={() => onmessages?.()} {onopenfile} {onpost} />{/if}
    {#if !shown?.rows.length}<p class="empty">Nothing said yet.</p>{/if}
  </div>
  <div class="input">
    <ComposerBox model={m?.profile || m?.model || ''} mode="Direct message" skin="glass" {attachments} bind:draft bind:field {placeholder} {busy} {canSend} bad={!!sendError} closed={!!m?.closed} oninput={() => { sendError = '' }} onkeydown={keydown} onsend={send} />
  </div>
  {#if sendError}<p class="err" role="alert">{sendError}</p>{/if}
{/snippet}

<div class="root" data-chat data-state={shape} bind:this={dockEl}>
{#if shape === CHAT && !phone}
  <!-- On a desktop the open chat is the shared floating window: it moves, resizes and scrolls like a terminal window. -->
  <div class="layer" bind:clientWidth={area.w} bind:clientHeight={area.h}>
    <FloatWindow rect={winRect} {area} label={`Chat with ${who}`} z={1} onmove={moveWin} onsize={sizeWin}>
      {#snippet title()}
        <span class="status-dot" aria-hidden="true"></span><span class="tl"><b>{who}</b>{#if age}<span class="age"> {age}</span>{/if}</span>
      {/snippet}
      {#snippet buttons()}
        <button class="ib" onclick={() => go('header')} aria-label="Tuck the chat" title="Tuck (Esc)">⌄</button>
      {/snippet}
      {@render chatBody()}
    </FloatWindow>
  </div>
{:else}
<div class="dock">
  {#if shape === TUCKED}
    <button class="pill glass pop" onclick={() => go('pill')} title={line} aria-label={`Open chat: ${line}`}>
      <span class="status-dot" class:unread aria-hidden="true"></span>
      {#if computerCount > 1}<span class="who">{who}</span>{/if}
      <span class="last">{line}</span>
      {#if age}<span class="age">· {age}</span>{/if}
    </button>
    <button class="phone-handle glass" onclick={() => go('pill')} title={line} aria-label="Open computer chat" aria-expanded="false">
      <span class="status-dot" aria-hidden="true"></span>
      {#if computerCount > 1}<span class="who">{who}</span>{/if}
      <span class="last">{line}</span>
      {#if age}<span class="age">· {age}</span>{/if}<span class="grip" aria-hidden="true"></span>
    </button>
  {:else if shape === COMPOSER}
    <div class="composer glass pop">
      <button class="lastline" onclick={() => go('lastline')} title="Open the chat (Ctrl+/)">
        <span class="t" title={line}>{line}</span>
        {#if age}<span class="age">{age}</span>{/if}
        <span class="open">Open chat ↗</span>
      </button>
      <div class="input">
        <ComposerBox model={m?.profile || m?.model || ''} mode="Direct message" skin="glass" {attachments} bind:draft bind:field {placeholder} {busy} {canSend} bad={!!sendError} closed={!!m?.closed} oninput={() => { sendError = '' }} onkeydown={keydown} onsend={send} />
      </div>
      {#if sendError}<p class="err" role="alert">{sendError}</p>{/if}
    </div>
  {:else}
    <div class="chat glass pop" role="dialog" aria-label={`Chat with ${who}`}>
      <header>
        <span class="status-dot" aria-hidden="true"></span><b>{who}</b>{#if age}<span class="age">{age}</span>{/if}
        <button class="x" onclick={() => go('header')} aria-label="Tuck the chat" title="Tuck (Esc)">⌄</button>
      </header>
      {@render chatBody()}
    </div>
  {/if}
</div>
{/if}
</div>

<style>
  /* The tucked pill starts below the stream. Open chat grows upward. */
  .root { display: contents; }
  /* The window's area is the stream's frame; only the window takes pointer input. */
  .layer { position: absolute; inset: 0; pointer-events: none; z-index: 5; }
  .layer :global(.win) { position: absolute; }
  .layer .age { margin-left: 4px; }
  .dock { position: absolute; left: 50%; bottom: -42px; transform: translateX(-50%); z-index: 5; display: flex; flex-direction: column; align-items: center; max-width: calc(100% - 16px); }
  .glass { background: rgba(var(--glass-ink), var(--glass)); backdrop-filter: blur(18px) saturate(1.4); -webkit-backdrop-filter: blur(18px) saturate(1.4); border: 1px solid var(--hair-strong); box-shadow: 0 18px 50px rgba(0, 0, 0, 0.5), inset 0 1px 0 rgba(255, 255, 255, 0.05); }
  .pop { animation: pop var(--motion-slow) var(--ease); }
  @keyframes pop { from { opacity: 0; transform: scale(0.96) translateY(6px); } }
  .who { font-weight: 600; color: var(--iris); font-size: 12.5px; flex: none; }

  .pill { --glass: var(--glass-pill); display: flex; align-items: center; gap: 8px; height: 32px; padding: 0 12px; border-radius: 999px; max-width: min(560px, 100%); cursor: pointer; }
  .pill .last, .phone-handle .last { font-size: 13px; color: var(--ink-2); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; min-width: 0; }
  .age { color: var(--muted); font-size: 11px; white-space: nowrap; flex: none; }
  .status-dot { width: 6px; height: 6px; border-radius: 50%; background: var(--sage); flex: none; }
  .status-dot.unread { box-shadow: 0 0 0 3px rgba(183, 168, 255, 0.18); }
  .phone-handle { display: none; }
  .intent { margin: 0; padding: 10px 14px; color: var(--ink-2); font-size: 13px; overflow-wrap: anywhere; border-bottom: 1px solid var(--hair); }

  .composer { --glass: var(--glass-composer); width: min(520px, 100%); border-radius: var(--r-lg); padding: 6px; display: flex; flex-direction: column; gap: 4px; }
  .lastline { display: flex; gap: 8px; align-items: baseline; padding: 4px 10px 2px; border-radius: var(--r-sm); text-align: left; transition: background-color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .lastline:hover { background: var(--raise-hover); }
  }
  .lastline:active { background: var(--select); }
  .lastline .who { font-size: 12px; }
  .lastline .t { font-size: 13px; color: var(--ink-2); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; flex: 1; min-width: 0; }
  .lastline .open { font-size: 11px; color: var(--faint); flex: none; }

  .chat { --glass: var(--glass-panel); width: 420px; max-width: 100%; height: min(420px, 62vh); border-radius: var(--r-lg); display: flex; flex-direction: column; overflow: hidden; }
  .chat header { display: flex; align-items: center; gap: 8px; padding: 11px 14px; border-bottom: 1px solid var(--hair); font-size: 12px; color: var(--muted); }
  .chat header b { color: var(--ink); font-weight: 600; }
  .chat header .x { margin-left: auto; color: var(--faint); font-size: 14px; padding: 0 4px; transition: color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .chat header .x:hover { color: var(--ink); }
  }
  .thread { flex: 1; min-height: 0; overflow-y: auto; overscroll-behavior: contain; padding: 4px 14px 12px; display: flex; flex-direction: column; }
  .thread > :global(:first-child) { margin-top: auto; }
  .empty { color: var(--faint); text-align: center; font-size: 12.5px; }
  .chat .input, .layer .input { margin: 0 8px 8px; }

  .err { margin: 0 10px 6px; font-size: 12px; color: var(--coral); }
  @media (max-width: 760px) {
    .dock { position: fixed; left: 0; right: 0; bottom: 0; transform: none; max-width: none; z-index: 30; }
    .pill { display: none; }
    .phone-handle { --glass: var(--glass-pill); display: flex; align-items: center; gap: 8px; max-width: calc(100% - 16px); padding: 8px 12px calc(8px + env(safe-area-inset-bottom)); border-radius: 15px 15px 0 0; cursor: pointer; }
    /* A status of up to 80 characters wraps instead of hiding its end. */
    .phone-handle .last { white-space: normal; display: -webkit-box; -webkit-line-clamp: 3; line-clamp: 3; -webkit-box-orient: vertical; text-align: left; }
    .grip { flex: none; width: 30px; height: 3px; border-radius: 3px; background: var(--hair-strong); }
    .chat, .composer { width: 100%; max-width: 100%; border-radius: 18px 18px 0 0; padding-bottom: env(safe-area-inset-bottom); }
    .chat { height: min(440px, 72dvh); }
    .composer { padding-top: 10px; }
  }
</style>
