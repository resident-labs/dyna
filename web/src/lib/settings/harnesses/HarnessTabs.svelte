<script lang="ts">
  import { nextTab } from './state.ts'

  // The two views of the pane. Arrow, Home and End keys move between them, as a
  // tablist should, and keep the focus on the tab that is now shown.
  let { tab = $bindable('harnesses') }: { tab?: string } = $props()

  function tabKey(e: KeyboardEvent) {
    const next = nextTab(tab, e.key)
    if (!next) return
    e.preventDefault()
    tab = next
    document.getElementById('harnesses-' + tab + '-tab')?.focus()
  }
</script>

<div class="tabs" role="tablist" tabindex="-1" aria-label="Harness views" onkeydown={tabKey}>
  <button
    id="harnesses-harnesses-tab"
    role="tab"
    class:on={tab === 'harnesses'}
    aria-selected={tab === 'harnesses'}
    aria-controls="harnesses-panel"
    tabindex={tab === 'harnesses' ? 0 : -1}
    onclick={() => (tab = 'harnesses')}
  >
    Harnesses
  </button>
  <button
    id="harnesses-tools-tab"
    role="tab"
    class:on={tab === 'tools'}
    aria-selected={tab === 'tools'}
    aria-controls="tools-panel"
    tabindex={tab === 'tools' ? 0 : -1}
    onclick={() => (tab = 'tools')}
  >
    MCP servers &amp; skills
  </button>
</div>

<style>
  .tabs { display: flex; gap: var(--s1); border-bottom: 1px solid var(--line); margin: var(--s5) 0 0; }
  .tabs button { padding: var(--s2) 14px; font-size: var(--t-ui); color: var(--muted); border-bottom: 2px solid transparent; margin-bottom: -1px; }
  .tabs button.on { color: var(--ink); border-color: var(--iris); }
</style>
