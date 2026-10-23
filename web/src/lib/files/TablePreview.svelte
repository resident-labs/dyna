<script>
  import { copyText } from '../../api.ts'
  import { tableCSV } from './tableCSV.ts'
  import { parseTable, TABLE_ROWS } from './preview.ts'
  let { text, delimiter = ',' } = $props()
  let header = $state(true)
  let parsed = $derived.by(() => {
    try { return { ...parseTable(text, delimiter), error: '' } }
    catch (e) { return { rows: [], error: e.message } }
  })
  let width = $derived(Math.max(0, ...parsed.rows.map((r) => r.length)))
  let headings = $derived(header ? parsed.rows[0] || [] : Array.from({ length: width }, (_, i) => `Column ${i + 1}`))
  let more = $derived(parsed.more || parsed.rows.length > TABLE_ROWS + (header ? 1 : 0))
  let rows = $derived(parsed.rows.slice(header ? 1 : 0, TABLE_ROWS + (header ? 1 : 0)))
  let copyRows = $derived(header && parsed.rows.length ? [parsed.rows[0], ...rows] : rows)
  let copyStatus = $state('')
  let copyGeneration = 0
  let copyTimer
  $effect(() => {
    text; delimiter; header
    copyGeneration++
    copyStatus = ''
    return () => clearTimeout(copyTimer)
  })
  async function copyVisible() {
    const current = ++copyGeneration
    clearTimeout(copyTimer)
    copyStatus = ''
    try {
      await copyText(tableCSV(copyRows, width))
      if (current !== copyGeneration) return
      copyStatus = 'Copied visible rows'
    } catch {
      if (current !== copyGeneration) return
      copyStatus = 'Could not copy'
    }
    copyTimer = setTimeout(() => { copyStatus = '' }, 2000)
  }
</script>

{#if parsed.error}
  <p class="error" role="alert">{parsed.error}</p>
{:else}
  <div class="table-info">
    <span>{rows.length.toLocaleString()}{more ? '+' : ''} rows <span class="dot">·</span> {width} columns</span>
    <label><input type="checkbox" bind:checked={header} /> First row is header</label>
    <button class="copy" disabled={!copyRows.length} onclick={copyVisible} title="Copy only the displayed rows and columns">Copy visible rows as CSV</button>
    <span class="copy-status" class:error={copyStatus === 'Could not copy'} role="status">{copyStatus}</span>
  </div>
  {#if more || parsed.columnsTruncated}<p class="limit">Showing up to {TABLE_ROWS} rows and 100 columns. Download the file for all data.</p>{/if}
  <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
  <div class="table-scroll" tabindex="0" role="region" aria-label="Table contents">
    <table>
      <thead><tr><th class="index" scope="col">#</th>{#each Array.from({ length: width }) as _, i}<th scope="col">{headings[i] || `Column ${i + 1}`}</th>{/each}</tr></thead>
      <tbody>
        {#each rows as row, i}
          <tr><th class="index" scope="row">{i + 1}</th>{#each Array.from({ length: width }) as _, col}<td class:number={/^-?\d[\d,.]*(%|e[+-]?\d+)?$/i.test(row[col] || '')}>{row[col] || ''}</td>{/each}</tr>
        {/each}
      </tbody>
    </table>
    {#if !rows.length}<p class="empty">This table has no data rows.</p>{/if}
  </div>
{/if}

<style>
  .table-info { display: flex; justify-content: space-between; flex-wrap: wrap; gap: 12px; padding: 16px 20px; font-size: 12px; color: var(--muted); border-bottom: 1px solid var(--line-soft); }
  .copy { min-height: 44px; padding: 8px 12px; border-radius: var(--r-xs); background: var(--raise); color: var(--ink); }
  .copy:disabled { opacity: 0.5; }
  .copy-status { color: var(--sage); align-self: center; }
  .copy-status.error { color: var(--coral); }
  .dot { padding: 0 5px; color: var(--faint); }
  label { display: flex; align-items: center; gap: 6px; cursor: pointer; }
  input { accent-color: var(--iris); margin: 0; }
  .table-scroll { flex: 1; min-height: 0; overflow: auto; }
  table { border-collapse: separate; border-spacing: 0; width: 100%; font-size: 13px; color: var(--ink-2); }
  th, td { text-align: left; padding: 10px 16px; border-bottom: 1px solid var(--line-soft); border-right: 1px solid var(--line-soft); min-width: 110px; max-width: 420px; overflow-wrap: anywhere; white-space: pre-wrap; vertical-align: top; }
  thead th { position: sticky; top: 0; background: var(--raise); color: var(--ink); font-weight: 500; z-index: 1; }
  .index { min-width: 45px; width: 45px; text-align: right; font: 11px var(--mono); color: var(--faint); background: var(--side); }
  tbody tr:nth-child(even) { background: rgba(255, 255, 255, 0.018); }
  tbody tr { transition: background-color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    tbody tr:hover { background: var(--hover); }
  }
  tbody tr:active { background: var(--select); }
  .number { text-align: right; font-variant-numeric: tabular-nums; }
  .limit, .error, .empty { padding: 12px 20px; margin: 0; font-size: 13px; color: var(--muted); }
  .error { color: var(--coral); }
  .limit { color: var(--gold); border-bottom: 1px solid var(--line-soft); }
</style>
