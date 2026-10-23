<script lang="ts">
  import { preferenceStatusLabel, type HarnessRow } from './state.ts'
  import type { PreferencePreview } from '../../api/harness.ts'

  // Settings import of the selected harness: a preview of the safe preference
  // keys, then the confirmed copy. The pane runs both calls.
  let {
    row,
    preview = null,
    allowedCount = 0,
    busy = false,
    onpreview,
    onimport,
  }: {
    row: HarnessRow
    preview?: PreferencePreview | null
    allowedCount?: number
    busy?: boolean
    onpreview: () => void
    onimport: () => void
  } = $props()
</script>

<div class="sec">
  <h3 class="section-label">Settings import</h3>
  <p class="muted small" style="margin:0 0 var(--s2)">
    Copy safe preferences from your own {row.meta.name} setup. Nothing is copied until you confirm.
  </p>
  {#if !row.activeAccount}
    <p class="muted small" style="margin:0">Sign in to preview and import personal preferences.</p>
  {:else}
    {#if preview}
      {#if ['grok', 'pi'].includes(row.id)}<p class="muted small">Preview only. This harness cannot copy preferences yet.</p>{/if}
      <div class="preview-box">
        {#each preview.keys ?? [] as item (item.key)}
          <div class="kv">
            <span><code>{item.key}</code></span>
            <span class="tag {item.status === 'allowed' ? 'ok' : 'no'}">{preferenceStatusLabel(item.status)}</span>
          </div>
        {:else}
          <p class="muted small" style="margin:6px 0">No personal preference keys found.</p>
        {/each}
      </div>
      <div class="btn-group" style="margin-top:10px">
        <button
          class="btn"
          disabled={busy || allowedCount === 0 || ['grok', 'pi'].includes(row.id)}
          onclick={onimport}
        >
          Confirm import ({allowedCount})
        </button>
        <button class="btn quiet" disabled={busy} onclick={onpreview}>Refresh preview</button>
      </div>
    {:else}
      <button class="btn quiet" disabled={busy} onclick={onpreview}>Preview settings import</button>
    {/if}
  {/if}
</div>

<style>
  .btn-group { display: flex; flex-wrap: wrap; align-items: center; gap: var(--s2); }
  .muted { color: var(--muted); }
  .small { font-size: var(--t-sm); }
  .sec { border-top: 1px solid var(--line); padding: var(--s4) 0; }
  .sec .section-label { margin-top: 0; }
  .kv { flex-wrap: wrap; display: flex; align-items: center; justify-content: space-between; gap: var(--s3); padding: 6px 0; font-size: var(--t-ui); }
  .tag { font-size: var(--t-cap); color: var(--muted); border: 1px solid var(--line-soft); border-radius: 4px; padding: 1px 6px; }
  .tag.ok { color: var(--sage); }
  .tag.no { color: var(--faint); text-decoration: line-through; }
  .preview-box { background: var(--main); border: 1px solid var(--line-soft); border-radius: 6px; padding: var(--s1) var(--s3); }
  code { font-family: var(--mono); font-size: var(--t-cap); }
</style>
