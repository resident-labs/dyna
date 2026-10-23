<script>
  import { api } from '../../api.js'
  import Avatar from '../ui/Avatar.svelte'
  import GoalGlyph from './GoalGlyph.svelte'
  import GoalHolders from './GoalHolders.svelte'
  import ExpediteBadge from './ExpediteBadge.svelte'
  import { expedite, goalExpedited } from './expedite.ts'
  import { goalAgo } from './goals.ts'
  // onexpedite(goal) starts or stops priority; App asks first when it replaces another goal.
  let { roomId, goal: g, members = [], onchanged, onclose, onexpedite = null } = $props()
  let editing = $state(false)
  let text = $state('')
  let pick = $state(false)
  let holders = $state([])
  let shared = $state(true)
  let action = $state('')
  let summary = $state('')
  let busy = $state(false)
  let error = $state('')
  let now = $state(Date.now())
  let status = $state(null)
  let items = $state([])
  $effect(() => { const timer = setInterval(() => (now = Date.now()), 60000); return () => clearInterval(timer) })
  // The status and checklist follow the selected goal; a late answer for an earlier goal is dropped.
  $effect(() => {
    const id = g.id
    if (!id) return
    status = null
    items = []
    api.goalStatus(roomId, id).then((s) => { if (g.id === id) status = s }, () => {})
    api.goalItems(roomId, id).then((list) => { if (g.id === id) items = list ?? [] }, () => {})
  })
  const TONES = { on_track: 'sage', waiting: 'muted', stuck: 'coral', errored: 'coral', ready_to_close: 'gold', unowned: 'gold' }
  const LABELS = { on_track: 'On track', waiting: 'Waiting', stuck: 'Stuck', errored: 'Errored', ready_to_close: 'Ready to close', unowned: 'Unowned' }
  const mark = (state) => (state === 'done' ? '✓' : state === 'dropped' ? '×' : '•')
  const member = (id) => members.find((m) => m.id === id || m.address === id)
  const name = (id) => member(id)?.name ?? id
  async function perform(fn) {
    busy = true; error = ''
    try { await fn(); editing = false; pick = false; action = ''; await onchanged() }
    catch (e) { error = String(e?.message || e) }
    finally { busy = false }
  }
  function editHolders() { holders = [...(g.holders ?? [])]; shared = g.shared; pick = true }
  function remove(id) {
    const next = g.holders.filter((h) => h !== id)
    perform(() => api.editGoal(roomId, g.id, '', next, !next.length))
  }
</script>
<aside class="detail" aria-label="Goal {g.id}">
  <header><GoalGlyph goal={g} /><h2>Goal {g.id}</h2>{#if goalExpedited($expedite, g)}<ExpediteBadge />{/if}<button class="link" disabled={editing} onclick={() => { text = g.text; editing = true }}>Edit</button><button class="x" aria-label="Close goal detail" title="Close (Esc)" onclick={onclose}>✕</button></header>
  <div class="scroll">
    {#if editing}
      <textarea aria-label="Edit goal text" maxlength="2000" rows="5" bind:value={text}></textarea>
      <div class="buttons"><button disabled={busy || !text.trim()} onclick={() => perform(() => api.editGoal(roomId, g.id, text, g.holders ?? [], g.shared))}>Save</button><button onclick={() => (editing = false)}>Cancel</button></div>
    {:else}
      <p class="prose">{g.text}</p>
    {/if}
    {#if status}
      <p class="statusline"><span class="tone-{TONES[status.label] ?? 'muted'}">{LABELS[status.label] ?? status.label}</span> <span class="reason">{status.reason}</span>{#if status.open || status.done || status.dropped}<span class="counts">{status.open} open · {status.done} done · {status.dropped} dropped</span>{/if}</p>
    {/if}
    <dl>
      <dt>Holders</dt><dd>
        {#if pick}
          <GoalHolders {members} bind:holders bind:shared />
          <div class="buttons"><button disabled={busy} onclick={() => perform(() => api.editGoal(roomId, g.id, '', holders, shared))}>Save</button><button onclick={() => (pick = false)}>Cancel</button></div>
        {:else}
          {#if g.shared}<span class="holder">Room</span>{/if}
          {#each g.holders ?? [] as id}
            {@const m = member(id)}
            <span class="holder"><Avatar name={name(id)} hue={m?.hue ?? 0} emoji={m?.emoji} size={18} /><span class="hue-{m?.hue ?? 0}">{name(id)}</span><button class="remove" aria-label="Remove {name(id)}" onclick={() => remove(id)}>✕</button></span>
          {/each}
          <button class="link" onclick={editHolders}>Add</button>
        {/if}
      </dd>
      <dt>From</dt><dd>{name(g.setter)}</dd>
      <dt>State</dt><dd class:done={g.state === 'done'}>{g.state}</dd>
    </dl>
    {#if g.state === 'active'}
      <div class="buttons actions"><button disabled={busy} title="Ask the room's goal holders for a check-in now" onclick={() => perform(() => api.checkGoal(roomId, g.id))}>Check in now</button>{#if onexpedite}<button disabled={busy} title="Review and merge this goal's agents and PRs first" onclick={() => perform(() => onexpedite(g))}>{goalExpedited($expedite, g) ? 'Stop expediting' : 'Expedite'}</button>{/if}<button onclick={() => (action = action === 'done' ? '' : 'done')}>Mark done</button><button class="danger" onclick={() => (action = action === 'drop' ? '' : 'drop')}>Drop</button></div>
      {#if action === 'done'}<form class="inline" onsubmit={(e) => { e.preventDefault(); perform(() => api.finishGoal(roomId, g.id, summary)) }}><input aria-label="Completion summary" placeholder="One-line summary (optional)" bind:value={summary} maxlength="2000" /><div class="buttons"><button disabled={busy}>Mark done</button><button type="button" onclick={() => (action = '')}>Cancel</button></div></form>{/if}
      {#if action === 'drop'}<div class="inline"><p>Drop this goal?</p><div class="buttons"><button class="danger" disabled={busy} onclick={() => perform(() => api.finishGoal(roomId, g.id, '', true))}>Drop goal</button><button onclick={() => (action = '')}>Cancel</button></div></div>{/if}
    {/if}
    {#if error}<p class="error" role="alert">{error}</p>{/if}
    <h3>Checklist</h3>
    {#if !items.length}<p class="quiet">No items. Delegate with <code>dyna subagent start</code> or add one with <code>dyna goal item add</code>.</p>{/if}
    <ol class="checklist" aria-label="Checklist">
      {#each items as it (it.id)}
        <li class:done={it.state === 'done'} class:dropped={it.state === 'dropped'}>
          <span class="mark" aria-hidden="true">{mark(it.state)}</span>
          <span class="item-text">{(it.text ?? '').split('\n')[0]}</span>
          {#if it.assignee}<span class="assignee">@{name(it.assignee)}</span>{/if}
        </li>
      {/each}
    </ol>
    <h3>History</h3>
    {#if !g.edits?.length}<p class="quiet">No edits yet.</p>{/if}
    {#each [...(g.edits ?? [])].reverse() as edit}<article class="history"><div>{name(edit.by)} edited · <time title={new Date(edit.at).toLocaleString()}>{goalAgo(edit.at, now)}</time></div><p><span>Was:</span> {edit.oldText}</p></article>{/each}
  </div>
</aside>
<style>
  .detail { width: 360px; flex: none; background: var(--side); border-left: 1px solid var(--line-soft); display: flex; flex-direction: column; min-height: 0; }
  header { display: flex; align-items: center; gap: var(--s2); padding: 14px var(--s3) var(--s2) var(--s4); }
  h2 { flex: 1; margin: 0; font-size: var(--t-lead); font-weight: 600; }
  .x { color: var(--muted); width: 28px; height: 28px; border-radius: 6px; font-size: var(--t-ui); transition: background-color var(--motion-fast) var(--ease); } @media (hover: hover) { .x:hover { background: var(--hover); } } .x:active { background: var(--select); }
  .scroll { overflow-y: auto; flex: 1; padding: var(--s2) 20px var(--s5); }
  .prose { white-space: pre-wrap; font-size: var(--t-lead); line-height: var(--leading); color: var(--ink); margin: 0 0 var(--s2); }
  .link { color: var(--muted); font-size: var(--t-sm); transition: color var(--motion-fast) var(--ease); } @media (hover: hover) { .link:hover { color: var(--iris); text-decoration: underline; } }
  textarea, input { width: 100%; background: var(--field); color: var(--ink); border: 1px solid var(--line); border-radius: 5px; font: inherit; font-size: var(--t-ui); padding: var(--s2); }
  textarea { resize: vertical; }
  dl { display: grid; grid-template-columns: 90px 1fr; gap: var(--s3) 0; font-size: var(--t-sm); margin: var(--s5) 0 20px; }
  dt { color: var(--muted); } dd { margin: 0; min-width: 0; overflow-wrap: anywhere; }
  .holder { display: inline-flex; align-items: center; gap: 5px; margin: 0 5px 5px 0; }
  .remove { opacity: 0; color: var(--faint); font-size: var(--t-cap); } .remove:focus-visible { opacity: 1; } .holder:hover .remove { opacity: 1; }
  .buttons { display: flex; gap: 5px; margin-top: 6px; }
  .buttons button { font-size: var(--t-sm); background: var(--raise); padding: 5px 9px; border-radius: 5px; color: var(--muted); transition: background-color var(--motion-fast) var(--ease), color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .buttons button:hover { background: var(--raise-hover); color: var(--ink); }
  }
  .buttons button:active { background: var(--select); } .buttons button:disabled { opacity: .4; }
  .buttons .danger { color: var(--coral); }
  .actions { padding: 0 0 20px; border-bottom: 1px solid var(--line-soft); }
  .inline { border: 1px solid var(--line); border-radius: 6px; padding: 10px; margin-bottom: var(--s4); font-size: var(--t-ui); } .inline p { margin: 0; }
  h3 { color: var(--muted); font-size: var(--t-sm); font-weight: 600; margin: 22px 0 var(--s3); }
  time { color: var(--faint); font-size: var(--t-cap); }
  .history p span { color: var(--muted); }
  .done { color: var(--sage); }
  .quiet { font-size: var(--t-sm); color: var(--faint); margin: 10px 0; }
  .history { font-size: var(--t-sm); margin-bottom: 14px; color: var(--muted); } .history time { margin-left: 6px; }
  .history p { color: var(--faint); margin: 5px 0 0; display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; overflow: hidden; }
  .statusline { display: flex; flex-wrap: wrap; gap: 6px; align-items: baseline; font-size: var(--t-sm); margin: 0 0 var(--s3); }
  .statusline .tone-sage { color: var(--sage); font-weight: 600; } .statusline .tone-coral { color: var(--coral); font-weight: 600; } .statusline .tone-gold { color: var(--gold); font-weight: 600; } .statusline .tone-muted { color: var(--muted); font-weight: 600; }
  .reason { color: var(--ink-2); min-width: 0; overflow-wrap: anywhere; }
  .counts { color: var(--faint); font-size: var(--t-cap); white-space: nowrap; }
  .checklist { list-style: none; margin: 0 0 var(--s5); padding: 0; display: flex; flex-direction: column; gap: 6px; }
  .checklist li { display: grid; grid-template-columns: 18px 1fr auto; gap: 6px; font-size: var(--t-sm); align-items: baseline; }
  .checklist .mark { color: var(--muted); } .checklist .done { color: var(--faint); } .checklist .done .mark { color: var(--sage); } .checklist .dropped { color: var(--faint); text-decoration: line-through; } .checklist .dropped .mark { color: var(--coral); }
  .item-text { min-width: 0; overflow-wrap: anywhere; }
  .assignee { color: var(--iris); font-size: var(--t-cap); white-space: nowrap; }
  .error { font-size: var(--t-sm); color: var(--coral); }
  @media (hover: none) { .remove { opacity: 1; } }
</style>
