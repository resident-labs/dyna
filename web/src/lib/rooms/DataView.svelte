<script lang="ts">
  // The structured data of a workflow post: closed by default as one line that
  // names its fields, open as key and value rows. Paths and hashes read as
  // code, long values cut short with a way to open them, and Copy puts the
  // JSON on the clipboard.
  import { codeLike } from './dataBlocks.ts'
  import type { JsonValue } from './dataBlocks.ts'

  let { value }: { value: JsonValue[] | { [key: string]: JsonValue } } = $props()
  const CUT = 140

  let open = $state(false)
  let copied = $state<'' | 'copied' | 'failed'>('')
  let longOpen = $state<Record<string, boolean>>({})

  let rows = $derived(Array.isArray(value) ? value.map((v, i): [string, JsonValue] => [String(i + 1), v]) : Object.entries(value))
  let noun = $derived(Array.isArray(value) ? (rows.length === 1 ? 'item' : 'items') : rows.length === 1 ? 'field' : 'fields')
  let keys = $derived(Array.isArray(value) ? '' : rows.map(([k]) => k).join(', '))

  function show(v: JsonValue): { text: string; code: boolean; plain: boolean } {
    if (typeof v === 'string') return { text: v, code: codeLike(v), plain: false }
    if (v === null || typeof v !== 'object') return { text: String(v), code: false, plain: true }
    return { text: JSON.stringify(v), code: true, plain: false }
  }

  async function copy() {
    try {
      await navigator.clipboard.writeText(JSON.stringify(value, null, 2))
      copied = 'copied'
    } catch {
      copied = 'failed'
    }
  }
</script>

<div class="data" class:open>
  <div class="bar">
    <button class="head" type="button" aria-expanded={open} onclick={() => (open = !open)}>
      <span class="chev" aria-hidden="true">›</span>
      <span class="count">{rows.length} {noun}</span>
      {#if keys && !open}<span class="keys">{keys}</span>{/if}
    </button>
    <button class="copy" type="button" onclick={copy} aria-label="Copy data as JSON">{copied === 'copied' ? 'Copied' : copied === 'failed' ? 'Not copied' : 'Copy'}</button>
  </div>
  {#if open}
    <dl class="rows">
      {#each rows as [k, v] (k)}
        {@const cell = show(v)}
        {@const cut = cell.text.length > CUT && !longOpen[k]}
        <dt>{k}</dt>
        <dd>
          {#if cell.code}<code>{cut ? cell.text.slice(0, CUT) + '…' : cell.text}</code>
          {:else if cell.plain}<span class="plain">{cell.text}</span>
          {:else if cell.text === ''}<span class="plain">empty</span>
          {:else}<span>{cut ? cell.text.slice(0, CUT) + '…' : cell.text}</span>{/if}
          {#if cell.text.length > CUT}<button class="more" type="button" onclick={() => (longOpen[k] = !longOpen[k])}>{longOpen[k] ? 'Show less' : 'Show more'}</button>{/if}
        </dd>
      {/each}
    </dl>
  {/if}
</div>

<style>
  .data { margin: var(--s2) 0; border: 1px solid var(--line-soft); border-radius: var(--r-sm); background: var(--side); max-width: 100%; }
  .bar { display: flex; align-items: center; gap: var(--s2); min-width: 0; }
  .head { flex: 1; min-width: 0; display: flex; align-items: baseline; gap: var(--s2); padding: 6px 10px; text-align: left; color: var(--ink-2); font-size: var(--t-sm); }
  .chev { color: var(--faint); transition: transform var(--motion-fast) var(--ease); flex: none; }
  .open .chev { transform: rotate(90deg); }
  .count { font-weight: 600; flex: none; }
  .keys { color: var(--faint); font-family: var(--mono); font-size: var(--t-cap); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; min-width: 0; }
  .copy { padding: 6px 10px; color: var(--muted); font-size: var(--t-sm); flex: none; transition: color var(--motion-fast) var(--ease); }
  @media (hover: hover) { .copy:hover { color: var(--ink); } }
  .rows { display: grid; grid-template-columns: minmax(70px, max-content) minmax(0, 1fr); gap: 4px 12px; margin: 0; padding: 4px 10px 10px; border-top: 1px solid var(--line-soft); max-height: 420px; overflow: auto; }
  dt { color: var(--muted); font-size: var(--t-sm); padding-top: 1px; overflow-wrap: anywhere; }
  dd { margin: 0; min-width: 0; overflow-wrap: anywhere; color: var(--ink-2); font-size: var(--t-sm); }
  code { font-family: var(--mono); font-size: var(--t-cap); background: var(--raise); padding: 0 4px; border-radius: 4px; word-break: break-all; }
  .plain { color: var(--faint); font-family: var(--mono); font-size: var(--t-cap); }
  .more { color: var(--iris); font-size: var(--t-cap); margin-left: var(--s1); }
  @media (max-width: 600px) {
    .rows { grid-template-columns: minmax(0, 1fr); gap: 0; }
    dd { margin-bottom: 6px; }
  }
</style>
