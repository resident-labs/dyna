<script lang="ts">
  import type { Modifiers, PhoneKey } from "../computerInput.ts";
  // The keys a phone keyboard lacks, in a row above it. Ctrl and Alt stay down for one key. The pointer press is
  // cancelled so the hidden text field keeps its focus and the keyboard stays open.
  let { held, onkey }: { held: Modifiers; onkey: (key: PhoneKey) => void } = $props();
  const keys: { key: PhoneKey; label: string; name?: string }[] = [
    { key: "esc", label: "Esc" }, { key: "tab", label: "Tab" }, { key: "ctrl", label: "Ctrl" }, { key: "alt", label: "Alt" },
    { key: "left", label: "←", name: "Left" }, { key: "up", label: "↑", name: "Up" }, { key: "down", label: "↓", name: "Down" }, { key: "right", label: "→", name: "Right" },
  ];
</script>

<div class="keys" role="toolbar" aria-label="Computer keys">
  {#each keys as { key, label, name } (key)}
    {@const modifier = key === "ctrl" || key === "alt"}
    <button type="button" class:held={modifier && held[key]} aria-pressed={modifier ? held[key] : undefined} aria-label={name ?? label}
      onpointerdown={event => event.preventDefault()} onclick={() => onkey(key)}>{label}</button>
  {/each}
</div>

<style>
  .keys { display: flex; flex: none; gap: 4px; padding: 6px 8px calc(6px + env(safe-area-inset-bottom)); border-top: 1px solid var(--line); background: var(--screen); }
  button { flex: 1 0 auto; min-width: 40px; height: 40px; padding: 0 8px; border: 0; border-radius: 6px; background: var(--screen-hover); color: var(--screen-text); font: 500 13px var(--mono); cursor: pointer; touch-action: manipulation; }
  button:active { background: var(--screen-line); }
  button.held { background: var(--screen-focus); color: var(--screen); }
  button:focus-visible { outline: 2px solid var(--screen-focus); outline-offset: 1px; }
</style>
