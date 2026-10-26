<script lang="ts">
  // The room or suite list of a CI machine: the chosen values as chips, each
  // with a remove button, and one way to add. An empty list means "every one",
  // which the empty text says. With readOnly the chips stay and the controls
  // go, for a caller who may not change the list.
  import { editScope } from './machines.ts'

  // ScopeOption is a value the picker offers and the name that shows for it.
  interface ScopeOption {
    value: string
    label: string
  }

  interface Props {
    // label names the list for assistive tech, such as "Rooms".
    label: string
    values: readonly string[]
    // options, when given, make the add control a picker of exactly these;
    // without it, the add control is a text field.
    options?: readonly ScopeOption[]
    // everyText words the empty list, such as "Every room".
    everyText: string
    placeholder?: string
    readOnly?: boolean
    busy?: boolean
    onchange: (values: string[]) => void
  }
  let { label, values, options = undefined, everyText, placeholder = '', readOnly = false, busy = false, onchange }: Props = $props()

  let draft = $state('')
  let names = $derived(new Map((options ?? []).map((o) => [o.value, o.label])))
  let available = $derived((options ?? []).filter((o) => !values.includes(o.value)))

  function change(edit: Parameters<typeof editScope>[1]) {
    const next = editScope(values, edit)
    if (next) onchange(next)
  }
  function addDraft() {
    change({ add: draft })
    draft = ''
  }
  function pick(e: Event & { currentTarget: HTMLSelectElement }) {
    change({ add: e.currentTarget.value })
    e.currentTarget.value = ''
  }
</script>

<span class="scope">
  <span class="chips" role="list" aria-label={label}>
    {#each values as value (value)}
      <span class="chip" role="listitem">
        <span class="text">{names.get(value) ?? value}</span>
        {#if !readOnly}<button class="x" type="button" disabled={busy} aria-label="Remove {names.get(value) ?? value}" onclick={() => change({ remove: value })}>×</button>{/if}
      </span>
    {:else}
      <span class="every">{everyText}</span>
    {/each}
  </span>
  {#if !readOnly}
    {#if options}
      <select class="add" aria-label="Add to {label}" disabled={busy || available.length === 0} onchange={pick}>
        <option value="">{available.length ? 'Add…' : 'None left'}</option>
        {#each available as o (o.value)}<option value={o.value}>{o.label}</option>{/each}
      </select>
    {:else}
      <input class="add" type="text" aria-label="Add to {label}" {placeholder} bind:value={draft} disabled={busy} onkeydown={(e) => { if (e.key === 'Enter') { e.preventDefault(); addDraft() } }} onchange={addDraft} />
    {/if}
  {/if}
</span>

<style>
  .scope { display: flex; flex-wrap: wrap; align-items: center; gap: var(--s2); min-width: 0; margin-top: var(--s2); }
  .chips { display: inline-flex; flex-wrap: wrap; gap: var(--s1); min-width: 0; }
  .chip { display: inline-flex; align-items: center; gap: 2px; padding: 2px 4px 2px 9px; border-radius: 9px; background: var(--iris-wash); color: var(--iris); box-shadow: inset 0 0 0 1px var(--iris-edge); font-size: var(--t-cap); font-weight: 600; max-width: 100%; }
  .chip:has(.x:not(:disabled)) { padding-right: 2px; }
  .text { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .x { flex: none; width: 18px; height: 18px; display: grid; place-items: center; border-radius: 50%; color: inherit; line-height: 1; transition: background-color var(--motion-fast) var(--ease); }
  @media (hover: hover) { .x:hover { background: var(--hover); } }
  .x:active { background: var(--select); }
  .x:disabled { opacity: 0.5; pointer-events: none; }
  .every { color: var(--faint); font-size: var(--t-sm); }
  .add { width: 120px; max-width: 100%; background: var(--field); border: 0; border-radius: var(--r-xs); padding: 4px 8px; color: var(--ink); font: inherit; font-size: var(--t-sm); }
  @media (max-width: 760px) {
    .x { width: 28px; height: 28px; }
    .add { min-height: 36px; }
  }
</style>
