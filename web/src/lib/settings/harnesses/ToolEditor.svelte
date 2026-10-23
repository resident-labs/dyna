<script lang="ts">
  import { errorText } from '../../errorText.ts'
  import { api } from '../../../api.ts'
  import type { HarnessConfig } from '../../api/harness.ts'
  let { kind, onsave, oncancel }: { kind: string; onsave: (catalog: HarnessConfig) => void; oncancel: () => void } = $props()
  let name = $state(''), mode = $state('command'), command = $state(''), args = $state('')
  let url = $state(''), env = $state(''), source = $state(''), busy = $state(false), error = $state('')

  async function save() {
    if (busy) return
    busy = true; error = ''
    try {
      const entry = { name: name.trim(), enabledHarnesses: [] }
      let next: HarnessConfig
      if (kind === 'skill') {
        next = await api.addHarnessSkill({ ...entry, source: source.trim() })
      } else if (mode === 'url') {
        next = await api.addHarnessMCP({ ...entry, url: url.trim() })
      } else {
        const argv = JSON.parse(args.trim() || '[]')
        if (!Array.isArray(argv) || argv.some((arg) => typeof arg !== 'string')) throw new Error('Arguments must be a JSON array of strings.')
        next = await api.addHarnessMCP({ ...entry, command: command.trim(), args: argv, envKeys: env.split(/[\s,]+/).filter(Boolean) })
      }
      onsave(next)
    } catch (e) { error = e ? errorText(e) : 'Could not add this tool.' }
    finally { busy = false }
  }
</script>

<form onsubmit={(event) => { event.preventDefault(); save() }} aria-label={kind === 'mcp' ? 'Add MCP server' : 'Add skill'}>
  <h3>{kind === 'mcp' ? 'Add MCP server' : 'Add skill'}</h3>
  <label>Name<input bind:value={name} required disabled={busy} placeholder={kind === 'mcp' ? 'e.g. browser' : 'e.g. code-review'} /></label>
  {#if kind === 'mcp'}
    <label>Connection<select bind:value={mode} disabled={busy}><option value="command">Local command</option><option value="url">Remote URL</option></select></label>
    {#if mode === 'command'}
      <label>Command<input bind:value={command} required disabled={busy} placeholder="e.g. npx" /></label>
      <label>Arguments (JSON array)<input bind:value={args} disabled={busy} placeholder={'["-y", "@playwright/mcp@latest"]'} /></label>
      <label>Environment variable names<input bind:value={env} disabled={busy} placeholder="e.g. API_TOKEN" /><span>Names only. Dyna reads the values from its environment.</span></label>
    {:else}
      <label>URL<input type="url" bind:value={url} required disabled={busy} placeholder="https://example.com/mcp" /></label>
    {/if}
  {:else}
    <label>Skill directory<input bind:value={source} required disabled={busy} placeholder="/absolute/path/to/skill" /><span>The local directory must contain SKILL.md.</span></label>
  {/if}
  <p>After you add this tool, turn it on for each harness that needs it.</p>
  {#if error}<p class="error" role="alert">{error}</p>{/if}
  <div class="actions"><button type="button" class="btn quiet" disabled={busy} onclick={oncancel}>Cancel</button><button class="btn pri" disabled={busy}>{busy ? 'Adding…' : 'Add'}</button></div>
</form>

<style>
  form { display: grid; gap: 14px; max-width: 560px; margin: 18px 0; padding: 20px; border: 1px solid var(--line); background: var(--side); border-radius: 10px; }
  h3 { margin: 0; font-size: var(--t-lead); font-weight: 500; }
  label { display: grid; gap: 6px; color: var(--ink-2); font-size: var(--t-sm); }
  input, select { width: 100%; min-width: 0; background: var(--field); border: 1px solid var(--line); border-radius: 6px; padding: var(--s2) 10px; color: var(--ink); }
  label span, p { font-size: var(--t-sm); color: var(--muted); margin: 0; line-height: 1.6; }
  .actions { display: flex; gap: var(--s2); justify-content: flex-end; }
  :disabled { opacity: .6; } .error { color: var(--coral); }
</style>
