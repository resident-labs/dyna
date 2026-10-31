<script lang="ts">
  import { scopes } from "../devices.ts";
  import type { Scope } from "../contract.gen.ts";
  // The 6 scopes as one row of checkboxes. An empty list grants more than the screen shows, so the last box stays checked.
  let { value, onchange, disabled = false }: { value: readonly Scope[]; onchange: (next: Scope[]) => void; disabled?: boolean } = $props();
  const pick = (scope: Scope, on: boolean) => onchange(scopes.map(item => item.scope).filter(item => item === scope ? on : value.includes(item)));
</script>

<fieldset class="boxes" aria-label="Access">
  {#each scopes as { scope, label, about } (scope)}{@const on = value.includes(scope)}
    <label title={about}><input type="checkbox" checked={on} disabled={disabled || (on && value.length === 1)} onchange={event => pick(scope, event.currentTarget.checked)} />{label}</label>
  {/each}
</fieldset>

<style>
  .boxes { display: flex; flex-wrap: wrap; gap: 6px 14px; min-width: 0; margin: 0; padding: 0; border: 0; font-size: 13px; font-weight: 500; }
  label { display: inline-flex; align-items: center; gap: 6px; cursor: pointer; }
  input { appearance: none; display: grid; place-items: center; width: 16px; height: 16px; margin: 0; border: 1px solid var(--firm); border-radius: 4px; background: none; cursor: pointer; }
  input:checked { border-color: var(--focus); background: var(--focus); }
  input:checked::after { content: ""; width: 8px; height: 4px; margin-top: -2px; border: solid var(--bg); border-width: 0 0 2px 2px; transform: rotate(-45deg); }
  input:focus-visible { outline-offset: 2px; }
  input:disabled { cursor: default; }
  @media (hover: none) { label { min-height: 40px; } }
</style>
