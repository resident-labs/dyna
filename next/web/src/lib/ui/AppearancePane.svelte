<script lang="ts">
  import { type Snippet } from "svelte";
  import { choose, theme } from "../theme.svelte.ts";
  import { carbon, themes } from "../themes.ts";
  import Icon from "./Icon.svelte";
  // Settings > Appearance. Each card draws a small Home from that theme's own tokens, scoped to the card, so a new
  // theme needs no picker work. The cards are one radio group: a click or an arrow key applies the theme at once.
  let { bar }: { bar: Snippet<[string, number | undefined, Snippet | undefined]> } = $props();
  let cards: HTMLElement[] = $state([]);
  const scoped = (tokens: Readonly<Record<string, string>>) => Object.entries(tokens).map(([name, value]) => `${name}: ${value}`).join("; ");
  function key(event: KeyboardEvent, index: number) {
    const step = { ArrowRight: 1, ArrowDown: 1, ArrowLeft: -1, ArrowUp: -1 }[event.key];
    if (!step) return;
    event.preventDefault();
    const next = (index + step + themes.length) % themes.length;
    choose(themes[next]);
    cards[next]?.focus();
  }
</script>

{@render bar("Appearance", undefined, undefined)}
<div class="body">
  <p class="lead">Your choice stays on this browser.</p>
  <div class="grid" role="radiogroup" aria-label="Theme">
    {#each themes as item, index (item.id)}{@const on = theme().id === item.id}
      <div class="pick" role="radio" aria-checked={on} aria-label={item.label} tabindex={on ? 0 : -1} bind:this={cards[index]}
        onclick={() => choose(item)} onkeydown={event => key(event, index)}>
        <div class="home" class:on style={scoped(item.tokens)} aria-hidden="true">
          <i class="m-rail">{#each [25, 150, 205, 285] as hue (hue)}<b style={`--hue: ${hue}`}></b>{/each}</i>
          <i class="m-page"><i class="m-title"></i>
            <i class="m-card"><i class="m-lines"><i></i><i class="m-short"></i></i><i class="m-pill"></i></i>
            <i class="m-card"><i class="m-lines"><i class="m-mid"></i></i><i class="m-dot m-fault"></i><i class="m-dot m-wait"></i></i>
            <i class="m-meters"><i style="--fill: 30%"></i><i style="--fill: 45%"></i><i class="m-warm" style="--fill: 92%"></i></i></i>
          {#if on}<span class="check"><Icon name="check" size={14} /></span>{/if}
        </div>
        <span class="name">{item.label}{#if item === carbon}<span class="badge">Default</span>{/if}</span>
      </div>
    {/each}
  </div>
</div>

<style>
  .body { flex: 1; min-height: 0; width: min(1040px, 100%); padding: 20px 24px; overflow-y: auto; }
  .lead { margin: 0 0 16px; color: var(--muted); font-size: 14px; }
  .grid { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 20px 16px; }
  .pick { display: grid; gap: 8px; min-width: 0; border-radius: 12px; cursor: pointer; }
  .pick:focus-visible { outline: none; }
  .pick:focus-visible .home { outline: 2px solid var(--focus); outline-offset: 2px; }
  /* The card is painted with its own theme's tokens, set on this element. */
  .home { position: relative; display: grid; grid-template-columns: 16px minmax(0, 1fr); aspect-ratio: 5 / 2; overflow: hidden;
    border: 1px solid var(--line); border-radius: 10px; background: var(--bg); }
  .pick:hover .home { box-shadow: 0 0 0 2px var(--selected); }
  /* A ring and a check: a ring alone reads as the focus ring. */
  .home.on { outline: 2px solid var(--focus); outline-offset: 3px; }
  i, b { display: block; font-style: normal; }
  .m-rail { display: grid; align-content: start; gap: 4px; padding: 7px 3px; border-right: 1px solid var(--line); background: var(--sidebar); }
  .m-rail b { aspect-ratio: 1; border-radius: 3px; background: oklch(var(--id-face) var(--hue) / calc(var(--id-alpha) * 3)); }
  .m-page { display: grid; align-content: start; gap: 6px; padding: 9px 9px 0; }
  .m-title { width: 40%; height: 5px; border-radius: 3px; background: var(--text); }
  .m-card { display: flex; align-items: center; gap: 6px; padding: 6px 8px; border: 1px solid var(--line); border-radius: 5px; background: var(--raised); }
  .m-lines { flex: 1; display: grid; gap: 3px; }
  .m-lines i { height: 3px; border-radius: 2px; background: var(--muted); }
  .m-lines .m-short { width: 60%; }
  .m-lines .m-mid { width: 65%; }
  .m-pill { width: 13%; height: 9px; border-radius: 3px; background: var(--focus); }
  .m-dot { width: 5px; height: 5px; border-radius: 50%; }
  .m-dot.m-fault { background: var(--danger); }
  .m-dot.m-wait { background: var(--attention); }
  .m-meters { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 8px; }
  .m-meters i { height: 3px; border-radius: 2px; background: linear-gradient(var(--muted) 0 0) 0 0 / var(--fill) 100% no-repeat, var(--track); }
  .m-meters .m-warm { background: linear-gradient(var(--attention-fill) 0 0) 0 0 / var(--fill) 100% no-repeat, var(--track); }
  .check { position: absolute; top: 7px; right: 7px; display: grid; place-items: center; width: 22px; height: 22px; border-radius: 50%; background: var(--focus); color: var(--bg); }
  .name { display: flex; align-items: center; gap: 8px; font-size: 14px; font-weight: 600; }
  @media (max-width: 640px) {
    .body { padding: 12px 16px; }
    .grid { grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 16px 12px; }
    .home { aspect-ratio: 5 / 3; }
  }
</style>
