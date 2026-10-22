<script lang="ts">
  import { onMount } from 'svelte'
  import { api, openExternal } from '../../api.js'
  import { ago } from '../ui/format.js'
  import type { NoiseRule, PRNoiseList } from '../api/change.ts'
  import { errorText } from '../errorText.ts'

  let { roomId }: { roomId: string } = $props()
  let data = $state<PRNoiseList | null>(null)
  let loading = $state(true)
  let error = $state('')
  let removing = $state('')
  let removeError = $state<{ id: string; text: string } | null>(null)
  let message = $state('')
  let active = true

  async function load() {
    loading = true
    error = ''
    try {
      const next = await api.prNoiseRules(roomId)
      if (active) data = next
    } catch (e) {
      if (active) error = errorText(e)
    } finally {
      if (active) loading = false
    }
  }

  async function remove(rule: NoiseRule) {
    removing = rule.id
    removeError = null
    message = ''
    try {
      await api.removePRNoiseRule(roomId, rule.id)
      if (active) {
        if (data) data = { ...data, rules: data.rules.filter((r) => r.id !== rule.id) }
        message = `Removed the rule for ${rule.bot}.`
      }
    } catch (e) {
      if (active) removeError = { id: rule.id, text: errorText(e) }
    } finally {
      if (active) removing = ''
    }
  }

  onMount(() => {
    load()
    return () => { active = false }
  })
</script>

<section aria-labelledby="noise-title" aria-busy={loading}>
  <div class="heading">
    <h3 id="noise-title">Learned bot noise rules</h3>
    <button class="reload" onclick={load} disabled={loading || !!removing}>Reload</button>
  </div>
  <p class="hint">These rules skip matching bot comments in agent updates. Remove applies immediately to every room that uses the repository.</p>
  {#if loading}<p class="quiet" role="status">Loading learned rules…</p>{/if}
  {#if error}<p class="err" role="alert">Could not load learned rules: {error}</p>{/if}
  {#if data?.warning}<p class="err" role="status">{data.warning}</p>{/if}
  {#if message}<p class="success" role="status">{message}</p>{/if}
  {#if data}
    {#if data.rules.length}
      <ul class="rules">
        {#each data.rules as rule (rule.id)}
          <li>
            <div class="rule-head">
              <strong>{rule.bot}</strong>
              <button class="remove" disabled={!!removing || loading} aria-label="Remove rule for {rule.bot}: {rule.reason}" onclick={() => remove(rule)}>{removing === rule.id ? 'Removing…' : 'Remove'}</button>
            </div>
            <p class="repo mono">{rule.host}/{rule.repo}</p>
            <p class="reason">{rule.reason || 'No reason was saved.'}</p>
            <details>
              <summary>Comment template</summary>
              <pre>{rule.template}</pre>
            </details>
            <div class="meta">
              <span>{rule.hits || 0} match{rule.hits === 1 ? '' : 'es'}</span>
              {#if rule.created}<span>Learned {ago(rule.created)}</span>{/if}
              {#if rule.sourceUrl}<button class="source" onclick={() => openExternal(rule.sourceUrl)}>Source comment ↗</button>{/if}
            </div>
            {#if removeError?.id === rule.id}<p class="err" role="alert">Could not remove this rule: {removeError.text}</p>{/if}
          </li>
        {/each}
      </ul>
    {:else if !loading}
      <p class="quiet">{data.repositories.length ? 'No learned rules for this room’s repositories.' : 'This room has no GitHub repository or tracked PR repository.'}</p>
    {/if}
  {/if}
</section>

<style>
  section { margin: 16px 0; padding-top: 14px; border-top: 1px solid var(--line-soft); }
  .heading, .rule-head { display: flex; align-items: center; gap: 10px; }
  h3 { flex: 1; margin: 0; font-size: 14px; font-weight: 600; }
  .hint, .quiet, .err, .success { font-size: 12.5px; margin: 6px 0 10px; overflow-wrap: anywhere; }
  .hint { color: var(--muted); }
  .quiet { color: var(--faint); }
  .err { color: var(--coral); }
  .success { color: var(--sage); }
  .rules { list-style: none; padding: 0; margin: 10px 0 0; display: flex; flex-direction: column; gap: 8px; }
  li { min-width: 0; border: 1px solid var(--line-soft); border-radius: 8px; background: var(--main); padding: 10px 12px; }
  .rule-head strong { flex: 1; min-width: 0; font-size: 13px; overflow-wrap: anywhere; }
  .reload, .remove { flex: none; color: var(--ink-2); font-size: 12.5px; font-weight: 600; padding: 5px 9px; border-radius: 6px; background: var(--raise); }
  .reload:hover, .remove:hover { background: var(--raise-hover); color: var(--ink); }
  .repo { color: var(--faint); font-size: 11.5px; margin: 3px 0 7px; overflow-wrap: anywhere; }
  .reason { font-size: 13px; color: var(--ink-2); margin: 0 0 8px; overflow-wrap: anywhere; }
  details { font-size: 12.5px; color: var(--muted); }
  summary { cursor: pointer; }
  pre { font: 11.5px var(--mono); white-space: pre-wrap; overflow-wrap: anywhere; max-height: 180px; overflow-y: auto; color: var(--ink-2); margin: 8px 0; }
  .meta { display: flex; align-items: center; flex-wrap: wrap; gap: 4px 12px; font-size: 11.5px; color: var(--faint); margin-top: 8px; }
  .source { font-size: 12px; color: var(--iris); padding: 0; }
  .source:hover { text-decoration: underline; }
  button:disabled { opacity: 0.5; cursor: default; }
  @media (max-width: 760px) {
    .reload, .remove, .source { min-height: 44px; }
    .repo { font-size: 12px; }
    summary { min-height: 44px; display: list-item; align-content: center; }
  }
</style>
