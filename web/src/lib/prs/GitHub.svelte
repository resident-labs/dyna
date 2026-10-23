<script lang="ts">
  import Segmented from '../ui/Segmented.svelte'
  import LoadOlder from '../ui/LoadOlder.svelte'
  // The room's pull requests and issues, as a list and one open item. What
  // Dyna tracks and untracked PRs share one open-only list. On a wide screen the list stays beside the open item; on a
  // narrow one the open item takes the screen and a back link returns.
  // An open PR has its story and its tabs (PRDetail). Opening a PR adds a
  // history entry with its link (prlink.js), so Back returns to the list.
  // Issues come from GitHub through gh on the host, grouped by who is on them
  // (IssueList) and opened as an IssuePage.
  import { onMount } from 'svelte'
  import { api } from '../../api.js'
  import RoomPRList from './RoomPRList.svelte'
  import MergingStrip from './MergingStrip.svelte'
  import { openPRs } from './roomPRs.ts'
  import TabHeader from '../rooms/TabHeader.svelte'
  import PRDetail from './PRDetail.svelte'
  import IssueList from './IssueList.svelte'
  import { unifiedIssues, type RoomIssue, type IssueRow, type IssueReference } from './unifiedIssues.ts'
  import { location as issueLocation } from '../rooms/issues.ts'
  import IssuePage from './IssuePage.svelte'
  import Menu from '../ui/Menu.svelte'
  import { changeTitle, isLocal, withoutReviewers } from './pr.ts'
  import { listView, rowsFromGitHub, moveKey, type Names, type Row } from './prlist.ts'
  import { neighbor } from './prboard.ts'
  import { prHash, parsePRHash, prRef, findPR, lookupNumber, issueHash, parseIssueHash, type Link } from './prlink.ts'
  import { roomHash } from '../ui/route.ts'
  import { trackedFor, loadFilters, saveFilters } from './github.ts'
  import type { MergeQueueItem, MergeControlStatus, ReviewerStatus } from '../api/change.ts'
  import type { Goal } from '../api/goal.ts'
  import type { GitHubDetail, GitHubItem, GitHubListResult } from '../api/github.ts'
  import type { Integration } from '../api/lanesync.ts'
  import type { Member } from '../api/room.ts'
  import { errorText } from '../errorText.ts'
  import type { ChangeAction } from './props.ts'

  // One entry of the track menu: the sessions that can take a PR.
  interface TrackItem {
    label: string
    hint?: string
    run: () => void
  }
  interface TrackMenu {
    x: number
    y: number
    anchor: HTMLElement | null
    items: TrackItem[]
  }
  interface Props {
    onmention?: (reference: IssueReference) => void
    localIssues?: RoomIssue[]
    oncloseissue?: (issue: RoomIssue) => void
    onexportissue?: (issue: RoomIssue) => void
    ondismississue?: (issue: RoomIssue) => void
    mergeVersion?: number
    older?: boolean
    loadingOlder?: boolean
    failedOlder?: boolean
    onolder?: () => void
    roomId: string
    kind?: string
    integration?: Integration | null
    changes?: Row[] | null
    members?: Member[]
    link?: Link | null
    onsession?: (member: Member, tab?: string) => void
    onpr: (number: number) => void
    ontrack?: (number: number | undefined, member: Member) => void
    onuntrack?: ChangeAction
    onresume?: ChangeAction
    reviewer?: ReviewerStatus | null
    onreviewer?: () => void
    goals?: Goal[]
    onexpedite?: (goal: Goal) => void | Promise<void>
    onlinkgoal?: (change: Row, goalId: string) => void | Promise<void>
  }

  // changes: the room's Changes rows, local changes and PRs.
  // members: the room's sessions, to name and open author and reviewer
  // sessions. onmention adds a PR or issue to the composer.
  // kind is 'pr' or 'issue': the Pull requests tab and the Issues tab each mount this view.
  // onpr opens a PR by number in the Pull requests tab, from an issue's linked PRs.
  // link is a PR link ({pr, tab}) or issue link ({issue}) to open, from the address bar.
  let { onmention, localIssues = [], oncloseissue, onexportissue, ondismississue, mergeVersion = 0, older = false, loadingOlder = false, failedOlder = false, onolder = () => {}, roomId, kind = 'pr', integration = null, changes = null, members = [], link = null, onsession, onpr, ontrack, onuntrack, onresume, reviewer = null, onreviewer = () => {}, goals = [], onexpedite, onlinkgoal }: Props = $props()

  const saved = loadFilters()
  let listState = $state(kind === 'issue' ? 'open' : saved.state)
  let source = $state('all')
  let localSelected = $state<RoomIssue | null>(null)
  let selectedLocal = $derived(localIssues.find((i) => i.id === localSelected?.id) || localSelected)
  let localBody = $state('')
  let localBodyError = $state('')
  let localSequence = 0
  let selId = $state(findPR(changes, link?.pr ?? '')?.changeId ?? '') // the open row's changeId
  let tab = $state(link?.tab ?? 'conversation')
  let search = $state('')
  let query = $state('') // the search the list was loaded with
  let list = $state<GitHubListResult | null>(null)
  let error = $state('')
  let sel = $state(kind === 'issue' ? link?.issue ?? 0 : 0) // the open PR or issue number, for its GitHub thread
  let detail = $state<GitHubDetail | null>(null)
  let detailError = $state('')
  let trackMenu = $state<TrackMenu | null>(null)
  let linkedItem = $state<GitHubDetail | null>(null) // a linked PR the GitHub list does not hold
  let followed = $state<Link | null>(null) // the last address-bar link the screen opened
  let width = $state(1200) // this screen's own width, from its box
  let mergeStatus = $state<MergeControlStatus | null>(null)
  let mergeError = $state('')
  let resuming = $state(false)
  let resumeError = $state('')
  let merge = $state<MergeQueueItem[] | null>(null) // the room's RoomMergeStatus answer
  let started = $state<Member | null>(null) // the session a warned Start made, for its Open session button

  let identified = $derived(members.filter((m): m is Member & { id: string } => !!m.id))
  let byId: Record<string, Member> = $derived(Object.fromEntries(identified.map((m) => [m.id, m])))
  let names: Names = $derived(Object.fromEntries(identified.map((m) => [m.id, m.name])))
  let inPRs = $derived(kind === 'pr')
  let base = $derived(changes ?? [])
  let allRows = $derived.by(() => {
    const items = list?.items ?? []
    const linked = linkedItem
    const extra = linked && !items.some((it) => it.number === linked.number) ? [linked] : []
    return [...base, ...rowsFromGitHub([...items, ...extra], base, list?.repo)]
  })
  // Under 900 px the list and the open item take turns.
  let narrow = $derived(width < 900)
  let searched = $derived.by(() => {
    const q = search.trim().toLowerCase()
    return allRows.filter((c) => !q || `${changeTitle(c)} ${c.branch || ''} ${c.number || ''}`.toLowerCase().includes(q))
  })
  let listRows = $derived(openPRs(searched, merge ?? []))
  let view = $derived(listView(listRows, merge))
  let anyRows = $derived(listRows.length > 0)
  let chg = $derived(inPRs ? allRows.find((c) => c.changeId === selId) ?? null : null)
  let chgAuthor = $derived(chg?.authorSession ? byId[chg.authorSession] : null)
  let chgReviewer = $derived(chg?.reviewerSession ? byId[chg.reviewerSession] : null)
  let rowItems = $derived(list?.items ?? [])
  let issueRows = $derived(unifiedIssues(rowItems, localIssues, source, listState, query))
  async function pickIssue(it: IssueRow) {
    localSelected = it.number ? null : it.local || null
    localBody = ''; localBodyError = ''
    const n = ++localSequence
    if (!localSelected) { openIssue(it.number); return }
    pick(0)
    history.replaceState(null, '', listAddress())
    const issue = localSelected
    try {
      const feed = await api.feedAround(roomId, issue.id, 1)
      if (n === localSequence) localBody = feed.posts?.find((p: { id: string }) => p.id === issue.id)?.text || ''
    } catch (e) { if (n === localSequence) localBodyError = errorText(e) }
  }
  let item = $derived(detail ?? rowItems.find((it) => it.number === sel) ?? null)
  let workSession = $derived(item?.workingSession ? byId[item.workingSession] : null)
  // Keyboard and swipe navigation follow exactly the visible open list.
  let order = $derived(listRows)

  let seq = 0
  async function load() {
    const n = ++seq
    try {
      const res = await api.githubList(roomId, kind, kind === 'pr' ? 'open' : listState, query)
      if (n !== seq) return
      list = res
      error = ''
    } catch (e) {
      if (n === seq) error = errorText(e)
    }
  }

  // Export changes the local row before GitHub's list contains the new issue.
  let localVersion: string | null = null
  $effect(() => {
    const version = localIssues.map((i) => i.id + ':' + i.state + ':' + (i.githubUrl || '')).join('|')
    if (localVersion !== null && localVersion !== version && !inPRs) load()
    localVersion = version
  })

  // The merge service's state and order for each open PR. It reads saved
  // facts only, so it reloads whenever a change moves.
  let mseq = 0
  async function loadMerge() {
    if (!inPRs) return
    const n = ++mseq
    try {
      const res = await api.mergeControlStatus(roomId) as MergeControlStatus
      if (n === mseq) { mergeStatus = res; merge = res.queue ?? []; mergeError = '' }
    } catch (e) {
      if (n === mseq) { mergeStatus = null; merge = null; mergeError = errorText(e) }
    }
  }
  async function resumeMerging() {
    if (resuming) return
    resuming = true
    resumeError = ''
    try {
      await api.resumeMerging(roomId)
      await loadMerge()
    } catch (e) {
      resumeError = errorText(e)
    } finally {
      resuming = false
    }
  }
  // Refresh for row changes and saved merge events, including main CI updates.
  let moved = $derived(moveKey(changes))
  $effect(() => {
    moved
    mergeVersion
    loadMerge()
  })

  // openRow opens a PR row and records it in the address bar: a click adds
  // a history entry, so Back returns; the j and k keys replace it.
  function openRow(c: Row, push = true) {
    selId = c.changeId ?? ''
    tab = 'conversation'
    const h = prHash(roomId, prRef(c))
    if (push) history.pushState({ pr: true }, '', h)
    else history.replaceState(history.state, '', h)
  }
  // A running trial can include a PR outside the loaded list page.
  function openMerge(id: string) {
    const row = allRows.find((r) => r.changeId === id)
    if (row) return openRow(row)
    const number = Number(id.match(/#(\d+)$/)?.[1])
    if (number) onpr(number)
  }
  // The list's own address, for the entry Back returns to.
  const listAddress = () => location.pathname + location.search + roomHash(roomId, inPRs ? 'github' : 'issues')
  function openIssue(n: number) {
    // A tab switch can leave a PR address behind. Back must return to this list.
    if (parseIssueHash(location.hash)?.issue !== sel) history.replaceState(null, '', listAddress())
    history.pushState({ issue: true }, '', issueHash(roomId, n))
    if (n !== sel) pick(n)
  }
  function backIssue() {
    if (history.state?.issue) return history.back()
    pick(0)
    history.replaceState(null, '', listAddress())
  }
  function showTab(t: string) {
    if (chg) history.replaceState(history.state, '', prHash(roomId, prRef(chg), t))
  }
  // back leaves the open PR: through history when the screen added the entry.
  function back() {
    if (history.state?.pr) return history.back()
    selId = ''
    history.replaceState(null, '', listAddress())
  }
  // The address bar leads: Back, Forward and a new link open what they name.
  function followLink(l: Link | null) {
    if (!inPRs) {
      localSelected = null
      localSequence++
      const n = l && (!l.roomId || l.roomId === roomId) ? l.issue ?? 0 : 0
      if (n !== sel) pick(n)
      return
    }
    const c = l && l.pr !== undefined && (!l.roomId || l.roomId === roomId) ? findPR(allRows, l.pr) : null
    if (c && l) {
      selId = c.changeId ?? ''
      tab = l.tab ?? 'conversation'
    } else if (!l) selId = ''
  }
  // A link from the address bar opens once, as soon as its row is known. A
  // linked PR outside the GitHub list (old or closed) loads by its number.
  let looked = 0
  $effect(() => {
    if (!inPRs) {
      if (link?.issue && link !== followed) {
        followed = link
        pick(link.issue)
      }
      return
    }
    if (!link || link === followed) return
    if (link.pr !== undefined && findPR(allRows, link.pr)) {
      followed = link
      followLink(link)
      return
    }
    const n = lookupNumber(allRows, link)
    if (!list || n === looked) return
    if (!n) {
      followed = link
      return
    }
    looked = n
    const l = link
    api.githubView(roomId, 'pr', n).then((d: GitHubDetail) => (linkedItem = d)).catch(() => (followed = l))
  })
  // The open PR row loads its GitHub thread.
  $effect(() => {
    if (!inPRs) return
    const n = chg && !isLocal(chg) && chg.number ? chg.number : 0
    if (n !== sel) pick(n)
  })
  // Opening the tab shows the full list; a PR opens only after a choice or link.
  let dseq = 0
  async function pick(n: number) {
    const d = ++dseq
    sel = n
    detail = null
    detailError = ''
    if (!n) return
    const k = kind
    try {
      const res = await api.githubView(roomId, k, n)
      if (d === dseq) detail = res
    } catch (e) {
      if (d === dseq) detailError = errorText(e)
    }
  }
  function setState(s: string) {
    listState = s
    saveFilters({ state: listState })
    load()
  }
  // The search runs on GitHub; wait for a pause in typing.
  let timer: ReturnType<typeof setTimeout> | undefined
  function typed() {
    clearTimeout(timer)
    timer = setTimeout(() => {
      if (search.trim() === query) return
      query = search.trim()
      load()
    }, 450)
  }
  function mentionPR() {
    if (chg) onmention?.({ kind: 'pr', number: chg.number || 0, title: changeTitle(chg), state: String(chg.state || 'OPEN'), url: chg.url || '' })
  }
  // Track and review: a PR that Dyna tracked before keeps its author session;
  // any other PR needs a session to wake.
  function trackRow(c: Row, e: MouseEvent) {
    const w = trackedFor(base, c.repo || list?.repo, c.number)
    if (w?.authorSession && byId[w.authorSession]) return onresume?.(w)
    const target = e.currentTarget
    if (!(target instanceof HTMLElement)) return
    const b = target.getBoundingClientRect()
    const open = withoutReviewers(members, reviewer).filter((m) => m.id && !m.closed && !m.owner)
    trackMenu = {
      x: b.left,
      y: b.bottom + 4,
      anchor: target,
      items: open.length
        ? open.map((m) => ({ label: m.name, hint: m.profile || '', run: () => ontrack?.(c.number, m) }))
        : [{ label: 'No open session in this room', run: () => {} }],
    }
  }
  // j and k move to the next and the previous PR, outside of text fields.
  function keys(e: KeyboardEvent) {
    if (!inPRs || e.ctrlKey || e.metaKey || e.altKey || trackMenu) return
    if (e.target instanceof Element && e.target.closest('input, textarea, select, [contenteditable], [role="dialog"]')) return
    const step = e.key === 'j' ? 1 : e.key === 'k' ? -1 : 0
    const next = step && neighbor(order, selId, step)
    if (!next) return
    e.preventDefault()
    openRow(next, false)
  }
  // On a narrow screen a swipe across the PR body opens its neighbor.
  // The tab bar keeps its own horizontal scroll gesture.
  let swipeX: number | null = null
  const swipeStart = (e: TouchEvent) => {
    const target = e.target
    swipeX = target !== null && 'closest' in target && typeof target.closest === 'function' && target.closest('[role="tablist"]') ? null : (e.changedTouches[0]?.clientX ?? null)
  }
  function swipeEnd(e: TouchEvent) {
    const end = e.changedTouches[0]
    if (swipeX === null || !end) return
    const dx = end.clientX - swipeX
    const next = Math.abs(dx) > 70 && neighbor(order, selId, dx < 0 ? 1 : -1)
    if (next) openRow(next, false)
  }
  onMount(() => {
    load()
    // Main CI moves no row, so a return to the window also reloads the merge state.
    const again = () => document.visibilityState === 'visible' && (load(), loadMerge())
    const popped = () => followLink(inPRs ? parsePRHash(location.hash) : parseIssueHash(location.hash))
    window.addEventListener('focus', again)
    window.addEventListener('popstate', popped)
    window.addEventListener('hashchange', popped)
    return () => {
      seq++
      dseq++
      mseq++
      window.removeEventListener('focus', again)
      window.removeEventListener('popstate', popped)
      window.removeEventListener('hashchange', popped)
      clearTimeout(timer)
    }
  })
  const STATES = ['open', 'closed', 'all']
</script>
{#snippet filters()}
  <header class="lhead">
    {#if !inPRs}<div class="filters">
      <Segmented small label="Source" options={[['all', 'All'], ['dyna', 'Dyna'], ['github', 'GitHub']]} value={source} onchange={(value: string) => source = value} />
      <Segmented small label="State" options={STATES.map((s) => [s, s.charAt(0).toUpperCase() + s.slice(1)])} value={listState} onchange={setState} />
    </div>{/if}
    <input class="search" type="search" bind:value={search} oninput={typed} onkeydown={(e) => e.key === 'Enter' && (clearTimeout(timer), (query = search.trim()), load())} placeholder="Search, for example author:@me label:bug" aria-label="Search {inPRs ? 'pull requests' : 'issues'}" />
  </header>
{/snippet}
{#snippet mentionAction()}
  {#if onmention && !isLocal(chg) && chg?.url}<button class="act" onclick={mentionPR} title="Add this PR to a chat message">Mention in chat</button>{/if}
{/snippet}
<svelte:window onkeydown={keys} />
<section class="gh" class:narrow class:pr-list={inPRs ? !chg : !sel && !localSelected} bind:clientWidth={width}>
  {#if inPRs}
      <div class="list prlist" hidden={narrow && !!chg}>
        <TabHeader title="Pull requests" usage={`${listRows.length} open`} />
        <MergingStrip status={mergeStatus} error={mergeError} onpick={openMerge} onresume={resumeMerging} {resuming} {resumeError} />
        {@render filters()}
        {#if error}<p class="more err">GitHub did not answer: {error}</p>{:else if list?.more}<p class="more">Showing the first {list.items?.length ?? 0} from GitHub. Search to narrow the list.</p>{/if}
        {#if anyRows}
          <RoomPRList rows={listRows} queue={merge ?? []} {selId} {names} {older} {loadingOlder} {failedOlder} {onolder} onpick={(c) => openRow(c)} />
        {:else}
          <div class="empty">
            <strong>No pull requests</strong>
            <p>{search.trim() ? 'Nothing matches this search.' : 'Open pull requests in this room appear here.'}</p>
          </div>
          <LoadOlder auto {older} loading={loadingOlder} failed={failedOlder} onclick={onolder} />
        {/if}
      </div>
    {#if chg}
      <div class="dscroll" role="presentation" ontouchstart={narrow ? swipeStart : null} ontouchend={narrow ? swipeEnd : null}>
        {#key chg.changeId}
          <PRDetail change={chg} {detail} {detailError} {roomId} {names} review={reviewer?.enabled ?? true} merge={merge?.find((m) => m.changeId === chg.changeId) ?? null} standing={view.states[chg.changeId ?? '']} bind:tab ontab={showTab} onback={back} {onuntrack} {onresume} ontrack={trackRow} onchange={onreviewer} onauthor={chgAuthor ? () => onsession?.(chgAuthor, 'prs') : null} onreviewer={chgReviewer ? () => onsession?.(chgReviewer) : null} actions={mentionAction} {goals} {onexpedite} {onlinkgoal} />
        {/key}
      </div>
    {/if}
  {:else}
    {#if !narrow || (!sel && !localSelected)}
      <div class="list" role="presentation">
        <TabHeader title="Issues" usage={`${issueRows.length} ${listState === 'all' ? 'total' : listState}`} />
        {@render filters()}
        {#if error && source !== 'dyna'}<p class="quiet err">GitHub did not answer: {error}</p>{/if}
        {#if !list && !error && source !== 'dyna'}<p class="quiet">Asking GitHub…</p>{/if}
        {#if issueRows.length}
          <IssueList groups={['open', 'closed'].map((id) => ({ id, label: id, rows: issueRows.filter((i) => (i.state === 'OPEN') === (id === 'open')) }))} {sel} {onmention} onpick={pickIssue}
            oncloseissue={oncloseissue ? (it) => it.local && oncloseissue(it.local) : undefined} onexport={onexportissue ? (it) => it.local && onexportissue(it.local) : undefined} ondismiss={ondismississue ? (it) => it.local && ondismississue(it.local) : undefined} />
          {#if list?.more && source !== 'dyna'}<p class="more">Showing the first {list.items?.length ?? 0} from GitHub. Search to narrow the list.</p>{/if}
        {:else if list || error || source === 'dyna'}
          <div class="empty"><strong>No issues</strong><p>{query ? 'Nothing matches this search.' : 'No issues match these filters.'}</p></div>
        {/if}
      </div>
    {/if}
    {#if selectedLocal}
      <div class="dscroll local-detail">
        <button class="act" onclick={() => { localSelected = null; localSequence++ }}>Issues</button>
        <h2>{selectedLocal.title}</h2>
        {#if onmention}<button class="act" onclick={() => onmention?.({ kind: 'room_issue', number: selectedLocal.id, title: selectedLocal.title, state: selectedLocal.state, url: `#room=${encodeURIComponent(roomId)}&view=issues` })}>Mention in chat</button>{/if}
        <p>Dyna · {selectedLocal.filer} · {selectedLocal.state}</p>
        {#if issueLocation(selectedLocal)}<p>{issueLocation(selectedLocal)}</p>{/if}
        {#if localBodyError}<p class="err">{localBodyError}</p>{/if}
        <div class="local-body">{localBody}</div>
        {#if selectedLocal.reason}<p>{selectedLocal.reason}</p>{/if}
        {#if selectedLocal.goalId}<p>Goal {selectedLocal.goalId}</p>{/if}
        {#if selectedLocal.githubUrl}<a href={selectedLocal.githubUrl} target="_blank" rel="noreferrer">Open on GitHub ↗</a>{/if}
      </div>
    {:else if sel}
      <div class="dscroll">
        {#if item}
        {#key sel}
          <IssuePage it={item} {detail} {detailError} {onmention} onsession={workSession ? () => onsession?.(workSession) : null} {onpr} onback={narrow ? backIssue : null} />
        {/key}
        {:else}
          <div class="detail">
            <button class="act" onclick={backIssue}>Issues</button>
            {#if detailError}<p class="quiet err">{detailError}</p>{:else}<p class="quiet">Loading #{sel}…</p>{/if}
          </div>
        {/if}
      </div>
    {:else if !narrow && list?.items?.length}
      <div class="detail none"><p class="quiet">Pick an issue to read it.</p></div>
    {/if}
  {/if}
</section>

{#if trackMenu}
  <Menu x={trackMenu.x} y={trackMenu.y} title="Track with which session?" items={trackMenu.items} onclose={() => { const a = trackMenu?.anchor; trackMenu = null; a?.focus?.() }} />
{/if}

<style>
  .local-detail { padding: var(--s5); overflow-wrap: anywhere; }
  .local-body { white-space: pre-wrap; margin: var(--s4) 0; }
  .gh { flex: 1; display: flex; min-height: 0; min-width: 0; }
  .list { width: 420px; flex: none; display: flex; flex-direction: column; border-right: 1px solid var(--line-soft); min-height: 0; }
  .prlist { overflow-y:auto; }
  .pr-list .list { width:100%; flex:1; border-right:0; }
  .list[hidden] { display: none; }
  .narrow .list { width: auto; flex: 1; min-width: 0; border-right: 0; }
  .lhead { flex: none; padding: 12px 12px 8px; display: flex; flex-direction: column; gap: 8px; border-bottom: 1px solid var(--line-soft); }
  .filters { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; }
  .search { width: 100%; font-size: 13px; padding: 7px 10px; border-radius: 7px; background: var(--raise); border: 1px solid var(--line-soft); color: var(--ink); }
  .search:focus { border-color: var(--iris-edge); outline: none; }
  .more { color: var(--faint); font-size: 12px; text-align: center; margin: 10px 0 0; }
  .detail { flex: 1; display: flex; flex-direction: column; min-width: 0; min-height: 0; }
  .detail.none { align-items: center; justify-content: center; }
  .act { font-size: 12.5px; font-weight: 600; color: var(--ink-2); padding: 6px 11px; border-radius: 6px; background: var(--raise); white-space: nowrap; }
  .act:hover { color: var(--ink); background: var(--raise-hover); }
  .dscroll { flex: 1; min-width: 0; overflow-y: auto; }
  .empty { margin: 28px 14px; text-align: center; background: var(--side); border: 1px solid var(--line-soft); border-radius: 12px; padding: 20px; }
  .empty p { color: var(--muted); font-size: 13.5px; margin: 6px 0 0; overflow-wrap: anywhere; }
  .quiet { color: var(--faint); font-size: 13.5px; padding: 12px 16px; }
  .err { color: var(--coral); padding: 12px 2px; overflow-wrap: anywhere; }
  @media (max-width: 760px) {
    .lhead { padding: 10px 10px 8px; }
    .search { font-size: 16px; padding: 10px 12px; }
    .act { padding: 10px 12px; }
  }
</style>
