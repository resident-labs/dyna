<script lang="ts">
  import { errorText } from '../../errorText.ts'
  import { api } from '../../../api.ts'
  import type { HarnessConfig } from '../../api/harness.ts'

  // Add one MCP server to Dyna's catalog: a local command with its arguments
  // and environment variable names, or a remote URL. A new server is off for
  // every harness until the MCP pane turns it on.
  let { onsave, oncancel }: { onsave: (catalog: HarnessConfig) => void; oncancel: () => void } = $props()
  let name = $state(''), mode = $state('command'), command = $state(''), args = $state('')
  let url = $state(''), env = $state(''), busy = $state(false), error = $state('')

  // argumentList reads a JSON array of strings, or null when the text is not one.
  function argumentList(text: string): string[] | null {
    const parsed: unknown = JSON.parse(text.trim() || '[]')
    if (!Array.isArray(parsed)) return null
    const items: unknown[] = parsed
    if (!items.every((item) => typeof item === 'string')) return null
    return items.map((item) => String(item))
  }

  async function save() {
    if (busy) return
    busy = true; error = ''
    try {
      const base = { name: name.trim(), enabledHarnesses: [] }
      let next: HarnessConfig
      if (mode === 'url') {
        next = await api.addHarnessMCP({ ...base, url: url.trim() })
      } else {
        const argv = argumentList(args)
        if (!argv) throw new Error('Arguments must be a JSON array of strings.')
        next = await api.addHarnessMCP({ ...base, command: command.trim(), args: argv, envKeys: env.split(/[\s,]+/).filter(Boolean) })
      }
      onsave(next)
    } catch (e) { error = e ? errorText(e) : 'Could not add this MCP server.' }
    finally { busy = false }
  }
</script>

<form onsubmit={(event) => { event.preventDefault(); save() }} aria-label="Add MCP server">
  <h3>Add MCP server</h3>
  <label>Name<input bind:value={name} required disabled={busy} placeholder="e.g. browser" /></label>
  <label>Connection<select bind:value={mode} disabled={busy}><option value="command">Local command</option><option value="url">Remote URL</option></select></label>
  {#if mode === 'command'}
    <label>Command<input bind:value={command} required disabled={busy} placeholder="e.g. npx" /></label>
    <label>Arguments (JSON array)<input bind:value={args} disabled={busy} placeholder={'["-y", "@playwright/mcp@latest"]'} spellcheck="false" /></label>
    <label>Environment variable names<input bind:value={env} disabled={busy} placeholder="e.g. API_TOKEN" /><span>Names only. Dyna reads the values from its environment.</span></label>
  {:else}
    <label>URL<input type="url" bind:value={url} required disabled={busy} placeholder="https://example.com/mcp" /></label>
  {/if}
  <p>New servers are off. Turn one on for each harness that should load it.</p>
  {#if error}<p class="error" role="alert">{error}</p>{/if}
  <div class="actions"><button type="button" class="btn quiet" disabled={busy} onclick={oncancel}>Cancel</button><button class="btn pri" disabled={busy}>{busy ? 'Adding…' : 'Add server'}</button></div>
</form>

<style>
  form { display: grid; gap: 14px; max-width: var(--page-reading-width); margin: 18px 0; padding: 20px; border: 1px solid var(--line); background: var(--side); border-radius: 10px; }
  h3 { margin: 0; font-size: var(--t-lead); font-weight: 500; }
  label { display: grid; gap: 6px; color: var(--ink-2); font-size: var(--t-sm); }
  input, select { width: 100%; min-width: 0; background: var(--field); border: 1px solid var(--line); border-radius: 6px; padding: var(--s2) 10px; color: var(--ink); }
  label span, p { font-size: var(--t-sm); color: var(--muted); margin: 0; line-height: 1.6; }
  .actions { display: flex; gap: var(--s2); justify-content: flex-end; }
  :disabled { opacity: .6; } .error { color: var(--coral); }
</style>
