<script lang="ts">
  import type { Member } from '../api/room.ts'
  import type { ComputerInfo } from '../api/computer.ts'
  import type { RoomControlValue } from './roomSessionControls.ts'
  import { flip } from 'svelte/animate'
  import { fade } from 'svelte/transition'
  import { cubicOut } from 'svelte/easing'
  import { motion, BASE, FAST } from '../ui/motion.ts'
  import { sessionStatus } from '../sessions/status.ts'
  import { selectableSession } from '../sessions/consolidation.ts'
  import { harnessIcons } from '../settings/harnesses/icons.ts'
  import Avatar from '../ui/Avatar.svelte'
  import RoomSessionStatus from './RoomSessionStatus.svelte'
  import { ago, clock, stamp } from '../ui/format.ts'
  import DoingLine from '../sessions/DoingLine.svelte'
  import ResponsibilityCount from './ResponsibilityCount.svelte'
  import ExpediteBadge from './ExpediteBadge.svelte'
  import { expedite, agentExpedited } from './expedite.ts'
  import MonitorGlyph from '../computers/MonitorGlyph.svelte'
  import { isLive, hasComputer, appName, ageText } from '../computers/computers.ts'
  import { finishedTrees, foldMembers, isFinished, openTrail, orderMemberSessions } from './memberOrder.ts'

  // sessionRow draws one folded row. The root row has no fold state, so the
  // fold fields stay optional for it.
  interface SessionRow {
    member: Member
    hasChildren?: boolean
    folded?: boolean
    hidden?: number
  }

  interface Props {
    members?: Member[]
    focus?: string
    scope?: string
    selected?: string
    fresh?: string
    closed?: boolean
    computers?: ComputerInfo[]
    onopencomputer?: (member: Member) => void
    onselect: (member: Member | { rootSeat: true }) => void
    onclosed: () => void
    onmenu: (member: Member, x: number, y: number, anchor?: HTMLElement) => void
    selecting?: boolean
    picked?: string[]
    onselection?: () => void
    onpick?: (member: Member, checked: boolean, begin: boolean) => void
    onactions?: (x: number, y: number, anchor?: HTMLElement) => void
    roomControls?: RoomControlValue | null
    onroomactions?: (x: number, y: number, anchor?: HTMLElement) => void
    onresumeroom?: () => void
    onreloadcontrols?: () => void
    oncleardormant?: ((count: number) => void) | null
    ondismiss?: ((member: Member) => void) | null
    dismissing?: string
    ontakeseat?: (() => void) | null
  }

  let { members = [], focus = '', scope = '', selected = '', fresh = '', closed = false, computers = [], onopencomputer = () => {}, onselect, onclosed, onmenu, selecting = false, picked = [], onselection = () => {}, onpick = () => {}, onactions = () => {}, roomControls = null, onroomactions = () => {}, onresumeroom = () => {}, onreloadcontrols = () => {}, oncleardormant = null, ondismiss = null, dismissing = '', ontakeseat = null }: Props = $props()

  // The session menu opens by right click, the Menu key or Shift+F10 on a
  // focused row, or the ⋯ button. Keyboard opens anchor it under the row.
  function menuKey(e: KeyboardEvent & { currentTarget: HTMLButtonElement }, m: Member) {
    if (e.key === 'ContextMenu' || (e.key === 'F10' && e.shiftKey)) {
      e.preventDefault()
      const r = e.currentTarget.getBoundingClientRect()
      onmenu(m, r.left + 24, r.bottom, e.currentTarget)
    }
  }

  function rowClick(e: MouseEvent & { currentTarget: HTMLButtonElement }, m: Member) {
    if (!e.target || !('closest' in e.target) || typeof e.target.closest !== 'function' || e.target.closest('button, a, input, summary') !== e.currentTarget) return
    if (selecting || e.ctrlKey || e.metaKey) {
      e.preventDefault()
      if (!selectableSession(m)) return
      onpick(m, !pickedSet.has(m.id ?? ''), !selecting)
    } else onselect(m)
  }
  function selectionKeydown(e: KeyboardEvent) {
    if (e.key === 'Escape' && selecting && !e.defaultPrevented) {
      e.preventDefault(); e.stopPropagation(); onselection()
    }
  }

  // The clock keeps relative ages fresh.
  let now = $state(Date.now())
  $effect(() => {
    // A live member's running call ticks each second; otherwise each minute.
    const t = setInterval(() => (now = Date.now()), members.some((m) => m.doing?.since) ? 1000 : 60 * 1000)
    return () => clearInterval(t)
  })
  const finished = (m: Member) => isFinished(m)

  const computerOf = (id: string | undefined) => computers.find((c) => c.session === id)
  // The small hover note on the monitor glyph: the app and the picture age.
  const computerTip = (c: ComputerInfo | undefined) => [appName(c?.apps?.[0]?.appId) || (isLive(c) ? 'Computer' : 'Computer stopped'), c?.shotAt && ageText(c.shotAt, now)].filter(Boolean).join(' · ')
  let pickedSet = $derived(new Set(picked))
  let owner = $derived(members.find((m) => m.owner))
  // The open internal root sits with the owner, not in Services: it holds the
  // room's root seat unless an external orchestrator attached. Closed roots
  // stay in Services, so their transcripts remain reachable.
  let rootSession = $derived(members.find((m) => m.role === 'root' && !m.closed))
  let seat = $derived(owner?.seat ?? {})
  let sessions = $derived(orderMemberSessions(members.filter((m) => !m.owner && !m.service)))
  // An external holder takes the root row; the internal root then waits in Services.
  let external = $derived(!!seat.external)
  let services = $derived(orderMemberSessions(members.filter((m) => !m.owner && m.service && (external || m.id !== rootSession?.id))))
  let liveCount = $derived(sessions.filter((m) => m.state === 'live').length)

  // Subagents fold under their parent, like a Discord category. The focused
  // channel's lead and its ancestors open by default, and so does the path to
  // a session just started. A click on a chevron overrides the default for
  // this room and view only.
  let overrides = $state<Record<string, Record<string, boolean>>>({})
  let trail = $derived(new Set([...openTrail([...sessions, ...services], focus), ...openTrail([...sessions, ...services], members.find((m) => m.id === fresh)?.parent)]))
  const isOpen = (id: string | undefined) => selecting || ((id === undefined ? undefined : overrides[scope]?.[id]) ?? trail.has(id))
  let rows = $derived(foldMembers(sessions, isOpen))
  let serviceRows = $derived(foldMembers(services, isOpen))
  // Trees with no open session except finished ones fold into a Finished group.
  // It opens by itself while it holds the focused channel or a selection is in progress.
  let doneSet = $derived(finishedTrees(sessions))
  let mainRows = $derived(rows.filter((row) => !doneSet.has(row.member.id)))
  let finishedRows = $derived(rows.filter((row) => doneSet.has(row.member.id)))
  let finishedCount = $derived([...sessions, ...services].filter(isFinished).length)
  // Services stay folded to one line. Only a failed service needs attention.
  let failedServices = $derived(services.filter((m) => m.state === 'error').length)
  let servicesChoice = $state<boolean | null>(null)
  let servicesOpen = $derived(selecting || (servicesChoice ?? serviceRows.some((row) => trail.has(row.member.id) || row.member.address === selected)))
  let finishedChoice = $state<boolean | null>(null)
  let finishedOpen = $derived(selecting || (finishedChoice ?? finishedRows.some((row) => trail.has(row.member.id) || row.member.address === selected)))
  function toggleFold(id: string | undefined) {
    if (id === undefined) return
    overrides = { ...overrides, [scope]: { ...overrides[scope], [id]: !isOpen(id) } }
  }
</script>

{#snippet sessionRow(row: SessionRow)}
  {@const m = row.member}
  {@const pc = computerOf(m.id)}
  {@const offSeat = external && m.id === rootSession?.id}
  {@const status = sessionStatus(m)}
  <div class="item" style="--depth:{m.depth}">
  {#if row.hasChildren && !selecting}
    <button class="fold" aria-expanded={!row.folded} aria-label="{row.folded ? 'Show' : 'Hide'} subagents of {m.name}" title={row.folded ? 'Show subagents' : 'Hide subagents'} onclick={() => toggleFold(m.id)}>
      <svg viewBox="0 0 16 16" width="12" height="12" aria-hidden="true"><path d="m6 4 4 4-4 4" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" /></svg>
    </button>
  {/if}
  <button class="row" class:sel={selecting ? pickedSet.has(m.id ?? '') : selected === m.address} class:closed={m.closed} class:finished={finished(m)} class:off={offSeat} class:fresh={fresh === m.id} class:pc={hasComputer(pc)} onclick={(e) => rowClick(e, m)} aria-pressed={selecting ? pickedSet.has(m.id ?? '') : undefined} aria-disabled={selecting && !selectableSession(m) ? true : undefined} title="{m.name} · {m.profile} · {sessionStatus(m, true)} · active {ago(m.lastActivity)}" oncontextmenu={(e) => { e.preventDefault(); onmenu(m, e.clientX, e.clientY, e.currentTarget) }} onkeydown={(e) => menuKey(e, m)}>
    {#if m.depth > 0}
      <span class="guide" aria-hidden="true"></span>
    {/if}
    <Avatar emoji={m.emoji} name={m.name} hue={m.hue} size={32} state={finished(m) ? 'finished' : m.state} status={m.status} ring="var(--side)" />
    <span class="text">
      <span class="name hue-{m.hue}"><span class="nm">{offSeat ? 'root (internal)' : m.name}</span>{#if row.folded}<span class="kids" title="{row.hidden} folded subagent{row.hidden === 1 ? '' : 's'}">{row.hidden}</span>{/if}{#if agentExpedited($expedite, m)} <ExpediteBadge short />{/if}</span>
      <span class="sub {finished(m) ? 'finished' : m.state}">{#if offSeat}off seat{:else if finished(m)}dormant · {ago(m.lastActivity)}{:else}{#if status}<span class="now" class:working={status === 'working'}>{status}</span> · {/if}{#if m.state === 'live' && m.doing?.text}<DoingLine doing={m.doing} {now} />{:else}{m.profile}{/if}{/if}</span>
      <ResponsibilityCount counts={m.responsibilities} />
    </span>
  </button>
      {#if finished(m) && !selecting && ondismiss}
        <div class="row-actions">
          <button type="button" class="row-action" disabled={!!dismissing} onclick={(e) => { e.stopPropagation(); ondismiss(m) }}>{dismissing === m.id ? 'Dismissing…' : 'Dismiss'}</button>
        </div>
      {/if}
  {#if hasComputer(pc)}<button class="pc-glyph" aria-label="Open {m.name}'s computer" title={computerTip(pc)} onclick={() => onopencomputer(m)}><MonitorGlyph size={15} live={isLive(pc)} /></button>{/if}
  <button class="more" aria-label="Actions for {m.name}" title="Actions" onclick={(e) => { const r = e.currentTarget.getBoundingClientRect(); const anchor = e.currentTarget.previousElementSibling; onmenu(m, r.left, r.bottom + 4, anchor instanceof HTMLElement ? anchor : undefined) }}>
    <svg viewBox="0 0 20 20" width="16" height="16" aria-hidden="true"><circle cx="5" cy="10" r="1.6" fill="currentColor" /><circle cx="10" cy="10" r="1.6" fill="currentColor" /><circle cx="15" cy="10" r="1.6" fill="currentColor" /></svg>
  </button>
  </div>
{/snippet}

<!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
<aside class="members" aria-label="Members" onkeydown={selectionKeydown}>
  <div class="scroll">
    <h2>Owner</h2>
    {#if owner}
      <div class="row owner">
        <Avatar name="you" owner hue={owner.hue} size={32} ring="var(--side)" />
        <span class="text">
          <span class="name hue-{owner.hue}">you</span>
          <span class="sub">{owner.children} top-level session{owner.children === 1 ? '' : 's'}</span>
        </span>
      </div>
      {#if external}
        <button type="button" class="row" class:sel={selected === owner.address + ':root'} onclick={() => onselect({ rootSeat: true })} aria-label="Open root conversation">
          <Avatar name="root" image={harnessIcons[seat.harness ?? '']} emoji="🖥️" hue={owner.hue} size={32} ring="var(--side)" />
          <span class="text">
            <span class="name hue-{owner.hue}">{seat.harness ? 'root' : 'root (external)'}</span>
            <span class="sub" title={stamp(seat.since)}>{seat.harness || 'external'} · since {clock(seat.since)}</span>
          </span>
        </button>
        {#if ontakeseat}<div class="row-actions"><button type="button" class="row-action" onclick={ontakeseat}>Take back seat</button></div>{/if}
      {:else if rootSession}
        {@render sessionRow({ member: { ...rootSession, depth: 0 } })}
      {:else}
        <button type="button" class="seat" onclick={() => onselect({ rootSeat: true })}>root starts when you post to @root</button>
      {/if}
    {/if}
    <h2>Sessions <span class="n">{sessions.length}</span>{#if liveCount}<span class="livecount">{liveCount} live</span>{/if}<button class="select-mode" aria-pressed={selecting} onclick={onselection}>Select</button><button class="room-actions" aria-label="Session controls" aria-haspopup="menu" title="Session controls" onclick={(e) => { const r = e.currentTarget.getBoundingClientRect(); onroomactions(r.left, r.bottom + 4, e.currentTarget) }}><svg viewBox="0 0 20 20" width="16" height="16" aria-hidden="true"><circle cx="5" cy="10" r="1.6" fill="currentColor" /><circle cx="10" cy="10" r="1.6" fill="currentColor" /><circle cx="15" cy="10" r="1.6" fill="currentColor" /></svg></button></h2>
    <RoomSessionStatus value={roomControls} onresume={onresumeroom} onreload={onreloadcontrols} />
    {#if selecting}<p class="selection-hint">Click rows to select sessions.</p>{/if}
    {#if sessions.length === 0}
      <p class="none">No sessions in this room yet.</p>
    {/if}
    <!-- Folding and Show closed sessions remove rows immediately. -->
    {#each mainRows as row (row.member.id)}<div animate:flip={{ duration: motion(BASE), easing: cubicOut }} in:fade={{ duration: motion(FAST) }}>{@render sessionRow(row)}</div>{/each}
    {#if finishedRows.length}
      <h2 class="group"><button class="group-toggle" aria-expanded={finishedOpen} onclick={() => (finishedChoice = !finishedOpen)}><svg viewBox="0 0 16 16" width="12" height="12" aria-hidden="true"><path d="m6 4 4 4-4 4" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" /></svg>Dormant <span class="n">{finishedRows.filter((row) => !doneSet.has(row.member.parent)).length}</span></button>{#if oncleardormant}<button class="group-clear" title="Close {finishedCount} dormant sessions" disabled={!!roomControls?.busy || !finishedCount} onclick={() => oncleardormant(finishedCount)}>Clear dormant</button>{/if}</h2>
      {#if finishedOpen}{#each finishedRows as row (row.member.id)}<div animate:flip={{ duration: motion(BASE), easing: cubicOut }} in:fade={{ duration: motion(FAST) }}>{@render sessionRow(row)}</div>{/each}{/if}
    {/if}
    {#if services.length}
      <h2 class="group"><button class="group-toggle" aria-expanded={servicesOpen} onclick={() => (servicesChoice = !servicesOpen)}><svg viewBox="0 0 16 16" width="12" height="12" aria-hidden="true"><path d="m6 4 4 4-4 4" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" /></svg>Services <span class="n">{services.length}</span>{#if failedServices}<span class="attention"> · {failedServices} need{failedServices === 1 ? 's' : ''} attention</span>{/if}</button></h2>
      {#if servicesOpen}{#each serviceRows as row (row.member.id)}<div animate:flip={{ duration: motion(BASE), easing: cubicOut }} in:fade={{ duration: motion(FAST) }}>{@render sessionRow(row)}</div>{/each}{/if}
    {/if}

  </div>
  <footer>
    {#if selecting}<div class="selection-bar"><span role="status">{picked.length} selected</span><button class="toggle" onclick={onselection}>Cancel</button><button class="consolidate" disabled={!picked.length} onclick={(e) => { const r = e.currentTarget.getBoundingClientRect(); onactions(r.left, r.top, e.currentTarget) }}>Actions…</button></div>{/if}
    <button class="toggle" onclick={onclosed}>{closed ? 'Hide closed sessions' : 'Show closed sessions'}</button>
  </footer>
</aside>

<style>
  .members {
    width: 264px;
    flex: none;
    background: var(--side);
    display: flex;
    flex-direction: column;
    min-height: 0;
  }
  .room-actions { display: inline-flex; align-items: center; justify-content: center; min-width: 24px; min-height: 28px; color: var(--muted); transition: color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .room-actions:hover { color: var(--ink); }
  }
  .scroll { flex: 1; overflow-y: auto; padding: var(--s1) var(--s2) var(--s3); }
  h2 {
    display: flex;
    align-items: baseline;
    gap: 6px;
    margin: 18px var(--s2) 6px;
    font-size: var(--t-sm);
    font-weight: 600;
    color: var(--muted);
  }
  h2:first-child { margin-top: 14px; }
  h2.group { margin-top: 14px; }
  .group-toggle { display: flex; align-items: baseline; gap: 6px; font: inherit; color: inherit; padding: 2px 0; transition: color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .group-toggle:hover { color: var(--ink); }
  }
  .group-toggle svg { align-self: center; transform: rotate(90deg); transition: transform var(--motion-fast); }
  .group-toggle[aria-expanded="false"] svg { transform: none; }
  .group-clear { margin-left: auto; font-size: var(--t-sm); font-weight: 500; color: var(--muted); padding: 2px var(--s1); border-radius: 4px; }
  .group-clear:not(:disabled) { transition: color var(--motion-fast) var(--ease), background-color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .group-clear:hover:not(:disabled) { color: var(--ink); background: var(--hover); }
  }
  .group-clear:active:not(:disabled) {
    background: var(--select);
}
  .group-clear:disabled { color: var(--faint); cursor: default; }
  .n { color: var(--faint); font-weight: 500; }
  .livecount { margin-left: auto; color: var(--gold); font-weight: 500; font-size: var(--t-sm); }
  .select-mode { margin-left: auto; font-size: var(--t-sm); color: var(--muted); padding: var(--s1); border-radius: 4px; transition: color var(--motion-fast) var(--ease), background-color var(--motion-fast) var(--ease); }
  .select-mode[aria-pressed="true"] { color: var(--iris); }
  @media (hover: hover) {
    .select-mode:hover { color: var(--ink); background: var(--hover); }
  }
  .select-mode:active {
    background: var(--select);
}
  .selection-hint { margin: var(--s2); color: var(--muted); font-size: var(--t-sm); line-height: var(--leading); }
  .selection-bar { display: flex; align-items: center; gap: 6px; justify-content: space-between; font-size: var(--t-sm); color: var(--muted); margin-bottom: 10px; }
  .consolidate { color: var(--iris); font-size: var(--t-sm); padding: 5px 0; }
  .consolidate:disabled { color: var(--faint); cursor: default; }
  .none { margin: 2px var(--s2); font-size: var(--t-ui); color: var(--faint); }
  .row {
    position: relative;
    width: 100%;
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 5px 8px 5px calc(18px + var(--depth, 0) * 18px);
    border-radius: 5px;
    text-align: left;
    margin: 1px 0;
  }
  .row:not(.owner) { transition: background-color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .row:not(.owner):hover { background: var(--hover); }
  }
  .row:not(.owner):active {
    background: var(--select);
}
  .seat { margin: 2px var(--s2) var(--s2); font-size: var(--t-sm); color: var(--muted); display: flex; flex-wrap: wrap; align-items: center; gap: 6px; }
  .row.sel, .row:not(.owner).sel { background: var(--select); }
  .row.closed { opacity: 0.55; }
  .attention { color: var(--coral); text-transform: none; letter-spacing: 0; }
  .row.finished { opacity: 0.72; }
  .row.off { opacity: 0.6; }
  .row.finished .name { color: var(--muted); }
  .row-actions { display: flex; align-items: center; gap: var(--s3); margin: -2px 28px var(--s2) 52px; }
  .row-action { font-size: var(--t-cap); color: var(--muted); padding: 1px 0; }
  .row-action:not(:disabled) { transition: color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .row-action:hover:not(:disabled) { color: var(--ink); text-decoration: underline; }
  }
  .row-action:disabled { color: var(--faint); cursor: default; }
  .item { position: relative; }
  /* The agent's computer: a small monitor at the right of its row. The
     actions button covers it on hover, so it sits left of that button. */
  .row.pc { padding-right: 56px; }
  .pc-glyph { position: absolute; right: 34px; top: 50%; transform: translateY(-50%); width: 22px; height: 22px; display: grid; place-items: center; border-radius: 5px; transition: background-color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .pc-glyph:hover { background: var(--hover); }
  }
  .pc-glyph:active {
    background: var(--select);
}
  /* The chevron sits in the gutter left of the avatar. */
  .fold {
    position: absolute;
    z-index: 1;
    left: calc(var(--depth, 0) * 18px + 2px);
    top: 50%;
    transform: translateY(-50%);
    width: 16px;
    height: 24px;
    display: grid;
    place-items: center;
    border-radius: 4px;
    color: var(--faint);
    transition: color var(--motion-fast) var(--ease), background-color var(--motion-fast) var(--ease);
  }
  @media (hover: hover) {
    .fold:hover { color: var(--ink); background: var(--hover); }
  }
  .fold:active {
    background: var(--select);
}
  .fold svg { transform: rotate(90deg); transition: transform var(--motion-fast); }
  .fold[aria-expanded="false"] svg { transform: none; }
  .more {
    position: absolute;
    right: 6px;
    top: 50%;
    transform: translateY(-50%);
    width: 26px;
    height: 26px;
    border-radius: 5px;
    display: none;
    place-items: center;
    color: var(--muted);
    background: var(--raise);
    transition: color var(--motion-fast) var(--ease);
  }
  .more:focus-visible { display: grid; }
  .item:hover .more { display: grid; }
  /* Touch: the actions button is always there, every control is as big as a
     thumb, and a long press on a row opens the same menu without the system
     text callout. */
  @media (hover: none) {
    .more { display: grid; width: 40px; height: 40px; }
    .pc-glyph { right: 46px; width: 40px; height: 40px; }
    .row.pc { padding-right: 92px; }
    /* The chevron keeps its 12 px glyph and takes a taller target in the
       gutter left of the avatar. */
    .fold { width: 24px; height: 44px; }
    .select-mode, .room-actions { min-width: 44px; min-height: 44px; }
    .group-toggle, .group-clear, .toggle { min-height: 44px; }
    .row { -webkit-touch-callout: none; user-select: none; }
  }
  @media (hover: hover) {
    .more:hover { color: var(--ink); }
  }
  /* A session just started from the composer flashes once. */
  .row.fresh { animation: fresh var(--motion-slow) ease-out 2; }
  @keyframes fresh { from { background: color-mix(in srgb, var(--sage) 28%, transparent); } }
  .guide {
    position: absolute;
    left: calc(var(--depth) * 18px + 8px);
    top: -4px;
    width: 10px;
    height: 24px;
    border-left: 1.5px solid #3f3c40;
    border-bottom: 1.5px solid #3f3c40;
    border-bottom-left-radius: 6px;
  }
  .text { display: flex; flex-direction: column; min-width: 0; flex: 1; line-height: var(--leading-tight); }
  .name {
    font-weight: 600;
    font-size: var(--t-body);
    min-width: 0;
    display: flex;
    align-items: center;
    gap: 6px;
  }
  /* The name is the flex item that gives way: it ends in an ellipsis, where
     the badges after it keep their size. */
  .nm {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .kids {
    font-size: var(--t-cap);
    font-weight: 600;
    color: var(--muted);
    background: var(--raise);
    border-radius: 7px;
    padding: 0 5px;
    line-height: 15px;
  }
  .sub { font-size: var(--t-sm); color: var(--faint); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  /* The status reads as the DM header does: gold only while working. */
  .sub .now.working { color: var(--gold); }
  .sub.error, .sub.finished { color: var(--coral); }
  .sub.done { color: var(--muted); }
  footer { flex: none; padding: var(--s2) var(--s4) var(--s3); border-top: 1px solid var(--line-soft); }
  .toggle { font-size: var(--t-sm); color: var(--muted); transition: color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .toggle:hover { color: var(--ink); text-decoration: underline; }
  }
</style>
