<script lang="ts">
  // The add and edit form for one hook. form is a hookForm copy; editing is
  // true when the hook exists, so its name cannot change. The host checks
  // every value, the same as `dyna git hooks add` and `dyna git hooks edit`.
  import { eventLabel, hookEvents, type HookFields } from './hooks.ts'

  interface Props {
    form: HookFields
    editing?: boolean
    busy?: boolean
    error?: string
    onsave: () => void
    oncancel: () => void
  }
  let { form = $bindable(), editing = false, busy = false, error = '', onsave, oncancel }: Props = $props()
</script>

<form class="hookform" aria-label={editing ? `Edit ${form.name}` : 'Add hook'} onsubmit={(e) => { e.preventDefault(); onsave() }}>
  <label for="hk-name">Name</label>
  <input id="hk-name" class="mono" bind:value={form.name} disabled={editing || busy} placeholder="verify" />
  <label for="hk-event">Event</label>
  <select id="hk-event" bind:value={form.event} disabled={busy}>
    {#if !hookEvents.includes(form.event)}<option value={form.event}>{eventLabel(form.event)}</option>{/if}
    {#each hookEvents as e}<option value={e}>{eventLabel(e)}</option>{/each}
  </select>
  {#if form.event === 'schedule'}
    <label for="hk-every">Every (minutes)</label>
    <input id="hk-every" type="number" min="1" bind:value={form.everyMinutes} disabled={busy} />
  {/if}
  <label for="hk-command">Command</label>
  <textarea id="hk-command" class="mono" rows="2" bind:value={form.command} disabled={busy} placeholder="go test ./..."></textarea>
  <label for="hk-cwd">Run in</label>
  <input id="hk-cwd" class="mono" bind:value={form.cwd} disabled={busy} aria-describedby="hk-cwd-help" />
  <p id="hk-cwd-help" class="hint">worktree, main, or an absolute path.</p>
  <label for="hk-timeout">Timeout</label>
  <input id="hk-timeout" class="mono" bind:value={form.timeout} disabled={busy} aria-describedby="hk-timeout-help" />
  <p id="hk-timeout-help" class="hint">A duration such as 5m. It bounds the command.</p>
  {#if form.event === 'before-land'}
    <label for="hk-fail">On failure</label>
    <select id="hk-fail" bind:value={form.onFail} disabled={busy}><option value="alert">Alert the owner</option><option value="block">Block the land</option></select>
  {/if}
  {#if error}<p class="err">{error}</p>{/if}
  <div class="actions">
    <button type="button" class="btn quiet" onclick={oncancel} disabled={busy}>Cancel</button>
    <button type="submit" class="btn pri" disabled={busy || !form.name.trim() || !form.command.trim()}>{busy ? 'Saving…' : editing ? 'Save' : 'Add hook'}</button>
  </div>
</form>

<style>
  .hookform { display: grid; grid-template-columns: 140px 1fr; gap: var(--s2) var(--s3); align-items: center; background: var(--side); border: 1px solid var(--line); border-radius: 10px; padding: 14px var(--s4); }
  label { font-size: var(--t-ui); color: var(--muted); }
  input, select, textarea { background: var(--rail); border: 1px solid transparent; border-radius: 6px; color: var(--ink); padding: 7px 10px; font: inherit; font-size: var(--t-ui); outline: none; min-width: 0; }
  textarea { resize: vertical; }
  .mono { font-family: var(--mono); font-size: var(--t-sm); }
  input:focus, select:focus, textarea:focus { border-color: var(--iris-edge); }
  input:disabled, select:disabled, textarea:disabled { opacity: 0.6; }
  .hint { grid-column: 2; margin: -4px 0 2px; font-size: var(--t-sm); color: var(--faint); }
  .err { grid-column: 1 / -1; margin: 0; font-size: var(--t-ui); color: var(--coral); }
  .actions { grid-column: 1 / -1; display: flex; justify-content: flex-end; gap: var(--s2); margin-top: var(--s1); }
  @media (max-width: 760px) {
    .hookform { grid-template-columns: 1fr; }
    .hint { grid-column: 1; }
    input, select, textarea { font-size: var(--t-title); min-height: 44px; }
    .actions button { flex: 1; min-height: 44px; }
  }
</style>
