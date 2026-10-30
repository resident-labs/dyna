<script lang="ts">
  import { errorText } from '../../errorText.ts'
  import { api } from '../../../api.ts'
  import type { HarnessConfig } from '../../api/harness.ts'

  // Add one skill to Dyna's catalog. A new skill is on, so every session can
  // read it from its next turn. The server checks the directory and the name,
  // and its message explains anything it rejects.
  let { onsave, oncancel }: { onsave: (catalog: HarnessConfig) => void; oncancel: () => void } = $props()
  let name = $state(''), source = $state(''), busy = $state(false), error = $state('')

  async function save() {
    if (busy) return
    busy = true; error = ''
    try {
      const next = await api.addHarnessSkill({ name: name.trim(), source: source.trim(), enabled: true })
      onsave(next)
    } catch (e) { error = e ? errorText(e) : 'Could not add this skill.' }
    finally { busy = false }
  }
</script>

<form onsubmit={(event) => { event.preventDefault(); save() }} aria-label="Add skill">
  <h3>Add skill</h3>
  <label>Name<input bind:value={name} required disabled={busy} placeholder="e.g. code-review" /></label>
  <label>Skill directory<input bind:value={source} required disabled={busy} placeholder="/absolute/path/to/skill" spellcheck="false" /><span>The absolute directory must contain SKILL.md.</span></label>
  <p>New skills are on. Every session can read them from its next turn.</p>
  {#if error}<p class="error" role="alert">{error}</p>{/if}
  <div class="actions"><button type="button" class="btn quiet" disabled={busy} onclick={oncancel}>Cancel</button><button class="btn pri" disabled={busy}>{busy ? 'Adding…' : 'Add skill'}</button></div>
</form>

<style>
  form { display: grid; gap: 14px; max-width: var(--page-reading-width); margin: 18px 0; padding: 20px; border: 1px solid var(--line); background: var(--side); border-radius: 10px; }
  h3 { margin: 0; font-size: var(--t-lead); font-weight: 500; }
  label { display: grid; gap: 6px; color: var(--ink-2); font-size: var(--t-sm); }
  input { width: 100%; min-width: 0; background: var(--field); border: 1px solid var(--line); border-radius: 6px; padding: var(--s2) 10px; color: var(--ink); }
  label span, p { font-size: var(--t-sm); color: var(--muted); margin: 0; line-height: 1.6; }
  .actions { display: flex; gap: var(--s2); justify-content: flex-end; }
  :disabled { opacity: .6; } .error { color: var(--coral); }
</style>
