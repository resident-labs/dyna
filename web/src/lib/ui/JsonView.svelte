<script>
  // JsonView typesets a JSON value for people: objects as labelled fields,
  // lists of records as rows that open in place, links as links, long text
  // as paragraphs. A closed record renders nothing inside it, so large
  // results stay fast. See readable.js.
  import JsonView from './JsonView.svelte'
  import Segmented from './Segmented.svelte'
  import { fieldLabel, kindOf, recordTitle, recordLine, linkOf, paragraphs } from './readable.js'
  let { value, depth = 0 } = $props()
  const LONG = 4000
  let mode = $state('readable')
  let inspectable = $derived(depth === 0 && value !== null && typeof value === 'object')
  let raw = $derived.by(() => {
    if (!inspectable || mode !== 'json') return null
    try {
      const text = JSON.stringify(value, null, 2)
      return typeof text === 'string' ? { text } : { error: 'Could not show JSON.' }
    } catch {
      return { error: 'Could not show JSON.' }
    }
  })
  let kind = $derived(kindOf(value))
  let opened = $state({})
  let more = $state(false)
  const segments = (s) => s.split('`').map((text, i) => ({ text, code: i % 2 === 1 }))
</script>

{#snippet text(s)}
  {@const shown = more || s.length <= LONG ? s : s.slice(0, LONG)}
  {@const parts = paragraphs(shown)}
  {#if parts.length === 1}<span class="inline">{#each segments(parts[0]) as seg}{#if seg.code}<code>{seg.text}</code>{:else}{seg.text}{/if}{/each}</span>
  {:else}{#each parts as p}<p class="para">{#each segments(p) as seg}{#if seg.code}<code>{seg.text}</code>{:else}{seg.text}{/if}{/each}</p>{/each}{/if}
  {#if s.length > LONG && !more}<button type="button" class="more" onclick={() => (more = true)}>Show all {s.length.toLocaleString()} characters</button>{/if}
{/snippet}

{#if inspectable}
  <div class="view-mode"><Segmented options={[['readable', 'Readable'], ['json', 'JSON']]} value={mode} label="Result view" small onchange={(value) => mode = value} /></div>
  <div hidden={mode !== 'readable'}>{@render readable()}</div>
  {#if mode === 'json'}
    {#if raw?.error}<p class="raw-error" role="status">{raw.error}</p>{:else}<pre class="raw">{raw?.text}</pre>{/if}
  {/if}
{:else}
  {@render readable()}
{/if}

{#snippet readable()}
{#if depth > 6}
  <code class="deep">{JSON.stringify(value)}</code>
{:else if kind === 'empty'}
  <span class="empty">—</span>
{:else if kind === 'scalar'}
  <span>{String(value)}</span>
{:else if kind === 'text'}
  {@render text(value)}
{:else if kind === 'link'}
  {@const l = linkOf(value)}
  <a href={l.url} target="_blank" rel="noopener noreferrer">{l.text}</a>{#if l.text !== l.url}<span class="host"> {l.host}</span>{/if}
{:else if kind === 'list'}
  <ul class="list">{#each value as item}<li><JsonView value={item} depth={depth + 1} /></li>{/each}</ul>
{:else if kind === 'records'}
  <div class="records">
    {#each value as r, i}
      <details class="rec" ontoggle={(e) => (opened = { ...opened, [i]: e.currentTarget.open })}>
        <summary><b>{recordTitle(r, i)}</b><span class="line">{recordLine(r)}</span><span class="chev" aria-hidden="true">›</span></summary>
        {#if opened[i]}<div class="body"><JsonView value={r} depth={depth + 1} /></div>{/if}
      </details>
    {/each}
  </div>
{:else if Object.keys(value).length === 1 && ['records', 'list', 'object'].includes(kindOf(Object.values(value)[0]))}
  <!-- One field holding a collection reads best without a label column. -->
  {@const [k, v] = Object.entries(value)[0]}
  <p class="caption">{fieldLabel(k)}{#if Array.isArray(v)}{' · ' + v.length}{/if}</p>
  <JsonView value={v} depth={depth + 1} />
{:else}
  <dl class="kv">
    {#each Object.entries(value) as [k, v]}<dt>{fieldLabel(k)}</dt><dd><JsonView value={v} depth={depth + 1} /></dd>{/each}
  </dl>
{/if}

{/snippet}

<style>
  .view-mode { margin-bottom: var(--s2); }
  .raw { margin: 0; max-height: min(60vh, 560px); overflow: auto; white-space: pre-wrap; overflow-wrap: anywhere; font-family: var(--mono); font-size: var(--t-sm); line-height: 1.5; padding: var(--s3); border-radius: var(--r-sm); background: var(--side); color: var(--ink-2); }
  .raw-error { color: var(--coral); font-size: var(--t-sm); }

  .kv { display: grid; grid-template-columns: minmax(110px, 180px) 1fr; gap: 8px 16px; margin: 0; }
  .kv dt { color: var(--muted); font-size: var(--t-sm); padding-top: 2px; overflow-wrap: anywhere; }
  .kv dd { margin: 0; min-width: 0; }
  .para { margin: 0 0 6px; line-height: 1.55; color: var(--ink-2); overflow-wrap: anywhere; }
  .para:last-of-type { margin-bottom: 0; }
  .inline { line-height: 1.55; color: var(--ink-2); overflow-wrap: anywhere; white-space: pre-line; }
  code { font-family: var(--mono); font-size: 0.9em; background: var(--raise); padding: 0 4px; border-radius: 4px; }
  .deep { display: block; white-space: pre-wrap; overflow-wrap: anywhere; color: var(--muted); }
  .empty { color: var(--faint); }
  .caption { margin: 0 0 8px; color: var(--muted); font-size: var(--t-sm); }
  .host { color: var(--faint); font-size: var(--t-sm); }
  a { color: var(--iris); text-decoration: none; overflow-wrap: anywhere; }
  @media (hover: hover) { a:hover { text-decoration: underline; } }
  .list { margin: 0; padding-left: 18px; color: var(--ink-2); line-height: 1.5; }
  .records { display: flex; flex-direction: column; gap: 6px; }
  .rec { border: 1px solid var(--line-soft); border-radius: 10px; background: var(--side); }
  .rec summary { list-style: none; display: grid; grid-template-columns: minmax(80px, 180px) 1fr auto; gap: 12px; padding: 10px 14px; cursor: pointer; align-items: baseline; }
  .rec summary::-webkit-details-marker { display: none; }
  .line { color: var(--ink-2); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; min-width: 0; }
  .chev { color: var(--faint); transition: transform var(--motion-fast) var(--ease); }
  .rec[open] > summary .chev { transform: rotate(90deg); }
  .body { padding: 0 14px 12px; }
  .more { color: var(--iris); font-size: var(--t-sm); padding: 2px 0; }
  @media (max-width: 600px) {
    .kv { grid-template-columns: 1fr; gap: 2px; }
    .kv dd { margin-bottom: 8px; }
    .rec summary { grid-template-columns: 1fr auto; }
    .line { grid-column: 1 / 2; white-space: normal; }
  }
</style>
