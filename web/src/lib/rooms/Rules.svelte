<script>
  import { tick } from 'svelte'
  import { api } from '../../api.ts'
  import Avatar from '../ui/Avatar.svelte'
  import Confirm from '../ui/Confirm.svelte'
  import TabHeader from './TabHeader.svelte'
  import ComposerBox from '../ui/ComposerBox.svelte'
  import Row from '../ui/Row.svelte'
  import EmptyState from '../ui/EmptyState.svelte'
  import { agentRule, globalRule, ruleFilter, ruleUsage, ruleTime, scopeLabels } from './rules.js'
  // view is {room, global, hash}: the Rules API response {rules, used, max,
  // hash} for this room and for all rooms. editing is true while an inline
  // edit is open; App holds new lists back until it closes.
  let { roomId, view = null, members = [], ownerHue = 0, editing = $bindable(false), onchanged } = $props()
  let filter = $state('all')
  let scope = $state('room')
  let text = $state('')
  let busy = $state(false)
  let error = $state('')
  let edit = $state(null)
  let editGeneration = 0
  let removing = $state(null)
  let now = $state(Date.now())
  $effect(() => { const timer = setInterval(() => (now = Date.now()), 60000); return () => clearInterval(timer) })
  // Unmounting with an edit open (another Room settings section) ends the edit.
  $effect(() => { editing = !!edit; return () => { editing = false } })
  let sections = $derived([
    { id: 'global', title: scopeLabels.global, blurb: 'Every agent in every room gets these when its harness next starts.', rules: view?.global?.rules ?? [] },
    { id: 'room', title: scopeLabels.room, blurb: 'Every agent in this room gets these on its next turn. They win over rules for all rooms.', rules: view?.room?.rules ?? [] },
  ])
  let agents = $derived(sections.reduce((n, s) => n + s.rules.filter(agentRule).length, 0))
  let usage = $derived(view?.[scope] ? `${ruleUsage(view[scope])} characters` : '')
  const member = (id) => members.find((m) => m.id === id || m.address === id)
  const say = (e) => (error = String(e?.message || e))
  async function add() {
    busy = true
    try { await api.addRule(roomId, scope === 'global', text); text = ''; error = ''; await onchanged() }
    catch (e) { say(e) } finally { busy = false }
  }
  async function startEdit(r) {
    editGeneration++
    edit = { id: r.id, text: r.text }
    await tick()
    document.getElementById(`rule-edit-${r.id}`)?.focus()
  }
  async function restoreEditFocus(id, generation, allowed = () => true) {
    await tick()
    if (generation === editGeneration && !edit && allowed()) document.getElementById(`rule-control-${id}`)?.focus()
  }
  async function cancelEdit() {
    const id = edit?.id
    const generation = ++editGeneration
    edit = null
    if (id) await restoreEditFocus(id, generation)
  }
  function editKey(e) {
    if (e.isComposing || e.keyCode === 229) return
    if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); save() }
    else if (e.key === 'Escape') { e.stopPropagation(); cancelEdit() }
  }
  async function save() {
    if (!edit?.text.trim() || busy) return
    const draft = edit
    const generation = editGeneration
    let focusMoved = false
    const moved = () => { focusMoved = true }
    document.addEventListener('focusin', moved)
    busy = true
    try {
      await api.editRule(roomId, globalRule(draft), draft.id, draft.text)
      if (generation === editGeneration) edit = null
      error = ''; await tick(); await onchanged()
      await restoreEditFocus(draft.id, generation, () => !focusMoved)
    } catch (e) { say(e) } finally {
      document.removeEventListener('focusin', moved)
      busy = false
    }
  }
  async function remove(r) {
    busy = true
    try { await api.removeRule(roomId, globalRule(r), r.id); removing = null; error = ''; await onchanged() }
    catch (e) { say(e); removing = null } finally { busy = false }
  }
</script>
<section class="rules" aria-label="Rules">
  <TabHeader title="Rules" blurb="Standing rules for agents. Owner rules win over agent rules." filters={[{ id: 'all', label: 'All' }, { id: 'owner', label: 'Owner' }, { id: 'agents', label: `Agents (${agents})` }]} bind:filter {usage} />
  <div class="add-rule"><ComposerBox label="Rule text" placeholder="Write a standing rule in plain English…" submitLabel="Add rule" maxlength={500} bind:draft={text} {busy} canSend={!!text.trim()} bad={!!error} onsend={add} mode="" expand>
    {#snippet tools()}<span class="scope" role="radiogroup" aria-label="Rule scope">{#each ['room', 'global'] as id}<button type="button" role="radio" aria-checked={scope === id} class:sel={scope === id} onclick={() => (scope = id)}>{scopeLabels[id]}</button>{/each}</span>{/snippet}
  </ComposerBox>{#if error}<p class="error" role="alert">{error}</p>{/if}</div>
  <div class="list">
    {#each sections as sec (sec.id)}
      {@const rows = ruleFilter(sec.rules, filter)}
      <h2>{sec.title}</h2>
      <p class="blurb">{sec.blurb}</p>
      {#if sec.rules.length === 0}<EmptyState title="No rules yet">Add one above, or an agent can run <code>dyna rules{sec.id === 'global' ? ' --global' : ''} add</code>.</EmptyState>
      {:else if rows.length === 0}<EmptyState title="No {filter === 'owner' ? 'owner' : 'agent'} rules" />{/if}
      {#each rows as r (r.id)}
        {@const agent = agentRule(r)}
        {@const m = agent ? member(r.setter) : null}
        {#snippet lead()}<span class="id mono">{r.id}</span>{/snippet}
        {#if edit?.id === r.id}
          <Row {lead}>
            <textarea id="rule-edit-{r.id}" class="edit" aria-label="Edit rule {r.id}" maxlength="500" rows="3" bind:value={edit.text} aria-describedby="rule-shortcuts-{r.id}" onkeydown={editKey}></textarea>
            <p class="shortcuts" id="rule-shortcuts-{r.id}">Enter saves · Shift+Enter adds a line · Esc cancels</p>
            <span class="edittools"><button class="ghost" onclick={cancelEdit}>Cancel</button><button class="save" disabled={busy || !edit.text.trim()} onclick={save}>Save</button></span>
          </Row>
        {:else}
          <Row text={r.text} clamp={false} {lead}>
            {#snippet meta()}{#if agent}<Avatar name={m?.name ?? r.setterName} hue={m?.hue ?? 0} emoji={m?.emoji} size={18} /><span>{m?.name ?? r.setterName ?? r.setter}</span><span class="tag">agent</span>{:else}<Avatar owner hue={ownerHue} size={18} /><span>owner</span>{/if}<span>· {ruleTime(r, now)}</span>{/snippet}
            {#snippet actions()}<button id="rule-control-{r.id}" onclick={() => startEdit(r)}>Edit</button><button aria-label="Remove rule {r.id}" onclick={() => (agent ? remove(r) : (removing = r))}>Remove ×</button>{/snippet}
          </Row>
        {/if}
      {/each}
    {/each}
  </div>
</section>
{#if removing}<Confirm title="Remove this rule?" lines={[`${removing.id}: ${removing.text}`, globalRule(removing) ? 'The owner wrote it. Agents in every room stop getting it when their harness next starts.' : 'The owner wrote it. Agents stop getting it on their next turn.']} action="Remove" {busy} onconfirm={() => remove(removing)} oncancel={() => (removing = null)} />{/if}
<style>
  .add-rule { margin: 0 var(--s4) 18px; }
  .error { color: var(--coral); font-size: var(--t-ui); margin: var(--s2) 0; }
  .rules { display: flex; flex-direction: column; min-height: 0; flex: 1; }
  .list { overflow-y: auto; padding: 0 var(--s4) var(--s5); flex: 1; }
  h2 { font-size: var(--t-ui); font-weight: 600; margin: 14px var(--s2) 2px; }
  h2:first-child { margin-top: 0; }
  .blurb { font-size: var(--t-sm); color: var(--faint); margin: 0 var(--s2) 6px; }
  .scope { display: flex; gap: 2px; margin-right: auto; }
  .scope button { border-radius: 12px; padding: 3px 10px; font-size: var(--t-sm); color: var(--muted); transition: background-color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .scope button:hover { background: var(--hover); }
  }
  .scope button:active { background: var(--select); }
  .scope button.sel { background: var(--select); color: var(--ink); }
  .id { display: block; width: 24px; color: var(--faint); font-size: var(--t-sm); }
  .tag { color: var(--coral); border: 1px solid color-mix(in srgb, var(--coral) 35%, transparent); border-radius: 4px; padding: 0 5px; font-size: var(--t-cap); }
  .list button { font-size: var(--t-sm); color: var(--faint); padding: 2px 6px; border-radius: 4px; transition: color var(--motion-fast) var(--ease), background-color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .list button:hover { color: var(--ink); background: var(--hover); }
  }
  .list button:active { background: var(--select); }
  .edit { display: block; width: 100%; background: var(--side); border: 1px solid var(--line); border-radius: 6px; resize: vertical; color: var(--ink); font: inherit; font-size: var(--t-body); line-height: var(--leading); padding: var(--s2) 10px; }
  .shortcuts { margin: 6px 0 0; font-size: var(--t-cap); color: var(--muted); }
  .edittools { display: flex; justify-content: flex-end; gap: 6px; margin-top: 6px; }
  .list .save { color: var(--ink); background: var(--raise); font-weight: 600; } .save:disabled { opacity: .4; cursor: default; }
  @media (max-width: 760px) { .list { padding: 0 var(--s2) var(--s4); } }
</style>
