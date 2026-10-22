<script>
  let { ctrl = false, alt = undefined, onkey, label = 'Terminal keys' } = $props()
  const keys = [
    { k: 'esc', label: 'Esc' }, { k: 'tab', label: 'Tab' },
    { k: 'ctrl', label: 'Ctrl' }, { k: 'alt', label: 'Alt' },
    { k: 'left', label: '←', aria: 'Left' }, { k: 'up', label: '↑', aria: 'Up' },
    { k: 'down', label: '↓', aria: 'Down' }, { k: 'right', label: '→', aria: 'Right' },
  ]
</script>

<div class="keys" role="toolbar" aria-label={label}>
  {#each keys.filter((k) => k.k !== 'alt' || alt !== undefined) as k}
    <button
      class:held={(k.k === 'ctrl' && ctrl) || (k.k === 'alt' && alt)}
      aria-pressed={k.k === 'ctrl' ? ctrl : k.k === 'alt' ? alt : undefined}
      aria-label={k.aria ?? k.label}
      onpointerdown={(e) => e.preventDefault()}
      onclick={() => onkey(k.k)}
    >{k.label}</button>
  {/each}
</div>

<style>
  .keys { display: flex; gap: 4px; padding: 6px 4px calc(6px + env(safe-area-inset-bottom)); background: var(--side); border-top: 1px solid var(--line-soft); flex: none; overflow-x: auto; scrollbar-width: none; }
  .keys button { flex: 1 0 auto; min-width: 40px; height: 44px; padding: 0 6px; border-radius: 8px; background: var(--raise); color: var(--ink-2); font-size: var(--t-body); font-weight: 600; font-family: var(--mono); touch-action: manipulation; }
  .keys button:active { background: var(--raise-hover); }
  .keys button.held { background: var(--iris); color: #18161d; }
</style>
