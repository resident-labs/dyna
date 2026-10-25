<script lang="ts">
  import { heldComments, refreshHeldComments } from '../prs/heldComments.ts'
  import HeldCommentCard from '../prs/HeldCommentCard.svelte'
  import { onMount } from 'svelte'
  import { createReloadGate } from '../ui/reloadGate.ts'
  import { hasItemChange, patchListRows } from '../ui/listChanges.ts'
  import { api } from '../../api.ts'
  import type { Room, Question } from '../api/room.ts'
  import type { StreamEvent, ListEvent } from '../api/event.ts'
  import type { AccountsSnapshot } from '../api/account.ts'
  import type { HarnessSnapshot } from '../api/harness.ts'
  import type { Notice, UpdateNotice } from '../api/settings.ts'
  import type { RowChange } from '../ui/listChanges.ts'
  import type { UpdateRow } from './home.ts'
  import { ago, roomLabel } from '../ui/format.ts'
  import SubscriptionCard from '../settings/accounts/SubscriptionCard.svelte'
  import Card from '../ui/Card.svelte'
  import Row from '../ui/Row.svelte'
  import Chip from '../ui/Chip.svelte'
  import EmptyState from '../ui/EmptyState.svelte'
  import NoticeCard from './NoticeCard.svelte'
  import { accountRows, allClear, updateRows, rootQuestions } from './home.ts'
  // update: the dyna update notice ({current, latest}) or null.
  // The machine list owns the load card now: Home shows what needs the owner.
  interface Props {
    rooms?: Room[]
    roomsLoaded?: boolean
    update?: UpdateNotice | null
    onroom: (id: string, view?: string) => void
    onaccount: (id: string) => void
    onnewroom: () => void
    // onsettings opens a Settings pane, the place an owner notice sends you.
    onsettings: (pane: string) => void
  }
  let { rooms = [], roomsLoaded = false, update = null, onroom, onaccount, onnewroom, onsettings }: Props = $props()

  let usage = $state<AccountsSnapshot | null>(null)
  let runtimes = $state<HarnessSnapshot | null>(null)
  let notices = $state<Notice[]>([])
  let questionAlive = true
  let questions = $state<Record<string, Question[]>>({}) // room id -> questions
  let busy = $state<Record<string, boolean>>({}) // update row id -> true while it runs
  let results = $state<Record<string, { ok: boolean; text: string }>>({}) // update row id -> {ok, text}

  let accounts = $derived(accountRows(usage))
  let updates = $derived(updateRows(update, runtimes))
  let asked = $derived(rootQuestions(rooms, questions))
  let alertCount = $derived(accounts.alerts.length + notices.length + $heldComments.comments.length)
  // Set only by a successful load: a slow or failed source is not "empty".
  let questionsLoaded = $state(false)
  let noticesLoaded = $state(false)
  let clear = $derived($heldComments.loaded && allClear({ usage, runtimes, questionsLoaded, noticesLoaded, cards: accounts.cards.length, asked: asked.length, alerts: alertCount, updates: updates.length }))
  let firstRoom = $derived(roomsLoaded && rooms.length === 0)

  onMount(() => {
    refresh()
    loadNotices()
    refreshHeldComments()
    return () => { questionAlive = false }
  })

  // event takes App's live events: one stream feeds the whole app.
  export function event(ev: StreamEvent) {
    if (ev.kind === 'resync') refresh()
    if (ev.kind === 'notices' || ev.kind === 'resync') loadNotices()
    if (ev.kind === 'resync' || (ev.kind === 'questions' && !applyQuestionEvent(ev))) loadQuestions()
  }

  // refresh reloads the account and runtime snapshots. No live event says
  // they changed; App calls it when Settings, where they change, closes.
  // Reading Home uses saved usage. Explicit provider refresh stays in Settings.
  // A later load wins over an earlier one that answers late.
  let accountsRequest = 0
  export function refresh() {
    const request = ++accountsRequest
    api.accountsView('').then((s) => { if (request === accountsRequest) usage = s }).catch(() => {})
    api.privateRuntimes().then((s) => { if (request === accountsRequest) runtimes = s }).catch(() => {})
  }

  // A host without the notices API has no notices.
  // A failed refresh keeps the last notices and is not counted as loaded,
  // so Home never claims all clear over alerts it could not refresh.
  let noticesRequest = 0
  async function loadNotices() {
    const request = ++noticesRequest
    noticesLoaded = false
    try {
      const value = (await api.notices()) ?? []
      if (request === noticesRequest) { notices = value; noticesLoaded = true }
    } catch {}
  }
  // Only changes to active room membership refresh the grouped snapshot.
  let questionRooms: string | null = null
  const questionReload = createReloadGate(() => ({ room: 'home', visit: questionRooms }), () => questionAlive)
  function refreshQuestionRooms() {
    const active = rooms.filter((room) => !room.archived).map((room) => room.id).sort().join(',')
    if (active === questionRooms) return
    questionRooms = active
    questionsLoaded = false
    return loadQuestions()
  }
  $effect(() => { refreshQuestionRooms() })

  function loadQuestions() {
    return questionReload('questions', async (current) => {
      try {
        const value = (await api.questions('')) ?? {}
        if (current()) { questions = value; questionsLoaded = true }
      } catch {}
    })
  }
  function applyQuestionEvent(ev: ListEvent) {
    // ListEvent shares an unknown item type across lists. The questions event carries a Question.
    const questionChange = ev.change as RowChange<Question> | undefined
    if (!hasItemChange(questionChange)) return false
    const roomId = rooms.find((room) => room.id === ev.roomId && !room.archived)?.id
    if (roomId === undefined) return true
    questionReload.patch('questions', (current) => {
      if (!current()) return
      const rows = patchListRows(questions[roomId] ?? [], questionChange).filter((q) => q.state === 'open')
      questions = { ...questions, [roomId]: rows }
    })
    return true
  }
  async function dismiss(id: string) {
    notices = notices.filter((n) => n.id !== id)
    await api.dismissNotice(id).catch(() => {})
    loadNotices()
  }
  async function dismissAll() {
    const ids = notices.map((n) => n.id)
    notices = []
    await Promise.all(ids.map((id) => api.dismissNotice(id).catch(() => {})))
    loadNotices()
  }

  async function runUpdate(u: UpdateRow) {
    busy = { ...busy, [u.id]: true }
    try {
      if (u.id === 'dyna') {
        await api.updateDyna()
        results = { ...results, [u.id]: { ok: true, text: `Updated to ${u.latest}. Restart the app.` } }
      } else {
        runtimes = await api.installPrivateRuntime(u.id, u.latest)
      }
    } catch (e) {
      results = { ...results, [u.id]: { ok: false, text: String((typeof e === 'object' && e !== null && 'message' in e && e.message) || e) } }
    } finally {
      busy = { ...busy, [u.id]: false }
    }
  }

  // noticeAction is where the owner can go about a notice: the worktree disk
  // floor and the usage list live in Settings, and a sync split opens the room
  // whose repository it names. A kind with no place gets no action button.
  function noticeAction(n: Notice) {
    if (n.kind === 'disk') return { label: 'Open settings', run: () => onsettings('settings') }
    if (n.kind === 'provider') return { label: 'Open accounts', run: () => onsettings('accounts') }
    if (n.kind === 'sync') {
      const repo = n.sync?.repo ?? ''
      const room = rooms.find((r) => !r.archived && r.repo === repo)
      if (room) return { label: 'Open sync', run: () => onroom(room.id) }
    }
    return null
  }
</script>

<section class="home">
<div class="dash">
  {#if firstRoom}
    <EmptyState title="Create your first room">Choose a project folder for your agents and their work.<br /><button class="new-room" onclick={onnewroom}>New room</button></EmptyState>
  {/if}

  {#if accounts.cards.length}
    <div class="cards">
      {#each accounts.cards as group (group.provider)}
        <SubscriptionCard {group} compact onopen={(account) => onaccount(account.id)} />
      {/each}
    </div>
  {/if}

  <!-- Each card shows only when it has rows: an empty box is noise. -->
  {#if asked.length}
    <Card>
      {#each asked as q (q.key)}
        <Row dense text="Question from root: {q.text}" onclick={() => onroom(q.room.id, 'questions')}>
          {#snippet lead()}<span class="ico gold">?</span>{/snippet}
          {#snippet trail()}<Chip>{roomLabel(q.room)}</Chip>{/snippet}
        </Row>
      {/each}
    </Card>
  {/if}

  {#if alertCount}
    <Card title="Dyna alerts" count={alertCount}>
      {#snippet action()}{#if notices.length > 1}<button class="link" onclick={dismissAll}>Dismiss all</button>{/if}{/snippet}
      {#each accounts.alerts as a (a.id)}
        <Row dense text={a.text}>
          {#snippet lead()}<span class="ico coral">!</span>{/snippet}
          {#snippet trail()}<button class="btn" onclick={() => onaccount(a.id)}>{a.action}</button>{/snippet}
        </Row>
      {/each}
      {#each $heldComments.comments as comment (comment.roomId + ':' + comment.key + ':' + comment.id)}
        <HeldCommentCard {comment} />
      {/each}
      {#each notices as n (n.id)}
        <NoticeCard notice={n} action={noticeAction(n)} ondismiss={() => dismiss(n.id)} />
      {/each}
    </Card>
  {/if}

  {#if updates.length}
    <Card title="Updates" count={updates.length}>
      {#each updates as u (u.id)}
        <Row dense text={u.name}>
          {#snippet lead()}<span class="ico iris">{u.id === 'dyna' ? 'D' : '↓'}</span>{/snippet}
          {#snippet meta()}{@const result = results[u.id]}{#if result}<span class="sub" class:err={!result.ok}>{result.text}</span>{:else}<span class="ver mono">{u.current} → <b>{u.latest}</b></span>{/if}{/snippet}
          {#snippet trail()}<button class="btn iris" disabled={busy[u.id]} onclick={() => runUpdate(u)}>{busy[u.id] ? 'Updating…' : 'Update'}</button>{/snippet}
        </Row>
      {/each}
    </Card>
  {/if}

  {#if $heldComments.error}<p class="err" role="alert">Could not load held PR comments: {$heldComments.error}</p>{/if}
  {#if clear && !firstRoom}<EmptyState title="All clear">No questions, alerts or updates need you.</EmptyState>{/if}
</div>
</section>

<style>
  .home { flex: 1; overflow-y: auto; }
  .dash { padding: var(--s4); display: grid; gap: var(--s3); align-content: start; max-width: 1240px; }
  .cards { display: grid; grid-template-columns: repeat(auto-fill, minmax(220px, 1fr)); gap: var(--s3); }
  .new-room { padding: var(--s2) 0; color: var(--ink); font-weight: 500; text-decoration: underline; text-underline-offset: 3px; }
  .link { color: var(--faint); font-size: var(--t-sm); transition: color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .link:hover { color: var(--ink); text-decoration: underline; }
  }
  .sub { color: var(--sage); }
  .sub.err { color: var(--coral); }
  code { font-size: var(--t-sm); color: var(--ink-2); background: rgba(0, 0, 0, 0.22); padding: 1px 5px; border-radius: 4px; overflow-wrap: anywhere; }
  time { font-size: var(--t-cap); color: var(--faint); flex: none; }
  .ico { flex: none; width: 24px; height: 24px; border-radius: var(--r-xs); display: grid; place-items: center; font-size: var(--t-sm); font-weight: 700; background: var(--raise); color: var(--muted); }
  .ico.gold { color: var(--gold); background: var(--gold-wash-hover); }
  .ico.coral { color: var(--coral); background: rgba(239, 138, 128, 0.12); }
  .ico.iris { color: var(--iris); background: var(--iris-wash); }
  .ver { color: var(--faint); }
  .ver b { color: var(--sage); font-weight: 500; }
  .btn { flex: none; font-size: var(--t-sm); padding: var(--s1) 11px; border-radius: var(--r-xs); background: var(--raise); color: var(--ink-2); white-space: nowrap; transition: background-color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .btn:hover { background: var(--raise-hover); }
  }
  .btn:active { background: var(--select); }
  .btn.iris { background: var(--iris-wash); color: var(--iris); box-shadow: inset 0 0 0 1px var(--iris-edge); }
  .btn:disabled { opacity: 0.6; }
  .x { flex: none; color: var(--faint); font-size: var(--t-lead); padding: 0 var(--s1); transition: color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .x:hover { color: var(--ink); }
  }
</style>
