<script lang="ts">
  import { onMount } from 'svelte'
  import { api } from '../../../api.ts'
  import { dialog } from '../../ui/dialog.ts'
  import { filterProviders } from './state.ts'

  // Add an API-key provider. The key goes to the engine once and is cleared
  // here; the engine never returns it.
  type Provider = { id: string; name: string }
  let { harness, name, onclose, onsaved }: { harness: string; name: string; onclose: () => void; onsaved: (providers: Provider[]) => void } = $props()

  let choices: Provider[] = $state([])
  let query = $state('')
  let picked: Provider | null = $state(null)
  let key = $state('')
  let loading = $state(true)
  let busy = $state(false)
  let error = $state('')
  let search: HTMLInputElement | undefined = $state()

  const shown = $derived(filterProviders(choices, query))

  onMount(async () => {
    try {
      choices = (await api.harnessProviderKeys(harness, 'choices')).choices ?? []
    } catch (e) {
      error = e instanceof Error && e.message ? e.message : `Could not read the providers ${name} supports.`
    } finally {
      loading = false
    }
  })

  async function save() {
    if (!picked || !key || busy) return
    busy = true
    error = ''
    try {
      const result = await api.harnessProviderKeys(harness, 'add', picked.id, key)
      key = ''
      onsaved(result.providers ?? [])
    } catch (e) {
      error = e instanceof Error && e.message ? e.message : 'Could not save the key.'
      busy = false
    }
  }
</script>

<!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
<div class="backdrop">
  <!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
  <form class="card" use:dialog={{ close: onclose, busy, initial: () => search }} role="dialog" tabindex="-1" aria-modal="true" aria-label={`Add provider to ${name}`} onclick={(e) => e.stopPropagation()} onsubmit={(e) => { e.preventDefault(); save() }}>
    <h2>Add provider to {name}</h2>
    <label>
      <span class="muted small">Provider</span>
      <input class="field" bind:this={search} bind:value={query} placeholder={loading ? 'Loading…' : 'Type to filter'} autocomplete="off" aria-label="Filter providers" disabled={busy} />
    </label>
    <div class="list" role="listbox" aria-label="Providers">
      {#each shown as c (c.id)}
        <button type="button" role="option" aria-selected={picked?.id === c.id} class:on={picked?.id === c.id} disabled={busy} onclick={() => (picked = c)}>{c.name}</button>
      {:else}
        <p class="muted small empty">{loading ? 'Loading…' : 'No provider matches.'}</p>
      {/each}
    </div>
    <label>
      <span class="muted small">API key</span>
      <input class="field mono" type="password" bind:value={key} autocomplete="off" spellcheck="false" disabled={busy} />
    </label>
    <p class="muted small">Dyna saves the key in {name}’s private home. It never shows the key again.</p>
    {#if error}<p class="err" role="alert">{error}</p>{/if}
    <div class="row">
      <button type="button" class="ghost" onclick={onclose} disabled={busy}>Cancel</button>
      <button type="submit" class="go" disabled={busy || !picked || !key}>{busy ? 'Saving…' : 'Save'}</button>
    </div>
  </form>
</div>

<style>
  .backdrop { position: fixed; inset: 0; background: rgba(8, 8, 10, 0.62); display: grid; place-items: center; z-index: 60; padding: var(--s6); }
  .card { width: min(440px, 100%); background: var(--raise); border: 1px solid var(--hair-strong); border-radius: var(--r-lg); box-shadow: 0 24px 64px rgba(0, 0, 0, 0.5); padding: var(--s5); }
  h2 { margin: 0 0 var(--s3); font-size: var(--t-title); font-weight: 600; }
  label { display: grid; gap: var(--s1); margin-bottom: var(--s3); }
  .field { background: var(--field); border: 1px solid var(--line); border-radius: var(--r-xs); padding: 7px 10px; font-size: var(--t-ui); color: var(--ink); }
  .mono { font-family: var(--mono); }
  .muted { color: var(--muted); }
  .small { font-size: var(--t-sm); }
  .list { max-height: 180px; overflow-y: auto; background: var(--field); border: 1px solid var(--line); border-radius: var(--r-xs); margin: calc(var(--s2) * -1) 0 var(--s3); }
  .list button { display: block; width: 100%; text-align: left; padding: 7px 10px; font-size: var(--t-ui); transition: background-color var(--motion-fast) var(--ease); }
  @media (hover: hover) { .list button:hover { background: var(--hover); } }
  .list button:active, .list button.on { background: var(--select); }
  .empty { margin: 0; padding: 7px 10px; }
  p { margin: 0 0 var(--s2); }
  .err { color: var(--coral); }
  .row { display: flex; justify-content: flex-end; gap: var(--s2); margin-top: var(--s4); }
  .row button { padding: var(--s2) var(--s4); border-radius: 6px; font-size: var(--t-body); font-weight: 500; }
  .ghost { color: var(--ink-2); }
  .go { background: var(--iris); color: #18161d; }
  .go:disabled, .ghost:disabled { opacity: 0.6; cursor: default; }
</style>
