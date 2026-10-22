<script lang="ts">
  // One open pull request, as on GitHub: the title, its state and one merge
  // state chip, the branch line and labels, then its tabs: the conversation
  // (with the reviewer's rounds inline and the merge box at the end), the
  // commits, the checks and the changed files. The list beside it or a back link leads out.
  //
  // Props:
  //   change     the open row; detail its GitHubView answer, detailError
  //   roomId, names, review (automatic review on in the room), merge (its RoomMergeStatus item), standing (its mergeState)
  //   tab        the open tab (bindable); ontab runs after a tab change
  //   onback     the back link, on a narrow screen only
  //   onuntrack, onresume, ontrack, onchange: the row actions
  //   onauthor, onreviewer: open those sessions from the merge box, or null
  //   actions    extra footer buttons
  import { tick } from 'svelte'
  import { ago } from '../ui/format.js'
  import MergeBox from './MergeBox.svelte'
  import PRChip from './PRChip.svelte'
  import ExpediteBadge from '../rooms/ExpediteBadge.svelte'
  import { expedite, goalExpedited, prExpedited } from '../rooms/expedite.ts'
  import type { Goal } from '../api/goal.ts'
  import PRGlyph from './PRGlyph.svelte'
  import PROverlaps from './PROverlaps.svelte'
  import PRChecks from './PRChecks.svelte'
  import PRCommits from './PRCommits.svelte'
  import PRFiles from './PRFiles.svelte'
  import Conversation from './Conversation.svelte'
  import ChangeDetail from './ChangeDetail.svelte'
  import ReviewDrawer from './ReviewDrawer.svelte'
  import { authorName, factsLine, isBotAuthor, botKey, mergeBox, mergeState, type MergeItem, type MergeState, type Names, type Row } from './prlist.ts'
  import { changeName, changeTitle, isLocal, type CheckRow } from './pr.ts'
  import { sortThread, timeline, itemState, labelStyle } from './github.ts'
  import { openExternal } from '../../api.js'
  import type { Snippet } from 'svelte'
  import type { BotReview, ChangeDetail as Detail } from '../api/change.ts'
  import type { GitHubDetail } from '../api/github.ts'
  import type { ChangeAction, TrackAction } from './props.ts'

  interface Props {
    change: Row
    detail?: GitHubDetail | null
    detailError?: string
    roomId: string
    names?: Names
    review?: boolean
    merge?: MergeItem | null
    standing?: MergeState | null
    tab?: string
    ontab?: (tab: string) => void
    onback?: (() => void) | null
    onuntrack?: ChangeAction | null
    onresume?: ChangeAction | null
    ontrack?: TrackAction | null
    onchange?: () => void | Promise<void>
    onauthor?: ChangeAction | null
    onreviewer?: (() => void) | null
    actions?: Snippet | null
    goals?: Goal[]
    onexpedite?: ((goal: Goal) => void | Promise<void>) | null
    onlinkgoal?: ((change: Row, goalId: string) => void | Promise<void>) | null
  }
  let { change, detail = null, detailError = '', roomId, names = {}, review = true, merge = null, standing = null, tab = $bindable('conversation'), ontab = () => {}, onback = null, onuntrack = null, onresume = null, ontrack = null, onchange = () => {}, onauthor = null, onreviewer = null, actions = null, goals = [], onexpedite = null, onlinkgoal = null }: Props = $props()
  let linking = $state(false)
  let pick = $state('')
  let xpBusy = $state(false)
  let xpError = $state('')
  let linked = $derived(goals.find((g) => g.id === change.goalId && g.state === 'active'))
  let candidates = $derived(goals.filter((g) => g.state === 'active'))
  // The button uses the linked goal; a change with none first links one.
  async function expediteChange(goal: Goal | undefined, link = '') {
    xpBusy = true; xpError = ''
    try {
      if (link) await onlinkgoal?.(change, link)
      const g = goal ?? goals.find((x) => x.id === link)
      // Linking to the current priority preserves it; the separate button can still stop it.
      if (g && (!link || !goalExpedited($expedite, g))) await onexpedite?.(g)
      linking = false
    } catch (e) { xpError = String((e as Error)?.message || e) } finally { xpBusy = false }
  }
  let changeDetail = $state<Detail | null>(null)
  let drawer = $state(false)
  let conversation = $state<{ reveal: () => Promise<void> } | null>(null)
  // PRFiles stays mounted once opened, so a tab change keeps its diff and open files.
  // tab is bindable, so links and Back change it without show(); the effect catches those.
  let filesSeen = $state(tab === 'files')
  $effect(() => { if (tab === 'files') filesSeen = true })

  let thread = $derived(detail ? sortThread(detail) : [])
  let talk = $derived(thread.filter((t) => !isBotAuthor(t.author)))
  // The reviews and comments bots left. Bots never block, so they sit in
  // the Checks tab and not in the conversation.
  let bots = $derived.by(() => {
    const seen = new Set((change.botReviews ?? []).map((b) => botKey(b.bot)))
    const extra = thread.filter((t) => isBotAuthor(t.author) && !seen.has(botKey(t.author.login)))
    const names = [...new Set(extra.map((t) => t.author.login))]
    return [...(change.botReviews ?? []), ...names.map((n): BotReview => ({ bot: n, state: 'commented', findings: 0, url: extra.findLast((t) => t.author.login === n)?.url || '' }))]
  })
  // A tracked change keeps its own checks; a PR Dyna does not watch shows GitHub's.
  let checks: CheckRow[] = $derived(change.checks?.length ? change.checks : (detail?.checks ?? []).map((c) => ({ name: c.workflow ? `${c.workflow} / ${c.name}` : c.name, state: c.result, url: c.url })))
  let size = $derived(detail?.changedFiles ? { files: detail.changedFiles, additions: detail.additions, deletions: detail.deletions } : null)
  let rounds = $derived(changeDetail?.reviewRounds ?? [])
  let events = $derived(timeline(talk, rounds))
  let number = $derived(change.kind !== 'local' ? (change.number ?? 0) : 0)
  let merged = $derived(standing ?? mergeState(change, merge))
  let box = $derived(mergeBox({ ...change, checks }, { merge, state: merged, review, names }))
  let gh = $derived(itemState('pr', change))
  let author = $derived(authorName(change, names))
  let labels = $derived(detail?.labels ?? [])

  function show(t: string) {
    tab = t
    ontab(t)
  }

  // "Read the review" shows the latest round in the conversation.
  async function readReview() {
    show('conversation')
    await tick()
    conversation?.reveal()
  }
</script>

<div class="detail">
  {#if onback}
    <button class="back" onclick={onback}>
      <svg viewBox="0 0 20 20" width="18" height="18" aria-hidden="true"><path d="M12.5 4.5 7 10l5.5 5.5" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" /></svg>
      Pull requests
    </button>
  {/if}
  <header class="head">
    <div class="title">
      <h1>{changeTitle(change)} <span class="num">{changeName(change)}</span></h1>
      {#if !isLocal(change) && change.url}<button class="ghb" onclick={() => openExternal(change.url)} title="Open on GitHub">GitHub ↗</button>{/if}
    </div>
    <p class="meta">
      {#if !isLocal(change)}<span class="badge {gh.tone}"><PRGlyph it={change} size={14} />{gh.label}</span>{/if}
      {#if change.state === 'open'}<PRChip pr={change} meta={merged} number={false} badge={false} />{/if}
      {#if prExpedited($expedite, change)}<ExpediteBadge />{/if}
      <span class="facts">{factsLine(change, { byline: false, size, ago })}</span>
      {#each labels as l (l.name)}<span class="ghlabel" style={labelStyle(l.color)}>{l.name}</span>{/each}
    </p>
    {#if change.state === 'open' && onexpedite}
      <div class="xp">
        {#if linked && goalExpedited($expedite, linked)}
          <button class="btn" disabled={xpBusy} onclick={() => expediteChange(linked)}>Stop expediting</button>
          <span class="quiet">Goal {linked.id} has priority.</span>
        {:else if linked}
          <button class="btn" disabled={xpBusy} onclick={() => expediteChange(linked)}>Expedite</button>
          <span class="quiet">Expedites goal {linked.id}, and every PR linked to it.</span>
        {:else if !linking}
          <button class="btn" onclick={() => (linking = true)}>Expedite</button>
        {:else}
          <label class="quiet" for="xp-goal">This change has no goal. Link it to one to expedite it.</label>
          <select id="xp-goal" bind:value={pick}>
            <option value="">Choose a goal</option>
            {#each candidates as g (g.id)}<option value={g.id}>{g.id} · {g.text.slice(0, 60)}</option>{/each}
          </select>
          <button class="btn" disabled={xpBusy || !pick} onclick={() => expediteChange(undefined, pick)}>Link and expedite</button>
          <button class="btn" onclick={() => (linking = false)}>Cancel</button>
        {/if}
        {#if xpError}<span class="err" role="alert">{xpError}</span>{/if}
      </div>
    {/if}
  </header>
  <ChangeDetail row={change} {roomId} bind:detail={changeDetail}>
    {#snippet children(full, fullError, fullLoading, refresh)}
      <div class="tabs" role="tablist">
        <button role="tab" aria-selected={tab === 'conversation'} class:on={tab === 'conversation'} onclick={() => show('conversation')}>Conversation<span>{events.length}</span></button>
        {#if number}<button role="tab" aria-selected={tab === 'commits'} class:on={tab === 'commits'} onclick={() => show('commits')}>Commits<span>{detail?.commits?.length || ''}</span></button>{/if}
        <button role="tab" aria-selected={tab === 'checks'} class:on={tab === 'checks'} onclick={() => show('checks')}>Checks<span>{checks.length + bots.length}</span></button>
        {#if number}<button role="tab" aria-selected={tab === 'files'} class:on={tab === 'files'} onclick={() => show('files')}>Files changed<span>{detail?.changedFiles || detail?.files?.length || ''}</span></button>{/if}
      </div>
      <!-- PRFiles stays mounted (hidden) outside the keyed panel, so the diff
           survives tab changes; its CSS fade restarts each time it shows. -->
      {#if filesSeen && number}<div class="tabpanel" hidden={tab !== 'files'}><PRFiles {roomId} {number} changeId={change.state === 'open' ? change.changeId : ''} author={author} onsent={() => { refresh(); return onchange() }} /></div>{/if}
      {#key tab}<div class="tabpanel">
      {#if tab === 'commits' && number}
        {#if detailError}<p class="err">{detailError}</p>{:else if detail}<PRCommits commits={detail.commits} url={detail.url} />{:else}<p class="quiet">Loading #{number}…</p>{/if}
      {:else if tab === 'checks'}
        <PRChecks {checks} {bots} pr={change} main={merge?.main ?? null} />
      {:else if tab !== 'files' || !number}
        {#if detailError}<p class="err">{detailError}</p>{:else if number && !detail}<p class="quiet">Loading #{number}…</p>{/if}
        {#if fullError}<p class="err">{fullError}</p>{/if}
        <Conversation bind:this={conversation} {detail} {events} {bots} change={full} {checks} {names} opener={author} {roomId} onchange={() => { refresh(); return onchange() }} />
        <PROverlaps overlaps={change.overlaps} />
        <MergeBox {change} {box} onchecks={() => show('checks')} onread={readReview} {onresume} {ontrack} {onreviewer} {onauthor} onrules={() => (drawer = true)} {onuntrack} {actions} />
      {/if}
      </div>{/key}
    {/snippet}
  </ChangeDetail>
</div>

{#if drawer}
  <ReviewDrawer {roomId} change={changeDetail && changeDetail.key === change.key ? changeDetail : change} onclose={() => (drawer = false)} {onchange} />
{/if}

<style>
  .detail { padding: var(--s4) var(--s5) var(--s6); min-width: 0; }
  .back { display: inline-flex; align-items: center; gap: 4px; margin: 0 0 var(--s3) -6px; padding: 6px; font-size: 13px; font-weight: 600; color: var(--muted); border-radius: 6px; }
  .back:hover { color: var(--ink); background: var(--hover); }
  .title { display: flex; align-items: flex-start; gap: var(--s3); }
  h1 { flex: 1; min-width: 0; margin: 0; font-size: 22px; line-height: 1.25; font-weight: 700; overflow-wrap: anywhere; }
  h1 .num { color: var(--faint); font-weight: 500; }
  .ghb { flex: none; font-size: 12.5px; font-weight: 600; color: var(--ink-2); background: var(--raise); border-radius: 6px; padding: 5px 10px; }
  .ghb:hover { background: var(--raise-hover); color: var(--ink); }
  .meta { display: flex; flex-wrap: wrap; align-items: center; gap: var(--s2) var(--s3); margin: var(--s2) 0 0; font-size: var(--t-sm); color: var(--faint); }
  .badge { display: inline-flex; align-items: center; gap: 5px; font-weight: 600; font-size: 12px; padding: 2px 9px 2px 7px; border-radius: 11px; }
  .badge :global(.glyph) { margin: 0; }
  .badge.sage { color: var(--sage); background: rgba(151, 201, 149, 0.13); }
  .badge.iris { color: var(--iris); background: var(--iris-wash); }
  .badge.coral { color: var(--coral); background: rgba(239, 138, 128, 0.13); }
  .badge.faint { color: var(--muted); background: var(--raise); }
  .facts { overflow-wrap: anywhere; }
  .ghlabel { font-size: 11px; font-weight: 600; padding: 0 7px; border-radius: 9px; line-height: 17px; }
  .tabs { display: flex; gap: 2px; overflow-x: auto; border-bottom: 1px solid var(--line-soft); margin: var(--s4) 0 4px; }
  .tabs button { white-space: nowrap; font-size: 13px; font-weight: 600; color: var(--muted); padding: 8px 12px; border-bottom: 2px solid transparent; margin-bottom: -1px; display: flex; gap: 6px; align-items: baseline; transition: color var(--motion-fast) var(--ease), border-color var(--motion-fast) var(--ease); }
  .tabpanel { animation: tabfade var(--motion-fast) var(--ease); }
  @keyframes tabfade { from { opacity: 0; } }
  .tabs button span { font-size: 11.5px; color: var(--faint); font-weight: 500; }
  .tabs button.on { color: var(--ink); border-color: var(--iris); }
  .xp { display: flex; flex-wrap: wrap; align-items: center; gap: var(--s2); margin-top: var(--s3); font-size: var(--t-sm); }
  .xp .quiet, .xp .err { padding: 0; font-size: var(--t-sm); }
  .xp .btn { padding: 4px 10px; border-radius: 6px; background: var(--raise); color: var(--ink-2); font-weight: 600; }
  .xp .btn:hover:not(:disabled) { color: var(--ink); background: var(--raise-hover); }
  .xp .btn:disabled { opacity: .5; }
  .xp select { background: var(--field); color: var(--ink); border: 1px solid var(--line); border-radius: 5px; padding: 4px 6px; max-width: 280px; }
  .quiet { color: var(--faint); font-size: 13.5px; padding: 12px 2px; }
  .err { color: var(--coral); padding: 12px 2px; overflow-wrap: anywhere; }
  @media (max-width: 760px) {
    h1 { font-size: 18px; }
    .ghb { min-height: 44px; }
    .detail { padding: var(--s3) var(--s3) var(--s5); }
    .back { min-height: 44px; }
    .tabs button { padding: 12px 12px; }
  }
</style>
