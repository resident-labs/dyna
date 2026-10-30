<script lang="ts">
  // The control for one `dyna settings` key, chosen by its kind. A switch or a
  // choice saves on change; text saves on Enter or blur, and Escape drops it.
  // A refused save keeps the text, so the owner can correct it.
  import Switch from '../ui/Switch.svelte'
  import Segmented from '../ui/Segmented.svelte'
  import { settingDisplay } from '../settings/display.ts'
  import type { SettingItem } from '../api/settings.ts'

  let { item, label, onsave, sourceInvalid = false }: { item: SettingItem; label: string; onsave: (value: string) => Promise<void>; sourceInvalid?: boolean } = $props()

  let draft = $state<string | null>(null)
  let sourceChoice = $state<'checkout' | null>(null)
  const sourceMode = $derived(sourceChoice ?? (item.value ? 'checkout' : 'release'))
  const fixed = $derived(item.source === 'env')

  async function commit() {
    if (draft === null || draft === settingDisplay(item)) { draft = null; return }
    // Only the text that was saved clears, so an edit typed meanwhile stays.
    const sent = draft
    await onsave(sent).then(() => { if (draft === sent) { draft = null; if (item.key === 'updater.source') sourceChoice = null } }, () => {})
  }

  // A choice saves at once; the row shows a refusal, and the control keeps
  // the stored value.
  const choose = (value: string): void => { onsave(value).catch(() => {}) }

  function key(e: KeyboardEvent & { currentTarget: HTMLInputElement }) {
    if (e.key === 'Enter') e.currentTarget.blur()
    if (e.key === 'Escape') { e.stopPropagation(); draft = null; e.currentTarget.blur() }
  }
</script>

{#if item.key === 'updater.source'}
  <div class="source">
    <Segmented options={[["release", "Release"], ["checkout", "Source checkout"]]} value={sourceMode} {label} onchange={(value: string) => { if (value === 'checkout') sourceChoice = 'checkout'; else onsave('').then(() => { sourceChoice = null }, () => { sourceChoice = null }) }} />
    {#if sourceMode === 'checkout'}
      <input class="field mono" aria-label="Source checkout path" placeholder="/path/to/checkout" disabled={fixed} value={draft ?? String(item.value)}
        oninput={(e) => (draft = e.currentTarget.value)} onkeydown={key} onblur={commit} />
      {#if sourceInvalid}<span class="source-hint">Not a Git repository. Choose a checkout path.</span>{/if}
    {/if}
  </div>
{:else if item.kind === 'bool'}
  <Switch on={item.value === true} {label} disabled={fixed} onchange={(on: boolean) => choose(String(on))} />
{:else if item.kind === 'enum'}
  <Segmented options={(item.options ?? []).map((o) => [o, o])} value={String(item.value)} {label} onchange={(o: string) => choose(o)} />
{:else if item.kind === 'select'}
  <select class="field" aria-label={label} disabled={fixed} value={String(item.value)} onchange={(e) => choose(e.currentTarget.value)}>
    {#each item.options ?? [] as value, i}<option {value}>{item.optionLabels?.[i] ?? value}</option>{/each}
  </select>
{:else}
  <input class="field mono" aria-label={label} disabled={fixed} value={draft ?? settingDisplay(item)} inputmode={item.kind === 'int' ? 'numeric' : 'text'}
    oninput={(e) => (draft = e.currentTarget.value)} onkeydown={key} onblur={commit} />
{/if}

<style>
  .source { display: flex; flex-direction: column; align-items: flex-start; gap: var(--s2); width: 100%; }
  .source-hint { width: 100%; color: var(--muted); font-size: var(--t-sm); }
  .field { width: 12rem; max-width: 100%; min-height: 30px; padding: 0 var(--s2); background: var(--rail); border: 1px solid var(--border-soft); border-radius: var(--r-xs); color: var(--ink); font-size: var(--t-sm); }
  .source .field { width: 100%; }
  .field:focus { border-color: var(--iris-edge); outline: none; }
</style>
