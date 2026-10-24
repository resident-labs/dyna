<script lang="ts">
  // The page's top bar: the drawer button on a phone, the sidebar toggle on a
  // desktop, the title and trail of the open screen, and the buttons that open
  // Search, the pinned posts, the review pane and the member list.
  // App keeps the screen state and the rule that one panel closes the other,
  // so this component shows that state and calls back for each action.
  import { inUse } from '../../computers/preview.ts'
  import { roomLabel } from '../../ui/format.ts'
  import { machinesSub } from '../../machines/machines.ts'
  import CallButton from '../../voice/CallButton.svelte'
  import DmIdentity from '../../sessions/DmIdentity.svelte'
  import PinIcon from '../../ui/PinIcon.svelte'
  import ReviewIcon from '../../ui/ReviewIcon.svelte'
  import SearchIcon from '../../ui/SearchIcon.svelte'
  import SessionTabs from '../../sessions/SessionTabs.svelte'
  import SyncBadge from '../../lanesync/SyncBadge.svelte'
  import Tabs from '../../ui/Tabs.svelte'
  import UseIcons from '../../sessions/UseIcons.svelte'
  import type { ComponentProps } from 'svelte'
  import type { Drill } from '../router.svelte.ts'
  import type { ComputerInfo } from '../../api/computer.ts'
  import type { MachineDetail, MachineSummary } from '../../api/machine.ts'
  import type { Member, Post, Room } from '../../api/room.ts'
  import type { FeedMember } from '../../rooms/feedViews.ts'
  import type { Session as SessionData } from '../../api/session.ts'
  import type { RunDetail } from '../../api/workflow.ts'
  import type { Row, Tab } from '../../rooms/workViews.ts'
  import type { Remote } from '../../lanesync/sync.ts'
  // The bar reads the screen the page shows and calls back for every change:
  // App owns the drawer, the sidebar and the two side panels.
  interface Props {
    /** The screen is narrow: the room list is a drawer and the bar loses its labels. */
    narrow: boolean
    navOpen: boolean
    showSide: boolean
    roomId: string
    room: Room | null
    current: Room | undefined
    drill: Drill | null
    session: SessionData | null
    machines: MachineSummary[] | null
    machineId: string
    machineDetail: MachineDetail | null
    readOnlyDm: boolean
    reviewOpen: boolean
    people: Member[]
    live: Member[]
    pins: Post[]
    /** The open session tab. The bar's tab row writes it. */
    sessionTab: string
    /** The session's computer, or null when computers are off. */
    sessionComputer: Pick<ComputerInfo, 'state'> | null
    /** The session's computer, for the use icon that shows while it runs. */
    drillComputer: Pick<ComputerInfo, 'state' | 'shotAt'> | null
    shells: number
    isRootDm: boolean
    threadsOpen: boolean
    threadCount: number
    sessionTrail: FeedMember[]
    view: string
    openRun: string
    runTrail: FeedMember[]
    runDetail: RunDetail | null
    workRow: Row | undefined
    workTabs: Tab[] | null
    syncRemotes: Remote[]
    syncRepo: ComponentProps<typeof SyncBadge>['status']
    syncNote: string
    showPins: boolean
    showMembers: boolean
    onnavopen: () => void
    ontoggleside: () => void
    onback: () => void
    onsession: (member: FeedMember) => void
    onmember: (member: Member) => void
    onthreads: () => void
    oncloserun: () => void
    onview: (view: string) => void
    onsyncrefresh: () => void
    onsyncexport: () => void
    onsearch: () => void
    ontogglepins: () => void
    onreview: () => void
    ontogglemembers: () => void
  }
  let { narrow, navOpen, showSide, roomId, room, current, drill, session, machines, machineId, machineDetail, readOnlyDm, reviewOpen, people, live, pins, sessionTab = $bindable(), sessionComputer, drillComputer, shells, isRootDm, threadsOpen, threadCount, sessionTrail, view, openRun, runTrail, runDetail, workRow, workTabs, syncRemotes, syncRepo, syncNote, showPins, showMembers, onnavopen, ontoggleside, onback, onsession, onmember, onthreads, oncloserun, onview, onsyncrefresh, onsyncexport, onsearch, ontogglepins, onreview, ontogglemembers }: Props = $props()
</script>

<header class="bar" class:dm-bar={!!drill} inert={narrow && reviewOpen}>
  {#if narrow}
    <button class="icon" onclick={onnavopen} title="Rooms and views" aria-label="Open the room list" aria-expanded={navOpen}>
      <svg viewBox="0 0 24 24" width="22" height="22" aria-hidden="true"><path d="M4 7h16M4 12h16M4 17h16" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" /></svg>
    </button>
  {:else}
  <button class="icon" onclick={ontoggleside} title={showSide ? 'Hide sidebar (Ctrl+B)' : 'Show sidebar (Ctrl+B)'} aria-label={showSide ? 'Hide sidebar' : 'Show sidebar'} aria-expanded={showSide}>
    <svg viewBox="0 0 24 24" width="20" height="20" aria-hidden="true"><rect x="3.5" y="4.5" width="17" height="15" rx="2.5" fill="none" stroke="currentColor" stroke-width="1.8" /><path d="M9.5 4.5v15" stroke="currentColor" stroke-width="1.8" />{#if showSide}<path d="M5.8 8.5h1.6M5.8 11.5h1.6" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" />{/if}</svg>
  </button>
  {/if}
  {#if !roomId}
    <span class="title">{machineDetail ? machineDetail.name : 'Machines'}</span>
    {#if !machineId}<span class="meta">{machinesSub(machines)}</span>{/if}
  {:else if room}
    {#if drill}
      {#if readOnlyDm}
        <button class="dm-back" aria-label="Back to Services" title="Back to Services" onclick={onback}><span class="back-arrow" aria-hidden="true">‹</span><span class="back-name">Services</span></button>
        <span class="dm-sep" aria-hidden="true">/</span>
        <span class="title">{drill.name}</span>
      {:else}
      <button class="dm-back" aria-label={`Back to ${roomLabel(current ?? room)}`} title={`Back to ${roomLabel(current ?? room)}`} onclick={onback}><span class="back-arrow" aria-hidden="true">‹</span><span class="back-name">{roomLabel(current ?? room)}</span></button>
      {#if sessionTrail.length}
        <nav class="trail" aria-label="Parent sessions">
          {#each sessionTrail as m (m.id)}
            <span class="dm-sep" aria-hidden="true">/</span>
            <button class="dm-back dm-crumb" title={`Open ${m.name}`} onclick={() => onsession(m)}><span class="back-name">{m.name}</span></button>
          {/each}
        </nav>
      {/if}
      <span class="dm-sep" aria-hidden="true">/</span>
      <DmIdentity member={session?.member || drill} usage={session?.usage} onopen={onmember} />
      {#if !narrow}<div class="dm-tabs"><SessionTabs bind:tab={sessionTab} media={session?.media ?? []} computer={sessionComputer} shells={shells} /></div>{/if}
      <span class="spacer"></span>
      <UseIcons shells={shells} computer={inUse(drillComputer)} tab={sessionTab} onselect={(tab) => (sessionTab = tab)} />
      {#if isRootDm}<CallButton {room} />{/if}
      <button class="dm-threads threads" class:on={threadsOpen} aria-expanded={threadsOpen} aria-label="Threads" title="Threads" onclick={onthreads}><svg viewBox="0 0 20 20" width="16" height="16" aria-hidden="true"><path d="M4 5.5A1.5 1.5 0 0 1 5.5 4h9A1.5 1.5 0 0 1 16 5.5v6a1.5 1.5 0 0 1-1.5 1.5H9l-3.5 3v-3h0A1.5 1.5 0 0 1 4 11.5z" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linejoin="round" /></svg>{#if threadCount}<b>{threadCount}</b>{/if}</button>
      {/if}
    {:else if view === 'runs' && openRun}
      <button class="crumb" onclick={oncloserun}><span class="hash">#</span>runs</button>
      <span class="trail">
        {#each runTrail as m (m.id)}
          <span class="slash" aria-hidden="true">/</span>
          <button class="crumb" title={`Open ${m.name}`} onclick={() => onsession(m)}>{m.name}</button>
        {/each}
      </span>
      <span class="slash" aria-hidden="true">/</span>
      <span class="title">{runDetail?.run?.name ?? openRun}</span>
    {:else if workRow}
      <span class="title" title={room.repo}><span class="hash">#</span><span class="name">{workRow.label}</span></span>
      {#if workTabs && !narrow}<div class="dm-tabs"><Tabs tabs={workTabs} tab={view} label={workRow?.label ?? ''} onselect={onview} /></div>{/if}
    {:else}
      <span class="title" title={room.repo}><span class="name">{roomLabel(current ?? room)}</span></span>
    {/if}
    {#if !drill}
    <span class="spacer"></span>
    <CallButton {room} />
    <span class="meta">{Math.max(0, people.length - 1)} session{people.length === 2 ? '' : 's'}{#if live.length}&nbsp;·&nbsp;<em>{live.length} working</em>{/if}</span>
    {#if syncRemotes.length}<SyncBadge remotes={syncRemotes} status={syncRepo} note={syncNote} onrefresh={onsyncrefresh} onexport={onsyncexport} />{/if}
    {#if !narrow}
      <button class="icon" onclick={onsearch} title="Search (Ctrl+K)" aria-label="Search sessions and messages"><SearchIcon /></button>
    {/if}
    <button class="icon pin" class:on={showPins} onclick={ontogglepins} title={showPins ? 'Hide pinned posts' : 'Show pinned posts'} aria-label={showPins ? 'Hide pinned posts' : 'Show pinned posts'} aria-expanded={showPins}>
      <PinIcon size={21} />
      {#if pins.length}<span class="badge">{pins.length}</span>{/if}
    </button>
    {/if}
  {/if}
  {#if room?.repo && !readOnlyDm}<button class="icon" class:on={reviewOpen} aria-controls="review-workspace" aria-expanded={reviewOpen} title={reviewOpen ? 'Hide review' : 'Review files, changes, PRs and issues'} aria-label={reviewOpen ? 'Hide review' : 'Show review'} onclick={onreview}>
    <ReviewIcon size={22} />
  </button>{/if}
  {#if !roomId}<span class="spacer"></span>{/if}
  {#if narrow}
    <button class="icon" onclick={onsearch} title="Search (Ctrl+K)" aria-label="Search sessions and messages"><SearchIcon size={22} /></button>
  {/if}
  <!-- Members closes the pinned panel, so the two panels never share the
       side area. The button is the bar's last one, on a phone and a desktop. -->
  {#if room && !drill}
    <button class="icon" class:on={showMembers} onclick={ontogglemembers} title={showMembers ? 'Hide member list' : 'Show member list'} aria-label={showMembers ? 'Hide member list' : 'Show member list'} aria-expanded={showMembers}>
      <svg viewBox="0 0 24 24" width="22" height="22" aria-hidden="true"><circle cx="9" cy="8" r="3.2" fill="none" stroke="currentColor" stroke-width="1.8" /><path d="M3.5 19c.6-3.2 2.8-5 5.5-5s4.9 1.8 5.5 5" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" /><circle cx="16.5" cy="9" r="2.4" fill="none" stroke="currentColor" stroke-width="1.6" /><path d="M16 13.6c2.4 0 4 1.5 4.5 4.4" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" /></svg>
    </button>
  {/if}
</header>

<style>
  .bar {
    height: 64px;
    flex: none;
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 0 16px 0 10px;
    border-bottom: 1px solid #1a191c;
    box-shadow: 0 1px 0 rgba(0, 0, 0, 0.18);
    min-width: 0;
  }
  .dm-bar { height: 56px; gap: 8px; }
  /* A work view gives its whole width to the tab labels: the room's repo path
     is in the title's tooltip, because a clipped tab is worse than a clipped
     path. Under 760 px the tabs scroll instead. */
  .dm-tabs { min-width: 0; }
  .dm-tabs :global(.tabs) { border: 0; padding: 0; }
  .dm-tabs :global(button) { padding: 5px 10px; font-size: 12.5px; white-space: nowrap; border-radius: 6px; }
  .dm-tabs :global(button.on) { background: var(--raise); }
  .dm-tabs :global(button.on::after) { display: none; }
  .dm-threads, .dm-back { flex: none; color: var(--muted); font-size: 12.5px; padding: 6px 8px; border-radius: 6px; transition: color var(--motion-fast) var(--ease), background-color var(--motion-fast) var(--ease); }
  .dm-back { display: inline-flex; align-items: center; gap: 5px; max-width: 160px; }
  .back-arrow { font-size: 17px; line-height: 1; }
  .back-name { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .dm-sep { color: var(--line); margin: 0 -2px; }
  .dm-threads { display: inline-flex; align-items: center; gap: 6px; }
  .dm-threads.on { color: var(--ink); background: var(--raise); }
  .dm-threads b { border-radius: 8px; color: var(--side); background: var(--iris); font-size: 11px; padding: 0 5px; }
  @media (hover: hover) {
    .dm-back:hover { color: var(--ink); background: var(--hover); }
  }
  .dm-back:active { background: var(--select); }
  .title { font-weight: 600; font-size: 16px; display: inline-flex; align-items: center; white-space: nowrap; min-width: 0; }
  .title .name { min-width: 0; overflow: hidden; text-overflow: ellipsis; }
  .hash { color: var(--faint); font-size: 22px; font-weight: 400; margin-right: 6px; line-height: 1; }
  .crumb { display: inline-flex; align-items: center; font-weight: 600; color: var(--muted); font-size: 16px; transition: color var(--motion-fast) var(--ease); }
  /* The view trail is one flex item, so a long trail shrinks as a unit. */
  .trail { display: flex; align-items: center; gap: 10px; min-width: 0; }
  @media (hover: hover) {
    .crumb:hover { color: var(--ink); }
  }
  .slash { color: var(--faint); }
  .trail { display: contents; }
  .dm-crumb { max-width: 140px; }
  .who { display: inline-flex; align-items: center; gap: 8px; }
  .chip { font-size: 12px; color: var(--muted); background: var(--raise); border-radius: 4px; padding: 1px 7px; }
  .spacer { flex: 1; }
  .meta { color: var(--faint); font-size: 13px; white-space: nowrap; }
  .meta em { font-style: normal; color: var(--gold); }
  .icon { color: var(--muted); display: grid; place-items: center; width: 32px; height: 32px; border-radius: 6px; transition: color var(--motion-fast) var(--ease), background-color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .icon:hover { color: var(--ink); background: var(--hover); }
  }
  .icon:active { background: var(--select); }
  .icon.on { color: var(--ink); }
  .icon.pin { position: relative; }
  .badge {
    position: absolute;
    top: 0;
    right: -2px;
    min-width: 15px;
    height: 15px;
    padding: 0 4px;
    border-radius: 8px;
    background: var(--select);
    color: var(--ink);
    font-size: 10.5px;
    font-weight: 600;
    line-height: 15px;
    text-align: center;
    border: 1px solid var(--line);
  }

  /* A phone: the bar keeps its controls and gives the title the width that is
     left. Touch targets are at least 44 px. */
  @media (max-width: 760px) {
    .bar { height: 52px; padding: 0 6px 0 4px; gap: 4px; }
    .meta, .slash, .chip { display: none; }
    .dm-bar .back-name, .dm-bar .back-arrow, .dm-bar .dm-sep { display: none; }
    .dm-bar > .spacer { display: none; }
    .dm-bar .dm-back { width: 36px; max-width: none; font-size: 0; padding: 6px; }
    .dm-bar .dm-back::before { content: '←'; font-size: 18px; }
    .dm-threads, .dm-back { min-height: 44px; }
    .dm-bar > .icon:last-child { display: none; }
    /* Keep the page title readable while the bar buttons keep their width.
       The room bar now holds Search and Members too, so the title gives way
       first and ends in an ellipsis on the narrowest phones. */
    .title, .crumb { min-width: 40px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    .trail { gap: 4px; }
    .crumb { flex: 0 1 auto; max-width: 40%; }
    /* A phone shows only the nearest parent of the breadcrumb. In a session,
       its arrow replaces the room arrow and goes up one level. */
    .trail > :not(:last-child) { display: none; }
    .dm-bar .dm-back:has(+ .trail) { display: none; }
    .dm-bar .trail .dm-crumb { width: auto; flex: 0 1 auto; min-width: 0; max-width: 26vw; font-size: 12.5px; padding: 6px; gap: 4px; }
    .dm-bar .trail .dm-crumb::before { flex: none; }
    .dm-bar .trail .back-name { display: inline; }
    .who { min-width: 0; }
    .icon { width: 44px; height: 44px; flex: none; }
  }
</style>
