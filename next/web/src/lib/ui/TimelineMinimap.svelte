<!-- Ported from T3 Code (apps/web/src/components/chat/TimelineMinimap.tsx), MIT License, Copyright (c) 2026 T3 Tools Inc. See next/third_party/t3code/LICENSE. -->
<script lang="ts" module>
  export type MinimapTurn = { id: string; userText: string | null; assistantText: string | null; visible?: boolean };
</script>

<script lang="ts">
  import Icon from "./Icon.svelte";

  // Dyna's caller owns turn ordering, viewport tracking, and scroll restoration.
  let { turns, current, onselect }: {
    turns: readonly MinimapTurn[];
    current: string | null;
    onselect: (id: string) => void;
  } = $props();

  let active: number | null = $state(null);
  let rail: HTMLDivElement;
  let currentIndex = $derived(turns.findIndex(turn => turn.id === current));
  let selected = $derived(active === null ? undefined : turns[active]);
  let height = $derived(`min(${Math.max(1, (turns.length - 1) * 8)}px, calc(100vh - 18rem))`);

  function choose(index: number) {
    const turn = turns[index];
    if (turn) onselect(turn.id);
  }
  function keydown(event: KeyboardEvent) {
    if (active === null) return;
    let next = active;
    if (event.key === "ArrowDown") next++;
    else if (event.key === "ArrowUp") next--;
    else if (event.key === "Home") next = 0;
    else if (event.key === "End") next = turns.length - 1;
    else return;
    event.preventDefault();
    next = Math.max(0, Math.min(turns.length - 1, next));
    active = next;
    rail.querySelectorAll<HTMLButtonElement>(".mark")[next]?.focus();
  }
</script>

{#if turns.length >= 2}
  <nav class="minimap" aria-label="Turn navigation">
    <div class="stack">
      <button class="step previous" aria-label="Previous turn" disabled={currentIndex <= 0} onclick={() => choose(currentIndex - 1)}><Icon name="chevron" size={14} /></button>
      <div class="rail" bind:this={rail} style:height onkeydown={keydown} role="group" aria-label="Turns">
        {#each turns as turn, index (turn.id)}
          <button class="mark" class:current={index === currentIndex} class:visible={turn.visible} class:active={index === active}
            style:top={`${index * 100 / (turns.length - 1)}%`}
            aria-label={`Jump to turn ${index + 1}: ${turn.userText?.trim() || "Turn"}`}
            aria-current={index === currentIndex ? "location" : undefined}
            onmouseenter={() => active = index} onmouseleave={() => active = null}
            onfocus={() => active = index} onblur={() => active = null}
            onclick={() => choose(index)}><span></span></button>
        {/each}
        {#if selected && active !== null}
          <div class="preview" style:top={`${active * 100 / (turns.length - 1)}%`} class:first={active === 0} class:last={active === turns.length - 1}>
            <strong>{selected.userText?.trim() || "Turn"}</strong>
            {#if selected.assistantText}<span>{selected.assistantText.slice(0, 1500)}</span>{/if}
          </div>
        {/if}
      </div>
      <button class="step next" aria-label="Next turn" disabled={currentIndex < 0 || currentIndex >= turns.length - 1} onclick={() => choose(currentIndex + 1)}><Icon name="chevron" size={14} /></button>
    </div>
  </nav>
{/if}

<style>
  /* The rail sits at the pane's left edge, as in T3 Code, so the gutter separates it from the reading column. */
  .minimap { position: absolute; inset: 0 auto 0 0; width: 72px; z-index: 10; pointer-events: none; }
  .stack { position: absolute; top: 50%; left: 12px; transform: translateY(-50%); width: 32px; pointer-events: auto; }
  .rail { position: relative; min-height: 8px; }
  .rail::before { content: ""; position: absolute; left: 12px; height: 100%; width: 1px; background: var(--line); opacity: .15; }
  button { color: var(--muted); border: 0; padding: 0; background: transparent; cursor: pointer; }
  button:focus-visible { outline: 2px solid var(--focus); outline-offset: 2px; }
  .mark { position: absolute; left: 0; width: 32px; height: 8px; transform: translateY(-50%); display: flex; align-items: center; }
  .mark span { height: 2px; width: 8px; border-radius: 2px; background: currentColor; opacity: .35; transition: width 150ms, opacity 150ms; }
  .mark.visible span, .mark.current span { opacity: .8; }
  .mark.active span { width: 24px; opacity: 1; }
  /* The steps float off the rail's ends and show only while the pointer or focus is on the rail. */
  .step { position: absolute; left: 4px; display: flex; justify-content: center; width: 24px; height: 24px; opacity: 0; transform: translateX(-50%); transition: opacity 150ms; }
  .previous { bottom: calc(100% + 2px); } .next { top: calc(100% + 2px); }
  .stack:hover .step, .stack:focus-within .step { opacity: .7; }
  .stack:hover .step:disabled, .stack:focus-within .step:disabled { opacity: .2; cursor: default; }
  .stack .step:hover:enabled { color: var(--text); opacity: 1; }
  .previous :global(svg) { transform: rotate(180deg); }
  .preview { position: absolute; left: 32px; width: 320px; max-width: calc(100vw - 60px); transform: translateY(-50%); padding: 12px; border-radius: 12px; background: var(--raised); box-shadow: 0 8px 24px var(--shadow-lg); pointer-events: none; color: var(--text); font-size: 14px; line-height: 20px; }
  .preview.first { transform: none; } .preview.last { transform: translateY(-100%); }
  .preview strong { display: block; overflow: hidden; white-space: nowrap; text-overflow: ellipsis; }
  .preview span { display: -webkit-box; margin-top: 4px; color: var(--muted); overflow: hidden; -webkit-line-clamp: 3; -webkit-box-orient: vertical; white-space: pre-wrap; overflow-wrap: anywhere; }
  @media (pointer: coarse) { .minimap { display: none; } }
  @media (prefers-reduced-motion: reduce) { .mark span, .step { transition: none; } }
</style>
