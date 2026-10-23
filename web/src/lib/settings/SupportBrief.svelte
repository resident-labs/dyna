<script lang="ts">
  // Ask an agent to add missing harness support. The brief is prefilled and
  // editable. Nothing starts until the owner presses Start: Start sends a
  // "/<profile> <brief>" draft to the current room, the same path the
  // composer uses.
  import { errorText } from '../errorText.ts'
  import { dialog } from '../ui/dialog.ts'
  import { api } from '../../api.ts'
  import { bestProfile, supportBrief, supportDraft } from './support.ts'
  import type { ProfileView } from '../api/profile.ts'
  import type { EditableProfile } from './profiles/state.ts'

  interface Props {
    profile: EditableProfile
    profiles?: ProfileView[]
    roomId?: string
    onstarted?: (profile: string) => void
    oncancel: () => void
  }
  let { profile, profiles = [], roomId = '', onstarted, oncancel }: Props = $props()

  let brief = $state(supportBrief(profile))
  let chosen = $state(bestProfile(profiles))
  let busy = $state(false)
  let error = $state('')
  let first = $state<HTMLSelectElement>()

  let choices = $derived(profiles.filter((p) => !p.disabled))

  async function start() {
    if (busy || !roomId || !chosen || !brief.trim()) return
    busy = true
    error = ''
    try {
      await api.send(roomId, supportDraft(chosen, brief), '', [])
      onstarted?.(chosen)
    } catch (e) {
      error = errorText(e)
      busy = false
    }
  }
</script>

<!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
<div class="backdrop">
  <!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
  <div class="card" use:dialog={{ close: oncancel, busy, initial: () => first }} role="dialog" tabindex="-1" aria-modal="true" aria-label="Add {profile.harness} support" onclick={(e) => e.stopPropagation()}>
    <h2>Add {profile.harness} support</h2>
    <p>This starts a new session in the current room. Edit the brief if you like. Nothing starts until you press Start.</p>
    <label>Run the session on
      <select bind:this={first} bind:value={chosen}>
        {#each choices as c (c.name)}<option value={c.name}>{c.name}{c.model ? ' · ' + c.model : ''}</option>{/each}
      </select>
    </label>
    <label>Brief
      <textarea bind:value={brief} rows="12" spellcheck="false"></textarea>
    </label>
    {#if !roomId}<p class="err">Open a room first. The session starts in the room you are in.</p>{/if}
    {#if error}<p class="err" role="alert">{error}</p>{/if}
    <div class="row">
      <button class="btn quiet lg" onclick={oncancel} disabled={busy}>Cancel</button>
      <button class="btn fill lg" onclick={start} disabled={busy || !roomId || !chosen || !brief.trim()}>{busy ? 'Starting…' : 'Start'}</button>
    </div>
  </div>
</div>

<style>
  .backdrop { position: fixed; inset: 0; background: rgba(8, 8, 10, 0.62); display: grid; place-items: center; z-index: 60; padding: var(--s6); }
  .card { width: min(560px, 100%); max-height: calc(100vh - 64px); overflow-y: auto; background: var(--side); border-radius: 12px; box-shadow: 0 24px 64px rgba(0, 0, 0, 0.5); padding: 20px 20px var(--s4); outline: none; }
  h2 { margin: 0 0 10px; font-size: var(--t-head); font-weight: 600; }
  p { margin: 0 0 var(--s2); color: var(--ink-2); font-size: var(--t-body); line-height: 1.45; }
  .err { color: var(--coral); }
  label { display: flex; flex-direction: column; gap: 6px; font-size: var(--t-sm); font-weight: 600; color: var(--muted); margin-top: var(--s3); }
  select, textarea { min-width: 0; box-sizing: border-box; width: 100%; background: var(--rail); border: 1px solid transparent; border-radius: 6px; color: var(--ink); padding: var(--s2) 10px; font: inherit; font-size: var(--t-ui); font-weight: 400; outline: none; }
  textarea { font-family: var(--mono); resize: vertical; }
  select:focus, textarea:focus { border-color: var(--iris-edge); }
  .row { display: flex; justify-content: flex-end; flex-wrap: wrap; gap: var(--s2); margin-top: var(--s4); }
</style>
