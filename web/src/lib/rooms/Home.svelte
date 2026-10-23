<script>
  import { heldComments, refreshHeldComments } from '../prs/heldComments.ts'
  import HeldCommentCard from '../prs/HeldCommentCard.svelte'
  import { onMount } from 'svelte'
  import { createReloadGate } from '../ui/reloadGate.ts'
  import { hasItemChange, patchListRows } from '../ui/listChanges.ts'
  import { api } from '../../api.js'
  import { ago, roomLabel } from '../ui/format.ts'
  import SubscriptionCard from '../settings/usage/SubscriptionCard.svelte'
  import Card from '../ui/Card.svelte'
  import Row from '../ui/Row.svelte'
  import Chip from '../ui/Chip.svelte'
  import EmptyState from '../ui/EmptyState.svelte'
  import SystemLoadCard from './SystemLoadCard.svelte'
  import { pollLoad } from './systemLoad.js'
  import { accountRows, allClear, updateRows, rootQuestions } from './home.js'
  // update: the dyna update notice ({current, latest}) or null.
  let { rooms = [], update = null, onroom, onaccount, onagent } = $props()

  let usage = $state(null)
  let load = $state(null)
  let runtimes = $state(null)
  let notices = $state([])
  let questionAlive = true
  let questions = $state({}) // room id -> questions
  let busy = $state({}) // update row id -> true while it runs
  let results = $state({}) // update row id -> {ok, text}

  let accounts = $derived(accountRows(usage))
  let updates = $derived(updateRows(update, runtimes))
  let asked = $derived(rootQuestions(rooms, questions))
  let alertCount = $derived(accounts.alerts.length + notices.length + $heldComments.comments.length)
  // Set only by a successful load: a slow or failed source is not "empty".
  let questionsLoaded = $state(false)
  let noticesLoaded = $state(false)
  let clear = $derived($heldComments.loaded && allClear({ usage, runtimes, questionsLoaded, noticesLoaded, cards: accounts.cards.length, asked: asked.length, alerts: alertCount, updates: updates.length }))

  onMount(() => {
    refresh()
    loadNotices()
    refreshHeldComments()
    const stopLoad = pollLoad(() => api.systemLoad(), (v) => { load = v })
    return () => { questionAlive = false; stopLoad() }
  })

  // event takes App's live events: one stream feeds the whole app.
  export function event(ev) {
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
    api.accountUsageCached().then((s) => { if (request === accountsRequest) usage = s }).catch(() => {})
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
  let questionRooms = null
  const questionReload = createReloadGate(() => ({ room: 'home', visit: questionRooms }), () => questionAlive)
  function refreshQuestionRooms() {
    const active = rooms.filter((room) => !room.archived).map((room) => room.id).sort().join(',')
    if (active === questionRooms) return
    questionRooms = active
    questionsLoaded = false
    return loadQuestions()
  }
  $effect(refreshQuestionRooms)

  function loadQuestions() {
    return questionReload('questions', async (current) => {
      try {
        const value = (await api.questions('')) ?? {}
        if (current()) { questions = value; questionsLoaded = true }
      } catch {}
    })
  }
  function applyQuestionEvent(ev) {
    if (!hasItemChange(ev.change)) return false
    if (!rooms.some((room) => room.id === ev.roomId && !room.archived)) return true
    questionReload.patch('questions', (current) => {
      if (!current()) return
      const rows = patchListRows(questions[ev.roomId] ?? [], ev.change).filter((q) => q.state === 'open')
      questions = { ...questions, [ev.roomId]: rows }
    })
    return true
  }
  async function dismiss(id) {
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

  async function runUpdate(u) {
    busy = { ...busy, [u.id]: true }
    try {
      if (u.id === 'dyna') {
        await api.updateDyna()
        results = { ...results, [u.id]: { ok: true, text: `Updated to ${u.latest}. Restart the app.` } }
      } else {
        runtimes = await api.installPrivateRuntime(u.id, u.latest)
      }
    } catch (e) {
      results = { ...results, [u.id]: { ok: false, text: String(e?.message || e) } }
    } finally {
      busy = { ...busy, [u.id]: false }
    }
  }

  const noticeIcon = { disk: '⛁', hook: '!', provider: '!' }
</script>

<section class="home">
<div class="dash">
  {#if accounts.cards.length}
    <div class="cards">
      {#each accounts.cards as group (group.id)}
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
        <Row dense clamp={false} text={n.text}>
          {#snippet lead()}<span class="ico" class:coral={n.severity === 'error'} class:gold={n.severity === 'warn'}>{noticeIcon[n.kind] ?? '!'}</span>{/snippet}
          {#snippet meta()}{#if n.command}<code class="mono">{n.command}</code>{/if}{/snippet}
          {#snippet trail()}<time>{ago(n.at)}</time><button class="x" aria-label="Dismiss" title="Dismiss" onclick={() => dismiss(n.id)}>×</button>{/snippet}
        </Row>
      {/each}
    </Card>
  {/if}

  {#if updates.length}
    <Card title="Updates" count={updates.length}>
      {#each updates as u (u.id)}
        <Row dense text={u.name}>
          {#snippet lead()}<span class="ico iris">{u.id === 'dyna' ? 'D' : '↓'}</span>{/snippet}
          {#snippet meta()}{#if results[u.id]}<span class="sub" class:err={!results[u.id].ok}>{results[u.id].text}</span>{:else}<span class="ver mono">{u.current} → <b>{u.latest}</b></span>{/if}{/snippet}
          {#snippet trail()}<button class="btn iris" disabled={busy[u.id]} onclick={() => runUpdate(u)}>{busy[u.id] ? 'Updating…' : 'Update'}</button>{/snippet}
        </Row>
      {/each}
    </Card>
  {/if}

  {#if load}<SystemLoadCard {load} {onagent} />{/if}

  {#if $heldComments.error}<p class="err" role="alert">Could not load held PR comments: {$heldComments.error}</p>{/if}
  {#if clear}<EmptyState title="All clear">No questions, alerts or updates need you.</EmptyState>{/if}
</div>
</section>

<style>
  .home { flex: 1; overflow-y: auto; }
  .dash { padding: var(--s4); display: grid; gap: var(--s3); align-content: start; max-width: 1240px; }
  .cards { display: grid; grid-template-columns: repeat(auto-fill, minmax(220px, 1fr)); gap: var(--s3); }
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
