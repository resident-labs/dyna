<script lang="ts">
  import { tick, type Snippet } from "svelte";
  import Icon from "../Icon.svelte";
  // A Settings list page (spec 5.13): the page's bar, its list, and the open item's detail beside the list, 400 px wide.
  // Below 760 px of pane the detail is a screen of its own with a back arrow, in place of the bar and the list, which
  // stays mounted so its search and scroll wait for the way back. Esc closes the detail, and focus returns to the list.
  // list gets drill (the narrow layout) and select, which opens an item; an item opened as a screen, or with Enter,
  // takes the focus into its detail. With nothing open, a wide pane shows summary in the detail's place.
  let { bar, label, title, open, onselect, onclose, list, detail, summary }: {
    bar: Snippet; label: string; title: string; open: boolean; onselect: (key: string, open: boolean) => void; onclose: () => void;
    list: Snippet<[{ drill: boolean; select: (key: string, open: boolean) => void }]>; detail: Snippet; summary?: Snippet;
  } = $props();
  let width = $state(0);
  let page: HTMLDivElement | undefined = $state(), side: HTMLElement | undefined = $state();
  let drill = $derived(width > 0 && width < 760);

  async function select(key: string, focus: boolean) {
    onselect(key, focus);
    if (!focus) return;
    await tick();
    // A text field first would put the cursor in it and raise a phone's keyboard, so then the detail itself takes the focus.
    const first = side?.querySelector<HTMLElement>(".body :is(input, button, select, textarea, a[href])");
    (first && !first.matches("textarea, input:not([type=checkbox], [type=radio], [type=button], [type=submit])") ? first : side)?.focus();
  }
  // A close can land later than its call, as a phone's goes back in history first, and the list is hidden until the
  // detail has gone, so the list takes the focus once open has turned false: after Esc or the arrow, and as well after
  // a page closes the detail itself, as a removal does, or Back leaves a page's form.
  let was = false;
  $effect(() => {
    const closed = was && !open;
    was = open;
    if (closed) void tick().then(() => page?.querySelector<HTMLElement>("[role=listbox]")?.focus());
  });
  function keydown(event: KeyboardEvent) {
    if (event.key !== "Escape" || !open || event.defaultPrevented) return;
    event.preventDefault();
    onclose();
  }
</script>

<!-- svelte-ignore a11y_no_static_element_interactions: Esc reaches here from the list and the detail inside. -->
<div class="page" bind:this={page} bind:clientWidth={width} onkeydown={keydown}>
  {#if !drill || !open}{@render bar()}{/if}
  <div class="split" class:drill class:open>
    <div class="list-side">{@render list({ drill, select })}</div>
    {#if open || !drill}<aside class="detail" aria-label={open ? title : label} tabindex="-1" data-f6 bind:this={side}>
      {#if open}<header class="bar">{#if drill}<button type="button" class="round" aria-label={`Back to ${label}`} title="Back (Esc)" onclick={onclose}><Icon name="backChevron" size={20} /></button>{/if}
        <strong>{title}</strong>{#if !drill}<button type="button" class="round close" aria-label="Close" title="Close (Esc)" onclick={onclose}><Icon name="close" /></button>{/if}</header>
        <div class="body">{@render detail()}</div>
      {:else}<header class="bar"><strong class="quiet">{label}</strong></header><div class="body">{@render summary?.()}<p class="small muted">Pick a row to see its settings.</p></div>{/if}
    </aside>{/if}
  </div>
</div>

<style>
  .page { flex: 1; min-height: 0; display: flex; flex-direction: column; }
  .split { position: relative; flex: 1; min-height: 0; display: grid; grid-template-columns: minmax(0, 1fr) 400px; }
  .split.drill { grid-template-columns: minmax(0, 1fr); }
  .list-side { min-width: 0; min-height: 0; display: flex; flex-direction: column; }
  .list-side > :global(*) { flex: 1; }
  /* Hidden, not removed and not display: none, so the list keeps its search, its window and its scroll position. */
  .split.drill.open .list-side { position: absolute; inset: 0; visibility: hidden; }
  .detail { min-width: 0; min-height: 0; display: flex; flex-direction: column; border-left: 1px solid var(--line); background: var(--bg); }
  .detail:focus { outline: none; }
  .drill .detail { border-left: 0; }
  .bar strong { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 16px; }
  .quiet { color: var(--muted); font-weight: 500; }
  .round { display: grid; place-items: center; flex: none; width: 40px; height: 40px; margin: 0 -8px 0 -10px; padding: 0; border: 0; border-radius: 8px; background: none; color: var(--muted); cursor: pointer; }
  .round:hover { color: var(--text); }
  .close { margin: 0 -10px 0 auto; }
  .body { flex: 1; min-height: 0; overflow-y: auto; display: grid; gap: 18px; align-content: start; padding: 16px 20px 32px; }
  /* What a detail draws: the page's help in small muted lines, and settings stacked in one card, each a name, its
     key, one line of help, and its control at the right. */
  .body :global(p) { margin: 0; }
  .body :global(.small) { font-size: 12.5px; }
  .body :global(.muted) { color: var(--muted); }
  /* A summary's facts, each "· fact", break between one another and never inside one. */
  .body :global(.fact) { white-space: nowrap; }
  .body :global(.stack) { display: grid; }
  .body :global(.set) { display: grid; grid-template-columns: minmax(0, 1fr) auto; gap: 2px 12px; align-items: center; padding: 10px 12px; border: 1px solid var(--line); background: var(--raised); cursor: pointer; }
  .body :global(.set:first-child) { border-radius: 12px 12px 0 0; }
  .body :global(.set:last-child) { border-radius: 0 0 12px 12px; }
  .body :global(.set:only-child) { border-radius: 12px; }
  .body :global(.set + .set) { border-top: 0; }
  .body :global(.set .name) { font-size: 14px; font-weight: 600; }
  .body :global(.set .key) { color: var(--muted); font-size: 12.5px; font-weight: 400; }
  .body :global(.set .help) { grid-column: 1; color: var(--muted); font-size: 12.5px; line-height: 1.35; }
  .body :global(.set > :nth-child(2)) { grid-row: 1 / span 2; grid-column: 2; }
  .body :global(.actions) { display: flex; flex-wrap: wrap; gap: 8px; }
  .body :global(.section) { display: grid; gap: 8px; min-width: 0; }
  /* An item's facts, a label and a value to a line, and its removal at the end, apart, with whom it affects. */
  .body :global(.facts) { grid-template-columns: 110px minmax(0, 1fr); gap: 6px 12px; font-size: 13.5px; }
  .body :global(.danger-zone) { display: flex; align-items: center; justify-content: space-between; gap: 12px; padding-top: 14px; border-top: 1px solid var(--line); }
  @media (max-width: 640px) { .body { padding: 14px 16px 32px; } .body :global(.facts) { grid-template-columns: 96px minmax(0, 1fr); } }
</style>
