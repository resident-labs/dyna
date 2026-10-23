<script>
  // The floating Voice call widget. It is one overlay above every view: it
  // collapses to Voice's line and the call controls, and expands into this
  // call's conversation. Drag it by its top line.
  import { untrack } from 'svelte'
  import Avatar from '../ui/Avatar.svelte'
  import { api } from '../../api.ts'
  import { voiceCall, callRequests, clock } from './voiceCall.ts'
  import { capturing, spaceTalk, statusLine } from './callState.ts'
  import { callPosts, callRows, sharedItems, unseen } from './conversation.ts'
  import { rootQuestion } from './attention.ts'
  import { ringing } from './callRequests.ts'
  import QuestionCard from './QuestionCard.svelte'
  import CallThread from './CallThread.svelte'
  import CallScreen from './CallScreen.svelte'
  import SharedBanner from './SharedBanner.svelte'
  import { clampWidget, micModes, phoneInitial, phoneLayout, phoneReduce, widgetPlace } from './phone.ts'
  import IncomingToast from './IncomingToast.svelte'
  import Menu from '../ui/Menu.svelte'
  import { icons } from './icons.ts'

  const POS_KEY = 'dyna.voice.widget'

  // narrow: App's phone breakpoint, where the call is a full screen.
  // onopenfile opens a shared file in the app's viewer. rooms name the room
  // of a call request. link names the request a push tap opened.
  let { narrow = false, rooms = [], link = $bindable(null), onopenfile } = $props()

  let s = $derived($voiceCall)
  let active = $derived(['starting', 'connecting', 'live', 'ending'].includes(s.phase))
  let posts = $state([])
  let open = $state(false)
  let closed = $state(true)
  let endedAt = $state(0)
  let now = $state(Date.now())
  let visible = $state(document.visibilityState === 'visible')
  let pos = $state(savedPos())
  let drag = null
  let thread = $state()
  let loadedFor = 0
  let modeMenu = $state(null) // the microphone mode menu, where it opened
  // Root asked to talk now: the oldest request that still rings. A push tap
  // names one request (link); its toast then covers the screen.
  let linkedRing = $derived(link ? $callRequests.find((r) => r.state === 'ringing' && r.room === link.room && r.id === link.call) : undefined)
  let ring = $derived(linkedRing ?? ringing($callRequests))
  let full = $derived(!!linkedRing)
  let question = $state(null) // root's open question in the call's room
  let phoneUI = $state(phoneInitial) // the phone screen: minimized, sheet open
  let layout = $derived(narrow ? phoneLayout(s, phoneUI) : '')

  let shown = $derived(active || s.phase === 'error' || s.phase === 'resumable' || (!closed && !!s.startedAt))

  // On a page load, a call the engine still runs shows at once, to rejoin
  // or end; nothing opens the microphone until Rejoin.
  $effect(() => {
    voiceCall.refresh()
  })
  let rows = $derived(callRows(posts, s.tools, s.room, s.unspoken))
  // The files shared in this call that the owner has not opened or hidden.
  let seen = $state(new Set())
  let shared = $derived(unseen(sharedItems(posts, s.room), seen))
  const see = (share) => (seen = new Set([...seen, share.key]))
  function openShared(share) {
    see(share)
    onopenfile({ ...share.att, postId: share.postId, roomId: s.roomId })
  }
  let length = $derived(s.startedAt ? clock((endedAt || now) - s.startedAt) : '')
  let line = $derived(s.phase === 'idle' && s.startedAt ? `Call ended · ${length}` : statusLine(s))

  // A new call starts a fresh conversation. On a reload mid-call, the
  // call's earlier posts come from the room log.
  $effect(() => {
    const at = s.startedAt
    if (!at || at === loadedFor) return
    loadedFor = at
    posts = []
    seen = new Set()
    closed = false
    endedAt = 0
    api.feedTail(s.roomId, '', 200).then((f) => { if (s.startedAt === at) posts = callPosts([...f.posts, ...posts], at) }).catch(() => {})
  })
  $effect(() => {
    if (s.phase === 'ending' && !endedAt) endedAt = Date.now()
  })
  $effect(() => {
    if (s.phase !== 'live') return
    const t = setInterval(() => (now = Date.now()), 1000)
    return () => clearInterval(t)
  })
  $effect(() => {
    rows.length
    s.partial
    if (thread) thread.scrollTop = thread.scrollHeight
  })

  // Root's question shows while the widget does; the engine has Voice read
  // it aloud during a call.
  let questionRoom = $derived(shown ? s.roomId : '')
  function loadQuestion(id) {
    api.questions(id).then((m) => { if (questionRoom === id) question = rootQuestion(m[id]) }).catch(() => {})
  }
  $effect(() => {
    if (questionRoom) loadQuestion(questionRoom)
    else question = null
  })
  // Between calls the phone screen resets, so the next call starts as the
  // bubble again.
  $effect(() => {
    if (!active) phoneUI = phoneInitial
  })

  // The requests that ring now, then each change by event. The engine learns
  // from the app that it showed a request, once.
  $effect(() => {
    callRequests.load().catch(() => {})
  })
  $effect(() => {
    if (ring && !active && visible) callRequests.shown(ring)
  })

  // Accept starts the normal call; the engine marks the request joined when
  // that call connects. A browser opens the microphone only after this tap.
  export function accept(req = ring) {
    if (!req || req.state !== 'ringing') return
    link = null
    voiceCall.call(req.room, rooms.find((r) => r.id === req.room)?.name ?? req.room)
  }
  export function decline(req = ring) {
    if (!req || req.state !== 'ringing') return
    link = null
    callRequests.decline(req).catch(() => {})
  }
  // A push link may name a request this app has not read yet.
  $effect(() => {
    if (link) callRequests.load(link.room).catch(() => {})
  })

  // event takes the app's live events; App passes each one.
  export function event(ev) {
    callRequests.apply(ev)
    if (ev.kind === 'resync') callRequests.load().catch(() => {})
    if (ev.kind === 'questions' && ev.roomId === questionRoom) loadQuestion(questionRoom)
    if (ev.kind !== 'posts' || ev.roomId !== s.roomId || !s.startedAt) return
    const more = callPosts(ev.posts ?? [], s.startedAt)
    if (more.length) posts = callPosts([...posts, ...more], s.startedAt)
  }

  function savedPos() {
    let raw = null
    try { raw = localStorage.getItem(POS_KEY) } catch { /* private mode */ }
    return widgetPlace(raw, window.innerWidth, window.innerHeight)
  }
  let widget = $state() // the widget's element, measured to keep it whole on screen
  function place(right, bottom) {
    const size = widget ? { width: widget.offsetWidth, height: widget.offsetHeight } : undefined
    pos = clampWidget({ right, bottom }, window.innerWidth, window.innerHeight, size)
  }
  // The widget grows and shrinks as it opens and as rows, banners and
  // question cards come: each size change places it again, whole on screen.
  $effect(() => {
    if (!widget) return
    const seen = new ResizeObserver(() => untrack(() => place(pos.right, pos.bottom)))
    seen.observe(widget)
    return () => seen.disconnect()
  })
  function save() {
    try { localStorage.setItem(POS_KEY, JSON.stringify(pos)) } catch { /* private mode */ }
  }
  function grab(e) {
    if (e.button !== 0 || e.target.closest('button')) return
    drag = { x: e.clientX, y: e.clientY, right: pos.right, bottom: pos.bottom }
    e.currentTarget.setPointerCapture(e.pointerId)
  }
  function move(e) {
    if (drag) place(drag.right - (e.clientX - drag.x), drag.bottom - (e.clientY - drag.y))
  }
  function release() {
    if (!drag) return
    drag = null
    save()
  }
  function nudge(e) {
    const step = e.shiftKey ? 64 : 16
    const d = { ArrowLeft: [step, 0], ArrowRight: [-step, 0], ArrowUp: [0, step], ArrowDown: [0, -step] }[e.key]
    if (!d) return
    e.preventDefault()
    place(pos.right + d[0], pos.bottom + d[1])
    save()
  }

  // Hold Space to talk (see spaceTalk); a blur or a hidden page also ends a
  // hold. A handled Space neither scrolls the page nor presses a focused
  // button: both happen as the key's default.
  function key(e) {
    const on = spaceTalk(e, s)
    if (on === null) return
    e.preventDefault()
    voiceCall.talk(on)
  }

  function close() {
    voiceCall.dismiss()
    closed = true
    open = false
  }
</script>

<svelte:window onkeydown={key} onkeyup={key} onblur={key} onresize={() => place(pos.right, pos.bottom)} />
<svelte:document onvisibilitychange={(e) => { key(e); visible = document.visibilityState === 'visible' }} />

{#if layout && layout !== 'none'}
  <CallScreen {s} {layout} ui={phoneUI} onui={(e) => (phoneUI = phoneReduce(phoneUI, e))} {rows} {question} {length} onclose={close} {shared} onopenshared={openShared} ondismissshared={see} />
{:else if shown && !narrow}
  <section class="widget" class:open aria-label="Voice call" bind:this={widget} style="right: {pos.right}px; bottom: {pos.bottom}px">
    <div class="glass">
      <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
      <div class="whead" class:drag={!!drag} tabindex="0" role="group" aria-label="Voice call. Drag, or use the arrow keys, to move it." onpointerdown={grab} onpointermove={move} onpointerup={release} onpointercancel={release} onkeydown={nudge}>
        <span class="ring" class:speak={s.phase === 'live' && s.activity === 'speaking'} class:wait={s.phase === 'starting' || s.phase === 'connecting'}><Avatar name="Voice" hue={3} size={36} ring="var(--raise)" /></span>
        <div class="wid">
          <strong>Voice{#if active && s.startedAt}<span>{length}</span>{/if}</strong>
          <span class:live={s.phase === 'live' && capturing(s)} class:bad={s.phase === 'error'} role="status">{line}</span>
        </div>
        {#if s.startedAt}
          <button class="cbtn icon" aria-expanded={open} aria-label={open ? 'Hide the conversation' : 'Show the conversation'} title={open ? 'Hide the conversation' : 'Show the conversation'} onclick={() => (open = !open)}>
            <svg class="chev" viewBox="0 0 24 24" width="18" height="18" aria-hidden="true"><path d={icons.chevron} fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" /></svg>
          </button>
        {/if}
        {#if !active && s.phase !== 'resumable'}
          <button class="cbtn icon" aria-label="Close" title="Close" onclick={close}>
            <svg viewBox="0 0 24 24" width="18" height="18" aria-hidden="true"><path d={icons.close} stroke="currentColor" stroke-width="1.8" stroke-linecap="round" /></svg>
          </button>
        {/if}
      </div>

      {#if shared.length}<div class="shared"><SharedBanner roomId={s.roomId} share={shared.at(-1)} onopen={openShared} ondismiss={see} /></div>{/if}
      {#if s.switchTo}
        <div class="ask" role="alertdialog" aria-label="Move the call">
          <p>End the call in {s.room} and call from {s.switchTo.room}?</p>
          <div class="row">
            <button class="btn pri" onclick={() => voiceCall.switchHere(s.switchTo.roomId, s.switchTo.room)}>End and call here</button>
            <button class="btn quiet" onclick={voiceCall.cancelSwitch}>Cancel</button>
          </div>
        </div>
      {/if}
      {#if active}
        <div class="wctl">
          {#if s.pushToTalk}
            <!-- Push to talk: hold the mic button, or Space, to talk. -->
            <button class="cbtn talk" class:on={s.talking} aria-pressed={s.talking} aria-label="Hold to talk" title="Hold to talk, or hold Space" disabled={s.deafened} onpointerdown={(e) => { if (e.button !== 0) return; e.currentTarget.setPointerCapture(e.pointerId); voiceCall.talk(true) }} onpointerup={() => voiceCall.talk(false)} onpointercancel={() => voiceCall.talk(false)}>
              <svg viewBox="0 0 24 24" width="18" height="18" aria-hidden="true"><path d={icons.mic} fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" /></svg>
            </button>
          {:else}
            <button class="cbtn" aria-pressed={s.muted} aria-label={s.muted ? 'Unmute' : 'Mute'} title={s.muted ? 'Unmute' : 'Mute'} onclick={voiceCall.toggleMute}>
              <svg viewBox="0 0 24 24" width="18" height="18" aria-hidden="true"><path d={s.muted ? icons.micOff : icons.mic} fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" /></svg>
            </button>
          {/if}
          <button class="cbtn" aria-pressed={s.deafened} aria-label={s.deafened ? 'Undeafen' : 'Deafen'} title={s.deafened ? 'Undeafen' : 'Deafen: Voice goes silent and your microphone mutes'} onclick={voiceCall.toggleDeafen}>
            <svg viewBox="0 0 24 24" width="18" height="18" aria-hidden="true"><path d={s.deafened ? icons.headOff : icons.head} fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" /></svg>
          </button>
          <button class="cbtn icon" aria-label="Microphone mode" title="Microphone mode" aria-haspopup="menu" onclick={(e) => { const r = e.currentTarget.getBoundingClientRect(); modeMenu = { x: r.left, y: r.bottom + 4, anchor: e.currentTarget } }}>
            <svg viewBox="0 0 24 24" width="18" height="18" aria-hidden="true"><path d={icons.more} fill="currentColor" /></svg>
          </button>
          {#if s.pushToTalk}<span class="hint">or hold Space</span>{/if}
          <span class="spacer"></span>
          <button class="cbtn hang" aria-label="Hang up" title="Hang up" disabled={s.phase === 'ending'} onclick={voiceCall.hangup}>
            <svg viewBox="0 0 24 24" width="18" height="18" aria-hidden="true"><path d={icons.phone} fill="none" stroke="currentColor" stroke-width="1.8" stroke-linejoin="round" transform="rotate(135 12 12)" /></svg>
          </button>
        </div>
      {:else if s.phase === 'resumable'}
        <div class="wctl">
          <button class="btn fill" onclick={() => voiceCall.start(s.roomId, s.room)}>Rejoin</button>
          <button class="btn danger" onclick={voiceCall.hangup}>End</button>
        </div>
      {:else if s.phase === 'error'}
        <div class="wctl">
          {#if s.busyRoom}
            <button class="btn pri" onclick={() => voiceCall.switchHere(s.roomId, s.room)}>End the call in {s.busyRoom} and call here</button>
          {:else}
            <button class="btn" onclick={() => voiceCall.start(s.roomId, s.room)}>Call again</button>
          {/if}
        </div>
      {:else}
        <div class="wctl"><button class="btn" onclick={() => voiceCall.start(s.roomId, s.room)}>Call again</button></div>
      {/if}

      {#if open && s.startedAt}
        <div class="wbody">
          <h2>Conversation <span class="n">read only</span></h2>
          <div class="thread" bind:this={thread}>
            <CallThread {rows} partial={s.partial} onopen={openShared} />
          </div>
        </div>
      {/if}
    </div>
    {#if question}<QuestionCard {question} roomId={s.roomId} live={s.phase === 'live'} />{/if}
  </section>
{/if}
{#if modeMenu}
  <Menu x={modeMenu.x} y={modeMenu.y} title="Microphone" items={micModes(s).map((m) => ({ label: m.label, hint: m.current ? '✓' : '', run: () => voiceCall.setPushToTalk(m.on) }))} onclose={() => { const a = modeMenu?.anchor; modeMenu = null; a?.focus?.() }} />
{/if}
{#if ring && !active}<IncomingToast request={ring} {narrow} {full} onaccept={() => accept()} ondecline={() => decline()} />{/if}

<style>
  .widget {
    position: fixed;
    z-index: 50;
    width: min(300px, calc(100vw - 16px));
    display: flex;
    flex-direction: column;
    gap: var(--s2);
  }
  .widget.open { width: min(360px, calc(100vw - 16px)); }
  .glass {
    background: rgba(var(--glass-ink), var(--glass-panel));
    -webkit-backdrop-filter: blur(14px);
    backdrop-filter: blur(14px);
    border: 1px solid var(--line);
    border-radius: var(--r-sm);
    box-shadow: 0 6px 24px rgba(0, 0, 0, 0.45);
  }
  .whead {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 10px 6px 6px 12px;
    cursor: grab;
    touch-action: none;
    user-select: none;
    border-radius: var(--r-sm) var(--r-sm) 0 0;
  }
  .whead.drag { cursor: grabbing; }
  .whead:focus-visible { outline: 2px solid var(--iris-edge); outline-offset: -2px; }
  .ring { display: inline-grid; border-radius: 50%; transition: box-shadow var(--motion) var(--ease); }
  .ring.speak { box-shadow: 0 0 0 2px var(--raise), 0 0 0 4px var(--gold); animation: live 1.6s ease-in-out infinite; }
  .ring.wait { box-shadow: 0 0 0 2px var(--raise), 0 0 0 4px var(--line); animation: live 1s ease-in-out infinite; }
  @keyframes live { 50% { opacity: 0.6; } }
  .wid { flex: 1; min-width: 0; line-height: var(--leading-tight); }
  .wid strong { display: flex; gap: var(--s2); align-items: baseline; font-size: var(--t-body); font-weight: 600; }
  .wid strong span { font-size: var(--t-sm); font-weight: 400; color: var(--faint); font-variant-numeric: tabular-nums; }
  .wid > span { display: block; font-size: var(--t-sm); color: var(--muted); overflow-wrap: anywhere; }
  .wid > span.live { color: var(--gold); }
  .wid > span.bad { color: var(--coral); }
  .wctl { display: flex; align-items: center; flex-wrap: wrap; gap: var(--s1); padding: 2px 10px 10px 12px; }
  .spacer { flex: 1; }
  .cbtn {
    width: 32px;
    height: 32px;
    border-radius: var(--r-xs);
    display: grid;
    place-items: center;
    color: var(--ink-2);
    background: var(--field);
    transition: background-color var(--motion-fast) var(--ease), color var(--motion-fast) var(--ease);
  }
  .cbtn.icon { background: transparent; color: var(--muted); }
  @media (hover: hover) {
    .cbtn:hover { background: var(--raise-hover); color: var(--ink); }
    .cbtn.hang:hover { background: color-mix(in srgb, var(--coral) 12%, transparent); color: var(--coral); }
  }
  .cbtn:active { background: var(--select); }
  .cbtn.hang:active { background: color-mix(in srgb, var(--coral) 20%, transparent); }
  .cbtn[aria-pressed='true'] { color: var(--coral); background: color-mix(in srgb, var(--coral) 16%, transparent); }
  .cbtn.hang { color: var(--coral); background: transparent; box-shadow: inset 0 0 0 1px color-mix(in srgb, var(--coral) 45%, transparent); }
  .cbtn:disabled { opacity: 0.6; cursor: default; }
  .chev { transition: transform var(--motion) var(--ease); }
  .open .chev { transform: rotate(180deg); }
  .cbtn.talk { touch-action: none; user-select: none; }
  .cbtn.talk.on { color: var(--iris); background: var(--iris-wash); box-shadow: inset 0 0 0 1px var(--iris-edge); }
  .hint { font-size: var(--t-sm); color: var(--faint); margin-left: var(--s1); }
  .shared { padding: 0 10px 8px 12px; }
  .ask { padding: 0 12px 10px; font-size: var(--t-sm); color: var(--ink-2); }
  .ask p { margin: 0 0 var(--s2); }
  .ask .row { display: flex; gap: var(--s2); flex-wrap: wrap; }
  .wbody { border-top: 1px solid var(--line-soft); padding-bottom: var(--s1); }
  .wbody h2 { margin: 10px var(--s3) 4px; font-size: var(--t-sm); font-weight: 600; color: var(--muted); display: flex; gap: 6px; }
  .wbody h2 .n { color: var(--faint); font-weight: 500; }
  .thread { overflow: auto; max-height: min(320px, 45vh); padding: 0 0 var(--s2); }
  @media (prefers-reduced-motion: reduce) {
    .ring.speak, .ring.wait { animation: none; }
    .chev { transition: none; }
  }
</style>
