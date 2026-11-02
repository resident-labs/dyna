<script lang="ts">
  import type { Snippet } from "svelte";
  import Icon from "./Icon.svelte";
  // One thing a session holds in About: a goal, a responsibility, the room's hooks. Closed it takes one or two clipped
  // lines, so a long one never pushes the panel's essentials away, with a short aside such as an open-items count. Opening
  // it shows the rest in place, and the aside gives its width back to the text. It opens only if there is more to see:
  // `more` says there is a body, and a head the clip cut has more of its own. A row with neither is plain text with no
  // chevron and no hover.
  let { head, aside, children, more = false }: { head: Snippet; aside?: Snippet; children?: Snippet; more?: boolean } = $props();
  let open = $state(false), clipped = $state(false);
  let text: HTMLElement | undefined = $state();
  $effect(() => {
    if (!text || open) return;
    const line = text;
    const observer = new ResizeObserver(() => { clipped = line.scrollHeight > line.clientHeight + 1; });
    observer.observe(line);
    return () => observer.disconnect();
  });
  let opens = $derived(more || clipped);
</script>

{#snippet line()}
  <span class="head" class:clip={!open} bind:this={text}>{@render head()}</span>
  {#if aside && !open}<span class="aside">{@render aside()}</span>{/if}
{/snippet}
{#if opens}
  <button type="button" class="row" aria-expanded={open} onclick={() => { open = !open; }}><Icon name="chevron" size={12} />{@render line()}</button>
  {#if open && children}<div class="body">{@render children()}</div>{/if}
{:else}<div class="row flat">{@render line()}</div>{/if}

<style>
  /* The wash reaches past the text, and the margin takes it back, so the row's text lines up with the panel's. */
  .row { display: grid; grid-template-columns: 12px minmax(0, 1fr) auto; column-gap: 8px; align-items: start; box-sizing: border-box; width: calc(100% + 16px); min-height: 28px; margin: 0 -8px; padding: 4px 8px;
    border: 0; border-radius: 6px; background: none; color: var(--text); font: inherit; line-height: 20px; text-align: left; }
  .flat { grid-template-columns: minmax(0, 1fr) auto; }
  button.row { cursor: pointer; }
  .row :global(svg) { margin-top: 4px; color: var(--muted); transition: transform 150ms ease-out; }
  .row[aria-expanded="false"] :global(svg) { transform: rotate(-90deg); }
  .head { min-width: 0; overflow-wrap: break-word; }
  .clip { display: -webkit-box; -webkit-box-orient: vertical; -webkit-line-clamp: 2; line-clamp: 2; overflow: hidden; }
  .aside { color: var(--muted); font-size: 12.5px; font-variant-numeric: tabular-nums; white-space: nowrap; }
  .body { display: grid; gap: 8px; padding: 4px 0 8px 20px; }
  button.row:focus-visible { outline: 2px solid var(--focus); outline-offset: -2px; }
  @media (hover: hover) { button.row:hover { background: var(--hover); } }
  @media (hover: none) { button.row { min-height: 40px; align-content: center; } }
  @media (prefers-reduced-motion: reduce) { .row :global(svg) { transition: none; } }
</style>
