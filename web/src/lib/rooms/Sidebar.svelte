<script lang="ts">
  import type { Member, Room } from '../api/room.ts'
  import type { ComputerInfo } from '../api/computer.ts'
  import type { MachineSummary } from '../api/machine.ts'
  import type { RunList } from '../api/workflow.ts'
  import type { RouteNotice } from '../settings/link.ts'
  import type { Facts } from '../prs/pr.ts'
  import { flip } from 'svelte/animate'
  import { fade } from 'svelte/transition'
  import { cubicOut } from 'svelte/easing'
  import { motion, BASE, FAST } from '../ui/motion.ts'
  import { GENERAL, ROOM_CHAT } from './feedViews.ts'
  import Avatar from '../ui/Avatar.svelte'
  import Notify from './Notify.svelte'
  import { features } from '../../api.ts'
  import { ago, roomLabel } from '../ui/format.ts'
  import PRChip from '../prs/PRChip.svelte'
  import ExpediteBadge from './ExpediteBadge.svelte'
  import { expedite, agentExpedited } from './expedite.ts'
  import GoalGlyph from './GoalGlyph.svelte'
  import { isLive } from '../computers/computers.ts'
  import { available as terminal } from '../sessions/TermPane.svelte'
  import { sections, rowOf, shown, runningWork } from './workViews.ts'
  import { sessionStatus } from '../sessions/status.ts'
  import MachineRow from '../machines/MachineRow.svelte'
  import { machinesSub } from '../machines/machines.ts'
  // questions: open questions to the owner, the one "needs you" count.
  // computers: the room's computers, or null when the feature is off.
  // runs: the room's workflow runs.
  // machines: the registered machines, or null until they first load.
  // machinesOpen: the rail's machines tile is open, so the sidebar lists the
  // machines of this engine instead of one room's views.
  interface Props {
    machines?: MachineSummary[] | null
    machinesOpen?: boolean
    machineId?: string
    onmachine?: ((id: string) => void) | null
    selected?: string
    onroom?: ((id: string) => void) | null
    route?: RouteNotice | null
    room?: Room | null
    view?: string
    viewUnread?: Record<string, number>
    questions?: number
    live?: Member[]
    runs?: RunList | null
    dataDir?: string
    ownerHue?: number
    update?: boolean
    prs?: Record<string, Facts[]>
    computers?: ComputerInfo[] | null
    onroommenu?: ((x: number, y: number, anchor?: HTMLElement) => void) | null
    onview: (view: string) => void
    onmember: (member: Member) => void
    onsettings?: (() => void) | null
    onlogout?: (() => void) | null
  }
  let { machines = null, machinesOpen = false, machineId = '', onmachine = null, selected = '', onroom = null, route = null, room = null, view = GENERAL, viewUnread = {}, questions = 0, live = [], runs = null, dataDir = '', ownerHue = 0, update = false, prs = {}, computers = null, onroommenu = null, onview, onmember, onsettings = null, onlogout = null }: Props = $props()
  // Gold marks only what needs the owner or runs now; no totals.
  let liveComputers = $derived(new Set((computers ?? []).filter(isLive).map((c) => c.session)).size)
  let running = $derived(runningWork(runs))
  let open = $derived(rowOf(view)?.id)
  const icons: Record<string, string> = {
    messages: 'M4 4.5h12a1.5 1.5 0 0 1 1.5 1.5v7a1.5 1.5 0 0 1-1.5 1.5H9l-3.5 3v-3H4A1.5 1.5 0 0 1 2.5 13V6A1.5 1.5 0 0 1 4 4.5Z',
    questions: 'M10 3a7 7 0 1 1 0 14 7 7 0 0 1 0-14ZM7.9 8.1a2.1 2.1 0 1 1 3 1.9c-.6.3-.9.7-.9 1.3v.4M10 13.9v.1',
    runs: 'M10 3a7 7 0 1 1 0 14 7 7 0 0 1 0-14ZM8.5 7.2v5.6l4.3-2.8Z',
    services: 'M4 4.5h12M4 10h12M4 15.5h7',
    visuals: 'M3 3v14h14M7 13V9M11 13V5M15 13V7',
    code: 'M6 4.5a1.5 1.5 0 1 1-3 0 1.5 1.5 0 0 1 3 0ZM6 15.5a1.5 1.5 0 1 1-3 0 1.5 1.5 0 0 1 3 0ZM17 15.5a1.5 1.5 0 1 1-3 0 1.5 1.5 0 0 1 3 0ZM4.5 6v8M15.5 14V8.5a2.5 2.5 0 0 0-2.5-2.5h-3M11.5 4 9.5 6l2 2',
    library: 'M4 3.5h3.5v13H4ZM8.5 3.5H12v13H8.5ZM13.2 4.6l3.2-.9 2.8 12.3-3.2.9Z',
    terminal: 'M3.5 4h13a1 1 0 0 1 1 1v10a1 1 0 0 1-1 1h-13a1 1 0 0 1-1-1V5a1 1 0 0 1 1-1ZM6 8l2.5 2L6 12M10.5 12.5H14',
    computers: 'M3 4.5h14v9.5H3ZM7 17h6M10 14v3',
  }
</script>

{#snippet person(m: Member, sel: boolean, onclick: () => void)}
  {@const pr = prs[m.id ?? '']?.[0]}
  <button class="chan person" class:sel style="--depth: 0" {onclick}>
    <Avatar emoji={m.emoji} name={m.name} hue={m.hue} size={22} state={m.state} ring="var(--side)" />
    <span class="label hue-{m.hue}">{m.name}</span>
    {#if agentExpedited($expedite, m)}<ExpediteBadge short />{/if}
    {#if pr}<PRChip {pr} short />{:else}<span class="turn">{m.state === 'live' ? `turn ${m.turns}` : m.state === 'error' ? sessionStatus(m) : ago(m.lastActivity)}</span>{/if}
  </button>
{/snippet}

<aside class="side">
  {#if machinesOpen}
    <header class="head">
      <h1>Machines</h1>
      <p class="sub">{machinesSub(machines)}</p>
    </header>
    <div class="scroll">
      {#each machines ?? [] as m (m.id)}
        <div animate:flip={{ duration: motion(BASE), easing: cubicOut }} in:fade={{ duration: motion(FAST) }} out:fade={{ duration: motion(FAST) }}>
          <MachineRow machine={m} selected={machineId === m.id} onopen={() => onmachine?.(m.id)} />
        </div>
      {:else}
        <p class="empty">{machines ? 'No machines yet.' : 'Loading machines…'}</p>
      {/each}
    </div>
  {:else if room}
    <header class="head">
      {#if onroommenu}
        <button class="roombtn" onclick={(e) => { const b = e.currentTarget.getBoundingClientRect(); onroommenu(b.left + 8, b.bottom + 4, e.currentTarget) }} aria-haspopup="menu" aria-label="{roomLabel(room)}, room options" title="Room options">
          <h1>{roomLabel(room)}</h1>
          <svg viewBox="0 0 16 16" width="14" height="14" aria-hidden="true"><path d="m4 6 4 4 4-4" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" /></svg>
        </button>
      {:else}
        <h1>{roomLabel(room)}</h1>
      {/if}
      {#if room.repo}<p class="repo mono" title={room.repo}><bdi>{room.repo}</bdi></p>{/if}
    </header>
    <div class="scroll">
      <button class="chan" class:sel={view === GENERAL || view.startsWith('channel:')} class:unread={(viewUnread[GENERAL] ?? 0) > 0} title="Room chat" onclick={() => onview(GENERAL)}>
        <svg class="wglyph" viewBox="0 0 20 20" width="17" height="17" aria-hidden="true"><path d={icons.messages} fill="none" stroke="currentColor" stroke-width="1.6" stroke-linejoin="round" /></svg><span class="label">{ROOM_CHAT}</span>{#if viewUnread[GENERAL]}<span class="count">{viewUnread[GENERAL]}</span>{/if}
      </button>
      {#each sections as section (section.label)}
        {@const rows = shown(section.rows, { repo: !!room.repo, terminal, computers: !!computers })}
        {#if rows.length}
          <h2>{section.label}</h2>
          {#each rows as r (r.id)}
            <button class="chan" class:sel={open === r.id} title={r.hint} onclick={() => { const tab = r.tabs[0]; if (tab) onview(tab.id) }}>
              {#if r.id === 'goals'}<span class="wglyph"><GoalGlyph neutral /></span>{:else}<svg class="wglyph" viewBox="0 0 20 20" width="17" height="17" aria-hidden="true"><path d={icons[r.id]} fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" /></svg>
              {/if}<span class="label">{r.label}</span>
              {#if r.id === 'questions' && questions}<span class="count live" title="{questions} waiting for you">{questions}</span>{/if}
              {#if r.id === 'runs' && running}<span class="lv" title="{running} running">● {running}</span>{/if}
              {#if r.id === 'computers' && liveComputers}<span class="lv">{liveComputers} live</span>{/if}
            </button>
          {/each}
        {/if}
      {/each}
      {#if live.length}
        <h2>Working now</h2>
        {#each live as m (m.id)}<div animate:flip={{ duration: motion(BASE), easing: cubicOut }} in:fade={{ duration: motion(FAST) }} out:fade={{ duration: motion(FAST) }}>{@render person(m, false, () => onmember(m))}</div>{/each}
      {/if}
    </div>
  {/if}
  <footer class="me">
    <Avatar name="you" owner hue={ownerHue} size={32} ring="#18171a" />
    <div class="who">
      <strong>you</strong>
      <span title={dataDir}>{room ? `owner of ${roomLabel(room)}` : 'room owner'}</span>
      {#if route}<span class="route" title={route.better ? `A nearer route is open: ${route.better}` : ''}>via {route.label}{route.better ? ' · nearer: ' + route.better : ''}</span>{/if}
    </div>
    {#if onsettings}
    <button class="gear" onclick={onsettings} aria-label="Settings" title="Profiles and settings">
      <svg viewBox="0 0 24 24" width="20" height="20" aria-hidden="true"><path d="M12 15.2a3.2 3.2 0 1 0 0-6.4 3.2 3.2 0 0 0 0 6.4Z" fill="none" stroke="currentColor" stroke-width="1.8" /><path d="M19.4 13.5a7.6 7.6 0 0 0 0-3l2-1.6-2-3.4-2.4 1a7.4 7.4 0 0 0-2.6-1.5L14 2.5h-4l-.4 2.5A7.4 7.4 0 0 0 7 6.5l-2.4-1-2 3.4 2 1.6a7.6 7.6 0 0 0 0 3l-2 1.6 2 3.4 2.4-1a7.4 7.4 0 0 0 2.6 1.5l.4 2.5h4l.4-2.5a7.4 7.4 0 0 0 2.6-1.5l2.4 1 2-3.4Z" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linejoin="round" /></svg>
      {#if update}<span class="udot" aria-label="Update available"></span>{/if}
    </button>
    {/if}
    {#if onlogout}
    {#if features.push}<Notify />{/if}
    <button class="gear" onclick={onlogout} aria-label="Sign out this device" title="Sign out this device">
      <svg viewBox="0 0 24 24" width="20" height="20" aria-hidden="true"><path d="M14 4.5H6.5a2 2 0 0 0-2 2v11a2 2 0 0 0 2 2H14" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" /><path d="M11 12h9m-3.2-3.5L20.3 12l-3.5 3.5" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" /></svg>
    </button>
    {/if}
  </footer>
</aside>

<style>
  .side {
    width: 240px;
    flex: none;
    background: var(--side);
    display: flex;
    flex-direction: column;
    min-height: 0;
  }
  .head {
    height: 64px;
    padding: var(--s3) var(--s4) 10px;
    border-bottom: 1px solid #141315;
    box-shadow: 0 1px 0 rgba(0, 0, 0, 0.2);
    display: flex;
    flex-direction: column;
    justify-content: center;
    flex: none;
  }
  h1 {
    margin: 0;
    font-size: var(--t-title);
    font-weight: 600;
    line-height: var(--leading-tight);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .roombtn { display: flex; align-items: center; gap: 6px; width: calc(100% + 16px); margin: -4px -8px 0; padding: var(--s1) var(--s2); border-radius: 6px; color: var(--ink); text-align: left; transition: background-color var(--motion-fast) var(--ease); }
  .roombtn h1 { flex: 1; min-width: 0; }
  .roombtn svg { color: var(--muted); flex: none; }
  @media (hover: hover) {
    .roombtn:hover { background: var(--hover); }
  }
  .roombtn:active {
    background: var(--select);
}
  .repo {
    margin: 3px 0 0;
    font-size: var(--t-cap);
    color: var(--faint);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    direction: rtl;
    text-align: left;
  }
  .scroll { flex: 1; overflow-y: auto; padding: var(--s2) var(--s2) var(--s3); }
  h2 {
    margin: 14px var(--s2) var(--s1);
    font-size: var(--t-sm);
    font-weight: 600;
    color: var(--muted);
  }
  h2:first-child { margin-top: 6px; }
  .chan {
    width: 100%;
    display: flex;
    align-items: center;
    gap: var(--s2);
    padding: 6px var(--s2);
    margin: 1px 0;
    border-radius: 5px;
    color: var(--muted);
    text-align: left;
    transition: background-color var(--motion-fast) var(--ease), color var(--motion-fast) var(--ease);
  }
  @media (hover: hover) {
    .chan:hover { background: var(--hover); color: var(--ink); }
  }
  .chan:active {
    background: var(--select);
}
  .chan.sel { background: var(--select); color: var(--ink); }
  .wglyph { width: 16px; flex: none; color: var(--faint); }
  .chan.sel .wglyph { color: var(--muted); }
  .label { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-weight: 500; }
  .count {
    flex: none;
    margin-left: var(--s1);
    background: var(--ink);
    color: #18171a;
    font-size: var(--t-cap);
    font-weight: 700;
    border-radius: 9px;
    min-width: 18px;
    padding: 1px 5px;
    text-align: center;
  }
  .count.live { background: var(--gold); }
  .chan.unread .label { color: var(--ink); font-weight: 600; }
  .lv { font-size: var(--t-sm); color: var(--gold); white-space: nowrap; }
  .person { padding: 4px 8px 4px calc(8px + var(--depth) * 14px); }
  .person .label { font-weight: 600; }
  .sub { margin: 3px 0 0; font-size: var(--t-cap); color: var(--faint); }
  .empty { margin: var(--s2); font-size: var(--t-ui); color: var(--faint); }
  .turn { font-size: var(--t-sm); color: var(--faint); }
  .me {
    height: 56px;
    flex: none;
    background: #18171a;
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 0 var(--s3);
  }
  .who { display: flex; flex-direction: column; min-width: 0; line-height: var(--leading-tight); flex: 1; }
  .gear { position: relative; width: 32px; height: 32px; border-radius: 6px; display: grid; place-items: center; color: var(--muted); flex: none; transition: background-color var(--motion-fast) var(--ease), color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .gear:hover { background: var(--hover); color: var(--ink); }
  }
  .gear:active {
    background: var(--select);
}
  .udot { position: absolute; top: 5px; right: 5px; width: 8px; height: 8px; border-radius: 50%; background: var(--gold); box-shadow: 0 0 0 2px #18171a; }
  .who strong { font-size: var(--t-body); font-weight: 600; }
  .who .route { font-size: var(--t-cap); }
  .who span { font-size: var(--t-cap); color: var(--faint); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
</style>
