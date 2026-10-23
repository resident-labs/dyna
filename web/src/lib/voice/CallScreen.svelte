<script lang="ts">
  // The Voice call on a phone (design option A): the orb first, the call's
  // conversation in a bottom sheet, and a bubble while minimized. It shows the
  // same call state as the floating widget, with touch-sized controls.
  import { voiceCall } from './voiceCall.ts'
  import { micControl, micModes, orb } from './phone.ts'
  import VoiceBubble from './VoiceBubble.svelte'
  import Menu from '../ui/Menu.svelte'
  import { DROPPED } from './callState.ts'
  import CallThread from './CallThread.svelte'
  import SharedBanner from './SharedBanner.svelte'
  import { NAMES, shareLine } from './conversation.ts'
  import QuestionCard from './QuestionCard.svelte'
  import { icons } from './icons.ts'
  import { dialog } from '../ui/dialog.ts'
  import type { CallState } from './callState.ts'
  import type { CallRow, SharedFile, SharedItem } from './conversation.ts'
  import type { Layout, PhoneEvent, PhoneUI } from './phone.ts'
  import type { Attachment, Question } from '../api/room.ts'

  // shared: the unseen shared items, oldest first; the banner shows the newest.
  let { s, layout, ui, onui, rows, question, length, onclose, shared, onopenshared, ondismissshared }: {
    s: CallState
    layout: Layout
    ui: PhoneUI
    onui: (e: PhoneEvent) => void
    rows: CallRow[]
    question: Question | null
    length: string
    onclose: () => void
    shared: SharedItem[]
    onopenshared: (share: SharedItem) => void
    ondismissshared: (share: SharedItem) => void
  } = $props()

  // The mode menu renders inside the call screen: the screen is an opaque
  // modal above the app, and a menu outside it would open behind it.
  let modeMenu = $state<{ x: number; y: number; anchor: HTMLElement } | null>(null)
  let o = $derived(orb(s))
  let mic = $derived(micControl(s))
  let active = $derived(['starting', 'connecting', 'live', 'ending'].includes(s.phase))
  let last = $derived(rows.at(-1))
  let peek = $derived(!last ? 'The conversation appears here.' : last.tool ? `Voice · ${last.tool.label}` : last.share ? shareLine(last.share) : last.ask ? `Voice · started: ${last.post?.text}` : `${last.who ? NAMES[last.who] : ''}${last.tag ? ' · ' + last.tag : ''}: ${last.post?.text}`)
  // newest is the shared file the banner shows.
  let newest = $derived(shared.at(-1))
  // stored is a shared file as a stored Attachment. A shared file may instead
  // be one the engine listed in a post's text; it carries the same id, name,
  // mime and title that SharedBanner and the app's viewer read, and no wider
  // type fits both of them.
  const stored = (att: SharedFile): Attachment => att as Attachment

  // One mic control: tap to mute in open mic, hold to talk in push to talk.
  function micDown(e: PointerEvent) {
    const source = e.currentTarget
    if (!mic.hold || e.button !== 0) return
    if (source instanceof HTMLElement) source.setPointerCapture(e.pointerId)
    voiceCall.talk(true)
  }
  const micUp = () => mic.hold && voiceCall.talk(false)
  const micClick = () => !mic.hold && voiceCall.toggleMute()

  // The open sheet follows the newest row, unless the owner scrolled up to
  // read earlier ones.
  let body = $state<HTMLDivElement>()
  let pinned = true
  const onscroll = () => {
    if (!body) return
    pinned = body.scrollHeight - body.scrollTop - body.clientHeight < 48
  }
  $effect(() => {
    rows.length
    s.partial
    if (body && pinned) body.scrollTop = body.scrollHeight
  })
</script>

{#snippet micIcon(size: number)}
  <svg viewBox="0 0 24 24" width={size} height={size} aria-hidden="true"><path d={s.muted || s.deafened ? icons.micOff : icons.mic} fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" /></svg>
{/snippet}
{#snippet hangIcon(size: number)}
  <svg viewBox="0 0 24 24" width={size} height={size} aria-hidden="true"><path d={icons.phone} fill="none" stroke="currentColor" stroke-width="1.8" stroke-linejoin="round" transform="rotate(135 12 12)" /></svg>
{/snippet}

{#if layout === 'bubble'}
  <!-- A call to rejoin is not connected here: a hold has nothing to talk to. -->
  <VoiceBubble {s} {question} {shared} onexpand={() => onui({ type: 'restore' })} ontalk={s.phase === 'resumable' ? () => {} : voiceCall.hold} />
{:else}
  <!-- Escape minimizes a call, hides a call to rejoin, or closes a dropped one. -->
  <div class="phone" role="dialog" aria-modal="true" aria-label="Voice call" tabindex="-1" use:dialog={{ close: () => (active ? onui({ type: 'minimize' }) : s.phase === 'resumable' ? onui({ type: 'hideResume' }) : onclose()), busy: !!modeMenu }}>
    <header class="top">
      {#if active}
        <button class="round quiet" aria-label="Minimize the call" onclick={() => onui({ type: 'minimize' })}>
          <svg class="down" viewBox="0 0 24 24" width="22" height="22" aria-hidden="true"><path d={icons.chevron} fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round" /></svg>
        </button>
      {:else if s.phase === 'resumable'}
        <!-- Hiding changes only this screen; the call goes on in the engine. -->
        <button class="round quiet" aria-label="Hide the call" onclick={() => onui({ type: 'hideResume' })}>
          <svg class="down" viewBox="0 0 24 24" width="22" height="22" aria-hidden="true"><path d={icons.chevron} fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round" /></svg>
        </button>
      {:else}
        <button class="round quiet" aria-label="Close" onclick={onclose}>
          <svg viewBox="0 0 24 24" width="22" height="22" aria-hidden="true"><path d={icons.close} stroke="currentColor" stroke-width="1.9" stroke-linecap="round" /></svg>
        </button>
      {/if}
      <div class="title"><strong>Voice</strong><span>#{s.room}{#if length && active}{' · '}{length}{/if}</span></div>
      <span class="round-space" aria-hidden="true"></span>
    </header>
    {#if newest}<div class="shared"><SharedBanner roomId={s.roomId} share={{ ...newest, att: stored(newest.att) }} onopen={onopenshared} ondismiss={ondismissshared} /></div>{/if}

    <div class="stage" class:small={ui.sheet || !!question}>
      <div class="orb {o.tone}" aria-hidden="true">
        {#if o.tone === 'listening'}
          <svg viewBox="0 0 24 24" width="40%" height="40%"><path d={icons.mic} fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" /></svg>
        {:else if o.tone === 'muted'}
          <svg viewBox="0 0 24 24" width="40%" height="40%"><path d={s.deafened ? icons.headOff : icons.micOff} fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" /></svg>
        {:else if o.tone === 'error'}
          <svg viewBox="0 0 24 24" width="40%" height="40%"><path d={icons.phone} fill="none" stroke="currentColor" stroke-width="1.6" stroke-linejoin="round" /></svg>
        {/if}
      </div>
      <p class="label {o.tone}" role="status">{o.label}</p>
      {#if s.phase === 'error'}
        {#if s.error !== DROPPED}<p class="why">{s.error}</p>{/if}
      {:else if s.partial}
        <p class="words" aria-live="polite">“…{s.partial}”</p>
      {/if}
    </div>

    {#if question}<div class="card"><QuestionCard {question} roomId={s.roomId} live={s.phase === 'live'} phone /></div>{/if}

    {#if s.switchTo}
      {@const moveTo = s.switchTo}
      <div class="ask" role="alertdialog" aria-label="Move the call">
        <p>End the call in {s.room} and call from {moveTo.room}?</p>
        <div class="row">
          <button class="btn pri lg" onclick={() => voiceCall.switchHere(moveTo.roomId, moveTo.room)}>End and call here</button>
          <button class="btn quiet lg" onclick={voiceCall.cancelSwitch}>Cancel</button>
        </div>
      </div>
    {/if}

    {#if active}
      <div class="controls">
        <div class="ctl">
          <button class="round big2" class:on={s.deafened} aria-pressed={s.deafened} aria-label={s.deafened ? 'Undeafen' : 'Deafen'} onclick={voiceCall.toggleDeafen}>
            <svg viewBox="0 0 24 24" width="24" height="24" aria-hidden="true"><path d={s.deafened ? icons.headOff : icons.head} fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" /></svg>
          </button>
          <span>{s.deafened ? 'Undeafen' : 'Deafen'}</span>
        </div>
        <div class="ctl">
          <button class="round mic" class:on={mic.pressed} class:hold={mic.hold} aria-pressed={mic.pressed} aria-label={mic.label} disabled={mic.hold && s.deafened} onpointerdown={micDown} onpointerup={micUp} onpointercancel={micUp} onclick={micClick}>{@render micIcon(30)}</button>
          <span>{mic.label}</span>
        </div>
        <div class="ctl">
          <button class="round big2 hang" aria-label="Hang up" disabled={s.phase === 'ending'} onclick={voiceCall.hangup}>{@render hangIcon(24)}</button>
          <span>Hang up</span>
        </div>
      </div>
      <button class="mode" aria-haspopup="menu" onclick={(e) => { const r = e.currentTarget.getBoundingClientRect(); modeMenu = { x: r.left, y: r.top - 104, anchor: e.currentTarget } }}>Mic: {s.pushToTalk ? 'push to talk' : 'open'} <svg viewBox="0 0 24 24" width="14" height="14" aria-hidden="true"><path d={icons.chevron} fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" transform="rotate(180 12 12)" /></svg></button>
    {:else if s.phase === 'resumable'}
      <!-- A reload: the call goes on in the engine, without this page. -->
      <div class="again">
        <button class="btn fill lg" onclick={() => voiceCall.start(s.roomId, s.room)}>Rejoin</button>
        <button class="btn danger lg" onclick={voiceCall.hangup}>End</button>
      </div>
    {:else if s.phase === 'error'}
      <div class="again">
        {#if s.busyRoom}
          <button class="btn fill lg" onclick={() => voiceCall.switchHere(s.roomId, s.room)}>End the call in {s.busyRoom} and call here</button>
        {:else}
          <button class="btn fill lg" onclick={() => voiceCall.start(s.roomId, s.room)}>Call again</button>
        {/if}
      </div>
    {/if}

    {#if s.startedAt}
      <div class="sheet" class:open={ui.sheet}>
        <button class="handle" aria-expanded={ui.sheet} aria-label={ui.sheet ? 'Hide the conversation' : 'Show the conversation'} onclick={() => onui({ type: 'toggleSheet' })}>
          <span class="bar" aria-hidden="true"></span>
          {#if !ui.sheet}<span class="peek">{peek}</span>{/if}
        </button>
        {#if ui.sheet}<div class="body" bind:this={body} {onscroll}><CallThread {rows} partial={s.partial} onopen={onopenshared} /></div>{/if}
      </div>
    {/if}
    {#if modeMenu}
      <Menu x={modeMenu.x} y={modeMenu.y} title="Microphone" items={micModes(s).map((m) => ({ label: m.label, hint: m.current ? '✓' : '', run: () => voiceCall.setPushToTalk(m.on) }))} onclose={() => { const a = modeMenu?.anchor; modeMenu = null; a?.focus() }} />
    {/if}
  </div>
{/if}

<style>
  .phone {
    position: fixed;
    inset: 0;
    z-index: 60;
    display: flex;
    flex-direction: column;
    background: radial-gradient(120% 60% at 50% 0%, color-mix(in srgb, var(--iris) 12%, var(--main)), var(--main) 70%);
    padding: env(safe-area-inset-top) env(safe-area-inset-right) 0 env(safe-area-inset-left);
  }
  .top { display: flex; align-items: center; gap: var(--s2); padding: var(--s2) var(--s3); }
  .title { flex: 1; text-align: center; line-height: var(--leading-tight); }
  .title strong { display: block; font-size: var(--t-title); font-weight: 600; }
  .title span { font-size: var(--t-sm); color: var(--muted); font-variant-numeric: tabular-nums; }
  .round-space { width: 44px; flex: none; }
  .down { transform: rotate(180deg); }

  .round {
    flex: none;
    width: 44px;
    height: 44px;
    border-radius: 50%;
    display: grid;
    place-items: center;
    color: var(--ink);
    background: var(--field);
    transition: background-color var(--motion-fast) var(--ease), color var(--motion-fast) var(--ease);
    touch-action: none;
    user-select: none;
    -webkit-user-select: none;
  }
  .round:active { background: var(--select); }
  .round.quiet { background: color-mix(in srgb, var(--iris) 14%, transparent); color: var(--iris); }
  .round.big2 { width: 56px; height: 56px; }
  .round.mic { width: 84px; height: 84px; }
  .round.on { background: var(--coral); color: var(--rail); }
  .round.mic.hold.on { background: var(--iris); color: var(--rail); box-shadow: 0 0 0 6px color-mix(in srgb, var(--iris) 25%, transparent); }
  .round.hang { background: var(--coral); color: var(--rail); }
  .round:disabled { opacity: 0.5; }

  .stage { flex: 1; min-height: 0; display: flex; flex-direction: column; align-items: center; justify-content: center; gap: var(--s3); padding: var(--s3); text-align: center; }
  .orb {
    --c: var(--muted);
    --size: 156px;
    position: relative;
    width: var(--size);
    height: var(--size);
    border-radius: 50%;
    display: grid;
    place-items: center;
    color: var(--rail);
    background: radial-gradient(circle at 35% 30%, color-mix(in srgb, var(--c) 85%, #fff), var(--c) 55%, color-mix(in srgb, var(--c) 60%, #000));
    transition: width var(--motion-slow) var(--ease), height var(--motion-slow) var(--ease);
  }
  .small .orb { --size: 96px; }
  .orb.listening { --c: var(--sage); }
  .orb.thinking { --c: var(--gold); }
  .orb.speaking { --c: var(--iris); }
  .orb.error { --c: var(--coral); }
  .orb::before, .orb::after { content: ''; position: absolute; inset: -18px; border-radius: 50%; border: 1px solid color-mix(in srgb, var(--c) 45%, transparent); opacity: 0; }
  .orb.listening::before { opacity: 1; border-style: dashed; animation: breathe 2s ease-in-out infinite; }
  .orb.speaking::before { opacity: 1; animation: pulse 1.8s ease-out infinite; }
  .orb.thinking::before { opacity: 1; inset: -10px; border: 3px solid transparent; border-top-color: var(--gold); border-right-color: var(--gold); animation: spin 1.1s linear infinite; }
  @keyframes pulse { from { transform: scale(0.92); opacity: 0.9; } to { transform: scale(1.18); opacity: 0; } }
  @keyframes breathe { 50% { transform: scale(1.06); } }
  @keyframes spin { to { transform: rotate(360deg); } }
  .label { margin: 0; font-size: var(--t-display); font-weight: 600; color: var(--ink); }
  .label.listening { color: var(--sage); }
  .label.thinking { color: var(--gold); }
  .label.speaking { color: var(--iris); }
  .label.error { color: var(--coral); }
  .words, .why { margin: 0; max-width: 32ch; font-size: var(--t-lead); color: var(--ink-2); overflow-wrap: anywhere; }
  .why { color: var(--muted); }

  .card { padding: 0 var(--s3) var(--s3); }
  .shared { padding: 0 var(--s3); }
  .ask { padding: 0 var(--s4) var(--s3); color: var(--ink-2); font-size: var(--t-body); }
  .ask p { margin: 0 0 var(--s2); }
  .ask .row { display: flex; gap: var(--s2); flex-wrap: wrap; }
  .ask .btn { min-height: 44px; }

  .controls { display: flex; justify-content: center; align-items: flex-start; gap: var(--s5); padding: var(--s3) var(--s4) 0; }
  .ctl { display: flex; flex-direction: column; align-items: center; gap: 6px; font-size: var(--t-sm); color: var(--ink-2); }
  .ctl:not(:nth-child(2)) { padding-top: 14px; }
  .mode { align-self: center; min-height: 44px; padding: 0 var(--s3); font-size: var(--t-ui); color: var(--muted); display: inline-flex; align-items: center; gap: 4px; }
  .again { display: flex; justify-content: center; gap: var(--s3); padding: var(--s3) var(--s4); }
  .again .btn { min-height: 48px; }

  .sheet {
    margin-top: var(--s2);
    background: var(--raise);
    border-radius: var(--r-lg) var(--r-lg) 0 0;
    padding-bottom: env(safe-area-inset-bottom);
    display: flex;
    flex-direction: column;
    max-height: 60vh;
  }
  /* An open sheet keeps one height; new rows scroll inside it instead of
     growing the sheet and pushing the call controls up. */
  .sheet.open { height: 45vh; height: 45dvh; }
  .handle { display: flex; flex-direction: column; align-items: stretch; gap: var(--s2); min-height: 44px; padding: var(--s2) var(--s4) var(--s3); text-align: left; }
  .bar { align-self: center; width: 40px; height: 4px; border-radius: 2px; background: var(--faint); }
  .peek { font-size: var(--t-body); color: var(--ink-2); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .body { flex: 1; min-height: 0; overflow: auto; padding-bottom: var(--s3); }


  @media (prefers-reduced-motion: reduce) {
    .orb, .orb::before, .orb::after { animation: none !important; transition: none; }
  }
</style>
