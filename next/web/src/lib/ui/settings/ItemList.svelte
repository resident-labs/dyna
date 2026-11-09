<script lang="ts" generics="T">
  import { tick, type Snippet } from "svelte";
  import Icon from "../Icon.svelte";
  import SearchBox from "./SearchBox.svelte";
  import { layoutRows, listKey, listView, noResults, visibleRange, type Chip, type Column, type Group, type Row } from "../../settings-list.ts";
  // The list of a Settings list page (spec 5.13): search, chips with counts, optional sortable columns, rows that fold
  // into groups, and the window: rows have fixed heights, so only those in view and 200 px each way are drawn, about 20
  // at any length. VirtualList is for rows of any height; these need their tops for the keys too.
  // items is undefined while loading. row draws an item; compact is a narrow list, where a row drops its columns.
  // selected is the open item's key. The list moves with ↑ ↓ Home End PgUp PgDn; on a wide pane a move opens the row
  // (onselect with open false), and Enter or a click on a narrow one opens it as a screen (open true). onkey takes
  // any other key for the row the list is on and says whether it used it. filter is the page's own narrowing from a
  // control in tools, such as Profiles' provider, and filtered names it while it narrows the list; onclear resets that
  // control when the no-results state clears the rest.
  let { items, failed = false, retry, key, text, noun, label, chips, columns, sort: start, group, folded: foldedFirst = [], row, tools, empty, selected, drill, onselect, onkey, filter, filtered, onclear }: {
    items: readonly T[] | undefined; failed?: boolean; retry?: () => void; key: (item: T) => string; text: (item: T) => string;
    noun: [one: string, many: string]; label: string; chips?: Chip<T>[]; columns?: Column<T>[]; sort?: { column: string; dir: 1 | -1 };
    group?: (item: T) => Group; folded?: string[]; row: Snippet<[T, boolean]>; tools?: Snippet; empty?: Snippet;
    selected?: string; drill: boolean; onselect: (key: string, open: boolean) => void; onkey?: (key: string, item: T) => boolean;
    filter?: (item: T) => boolean; filtered?: string; onclear?: () => void;
  } = $props();
  const id = $props.id();
  const head = 34, margin = 200;

  let query = $state("");
  // svelte-ignore state_referenced_locally
  // The chip by id: a page that derives its chips makes new ones on each change, and the pressed one must survive that.
  let chipId = $state(chips?.[0]?.id), sorting = $state.raw(start), folded = $state.raw(new Set(foldedFirst));
  let chip = $derived(chips?.find(each => each.id === chipId) ?? chips?.[0]);
  // A chip no item passes would only lead to an empty list, so it shows only while pressed. It counts over all items,
  // not the search's matches, so chips do not come and go while typing.
  let shownChips = $derived(chips?.filter(each => !each.test || !items || each.id === chipId || items.some(each.test)));
  let scroller: HTMLDivElement | undefined = $state();
  let scrollTop = $state(0), height = $state(0), width = $state(0);
  // The row the keys are on, by key, so a filter that moves it keeps it.
  let current: string | undefined = $state();

  let compact = $derived(width > 0 && width < (columns ? 500 : 420));
  let rowHeight = $derived(drill ? 60 : 52);
  let tracks = $derived(columns?.map(column => column.width).join(" "));
  let view = $derived(items && listView(items, { text, key, query, chip: chip ?? { id: "", label: "" }, chips, filter, folded, group,
    sort: sorting && columns?.find(column => column.id === sorting!.column) ? { column: columns.find(column => column.id === sorting!.column)!, dir: sorting.dir } : undefined }));
  let rows = $derived(view?.rows ?? []);
  let layout = $derived(layoutRows(rows, rowHeight, head));
  let range = $derived(visibleRange(layout.tops, scrollTop, height || 600, margin));
  const rowKey = (each: Row<T>) => each.kind === "item" ? each.key : `group ${each.group.id}`;
  let active = $derived(current === undefined ? -1 : rows.findIndex(each => rowKey(each) === current));
  // Each row's group head, found once per rows rather than on each scroll.
  let heads = $derived.by(() => { let last = -1; return group ? rows.map((each, index) => each.kind === "group" ? (last = index) : last) : []; });
  // The group of the first row in view stays at the top while its rows scroll under it.
  let pinned = $derived.by(() => {
    if (!group || scrollTop <= 0) return undefined;
    let first = range.start;
    while (first + 1 < rows.length && layout.tops[first + 1] <= scrollTop) first++;
    const index = heads[first];
    return index === undefined || index < 0 || layout.tops[index] === scrollTop ? undefined : { each: rows[index], index };
  });
  // Only drawn rows can be pointed at. A scroll, a resize or a fold can leave the active row out of the window; then the
  // listbox points at nothing until the next key, which scrolls the row back in.
  let pointed = $derived(active >= range.start && active < range.end ? `${id}-${active}` : undefined);
  const count = (n: number) => `${n.toLocaleString("en")} ${n === 1 ? noun[0] : noun[1]}`;

  // The open item is the row the keys are on.
  $effect.pre(() => { if (selected !== undefined) current = selected; });
  // A new search or chip starts at the top.
  $effect(() => { void query; void chip; void filter; if (scroller) scroller.scrollTop = 0; scrollTop = 0; });

  function show(index: number) {
    if (!scroller) return;
    const top = layout.tops[index], bottom = top + (rows[index].kind === "group" ? head : rowHeight), under = group ? head : 0;
    if (top < scroller.scrollTop + under) scroller.scrollTop = top - under;
    else if (bottom > scroller.scrollTop + scroller.clientHeight) scroller.scrollTop = bottom - scroller.clientHeight;
  }
  function fold(each: Row<T>) {
    if (each.kind !== "group") return;
    const next = new Set(folded);
    if (!next.delete(each.group.id)) next.add(each.group.id);
    folded = next;
    current = rowKey(each);
  }
  function move(index: number, open: boolean) {
    const each = rows[index];
    current = rowKey(each);
    show(index);
    if (each.kind === "item" && (open || !drill)) onselect(each.key, open);
  }
  function keydown(event: KeyboardEvent) {
    if (event.ctrlKey || event.metaKey || event.altKey) return;
    const action = listKey(event.key, active, rows, Math.max(1, Math.floor((height || 600) / rowHeight) - 1));
    const each = rows[active];
    if (action) {
      event.preventDefault();
      if ("fold" in action) fold(rows[action.fold]);
      else move("move" in action ? action.move : action.open, "open" in action);
    } else if (each?.kind === "item" && onkey?.(event.key, each.item)) event.preventDefault();
  }
  function click(event: MouseEvent, index: number) {
    // A control in the row does its own work.
    if ((event.target as Element).closest("input, button, a, select")) return;
    scroller?.focus({ preventScroll: true });
    const each = rows[index];
    if (each.kind === "group") fold(each);
    else move(index, drill);
  }
  async function first() {
    scroller?.focus();
    if (active < 0) { const index = rows.findIndex(each => each.kind === "item"); if (index >= 0) { await tick(); move(index, false); } }
  }
  function clear() { query = ""; chipId = chips?.[0]?.id; onclear?.(); }
</script>

<!-- The column is an F6 stop (spec 5.13); F6 hands its focus to the list, or to the first control while the list is not there. -->
<div class="column" data-page-ready={!!items || failed} data-f6 tabindex="-1"
  onfocus={event => { if (event.target === event.currentTarget) (scroller ?? event.currentTarget.querySelector<HTMLElement>("input:enabled, button:enabled"))?.focus(); }}>
  <div class="tools">
    <SearchBox bind:value={query} slash label={`Search ${noun[1]}`} placeholder={items?.length ? `Search ${count(items.length)}` : `Search ${noun[1]}`} disabled={!items} ondown={() => void first()} />
    {#if chips}<div class="chips" role="group" aria-label="Filter">{#each shownChips ?? [] as each (each.id)}
      <button type="button" class="chip" aria-pressed={chip?.id === each.id} disabled={!items} onclick={() => { chipId = each.id; }}>{each.label}{#if each.test && view}<span class="n">{view.counts[each.id].toLocaleString("en")}</span>{/if}</button>{/each}</div>{/if}
    {@render tools?.()}
  </div>
  <div class="frame" bind:clientWidth={width}>
    {#if columns && !compact && view}<div class="heads" style:grid-template-columns={tracks}>{#each columns as column (column.id)}
      {#if column.compare}{@const dir = sorting?.column === column.id ? sorting.dir : 0}<button type="button" aria-label={`Sort by ${column.label}${dir ? `, now ${dir > 0 ? "ascending" : "descending"}` : ""}`}
        onclick={() => { sorting = { column: column.id, dir: dir > 0 ? -1 : 1 }; }}>{column.label}{dir > 0 ? " ↑" : dir < 0 ? " ↓" : ""}</button>
      {:else}<span>{column.label}</span>{/if}{/each}</div>{/if}
    {#if failed && !items}<div class="state" role="alert">The {noun[1]} did not load. <button type="button" class="retry" onclick={retry}>Try again</button></div>
    {:else if !view}<div class="bones" aria-label={`Loading ${noun[1]}`}>{#each [0, 1, 2, 3, 4] as n (n)}<div class="bone-row"><i class="bone" style:width="24px" style:height="24px"></i><span class="lines"><i class="bone" style:width="{140 + n * 17 % 60}px" style:height="12px"></i><i class="bone" style:width="{220 - n * 23 % 70}px" style:height="10px"></i></span></div>{/each}</div>
    {:else if !items?.length}<div class="state"><span>No {noun[1]} yet.</span>{@render empty?.()}</div>
    {:else if !rows.length}<div class="state"><span>{noResults(noun[1], query, [chip?.test ? chip.label : "", filter ? filtered ?? "" : ""].filter(Boolean))}</span><button type="button" class="btn" onclick={clear}>Clear search and filters</button></div>
    {:else}
      <div class="list" role="listbox" tabindex="0" aria-label={label} aria-activedescendant={pointed} bind:this={scroller} bind:clientHeight={height}
        onscroll={() => { scrollTop = scroller!.scrollTop; }} onkeydown={keydown}>
        {#snippet heading(each: Row<T> & { kind: "group" }, index: number, top: number, sticky: boolean)}
          <div class="row group" class:folded={each.folded} class:active={!sticky && index === active} id={sticky ? undefined : `${id}-${index}`} role={sticky ? undefined : "option"} aria-selected={sticky ? undefined : false}
            aria-label={sticky ? undefined : `${each.group.title}, ${each.count}, ${each.folded ? "folded" : "open"}`} aria-setsize={sticky ? undefined : rows.length} aria-posinset={sticky ? undefined : index + 1}
            style:top="{top}px" style:height="{head}px" onclick={event => click(event, index)} aria-hidden={sticky || undefined}><Icon name="chevron" size={14} /><span>{each.group.title}</span><span class="n">{each.count}</span></div>
        {/snippet}
        {#if pinned}<div class="pin">{@render heading(pinned.each as Row<T> & { kind: "group" }, pinned.index, 0, true)}</div>{/if}
        <div class="space" style:height="{layout.total}px">
          {#each rows.slice(range.start, range.end) as each, offset (rowKey(each))}{@const index = range.start + offset}
            {#if each.kind === "group"}{@render heading(each, index, layout.tops[index], false)}
            {:else}<!-- svelte-ignore a11y_interactive_supports_focus, a11y_click_events_have_key_events: the listbox takes the focus and the keys, and points at the row with aria-activedescendant. -->
            <div class="row item" class:grid={columns && !compact} class:active={index === active} id={`${id}-${index}`} role="option" aria-selected={each.key === selected}
              aria-setsize={rows.length} aria-posinset={index + 1} style:top="{layout.tops[index]}px" style:height="{rowHeight}px" style:grid-template-columns={columns && !compact ? tracks : undefined}
              onclick={event => click(event, index)}>{@render row(each.item, compact)}</div>{/if}
          {/each}
        </div>
      </div>
    {/if}
  </div>
  <div class="foot">{#if view && items?.length}{view.shown === items.length ? count(items.length) : `${view.shown.toLocaleString("en")} of ${count(items.length)}`}{:else if !items && !failed}Loading {noun[1]}…{/if}</div>
</div>

<style>
  .column { min-width: 0; min-height: 0; display: flex; flex-direction: column; gap: 10px; padding: 16px 20px 12px 24px; }
  .column:focus { outline: none; }
  /* The search spans the list and the chips take the row below, so the toolbar keeps its shape at any count. */
  .tools { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; }
  .tools > :global(.search) { flex-basis: 100%; }
  .chips { display: flex; flex-wrap: wrap; gap: 4px; }
  .chip { height: 30px; padding: 0 10px; border: 1px solid var(--firm); border-radius: 6px; background: none; color: var(--muted); font: inherit; font-size: 13px; font-weight: 500; white-space: nowrap; cursor: pointer; }
  .chip[aria-pressed="true"] { border-color: var(--focus); color: var(--text); }
  .chip:disabled { opacity: 0.45; cursor: default; }
  .chip .n { margin-left: 4px; font-variant-numeric: tabular-nums; opacity: 0.8; }
  @media (hover: none) { .chip { height: 36px; } }
  .frame { flex: 1; min-height: 0; display: flex; flex-direction: column; border: 1px solid var(--line); border-radius: 12px; background: var(--raised); overflow: hidden; }
  .heads { flex: none; display: grid; align-items: end; gap: 10px; padding: 8px 14px 6px; border-bottom: 1px solid var(--line); color: var(--muted); font-size: 12px; }
  .heads > * { white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .heads > :not(:first-child) { text-align: center; }
  .heads button { padding: 0; border: 0; background: none; color: inherit; font: inherit; text-align: left; cursor: pointer; }
  .heads button:hover { color: var(--text); }
  .list { flex: 1; min-height: 0; overflow-y: auto; position: relative; outline: none; }
  .space { position: relative; }
  .pin { position: sticky; top: 0; z-index: 1; height: 0; }
  .row { position: absolute; left: 6px; right: 6px; display: flex; align-items: center; gap: 12px; padding: 0 8px; border-radius: 6px; cursor: pointer; }
  .row.item:hover { background: var(--hover); }
  .row[aria-selected="true"] { background: var(--selected); }
  /* On the selected wash an off switch's usual border all but vanishes in light, and the switch reads as disabled. */
  .row[aria-selected="true"] :global(input[role="switch"]:not(:checked)) { border-color: var(--muted); }
  .list:focus-visible .row.active { outline: 2px solid var(--focus); outline-offset: -2px; }
  .row.grid { display: grid; gap: 10px; }
  .group { left: 0; right: 0; gap: 8px; padding: 0 14px; border-radius: 0; color: var(--muted); font-size: 13px; font-weight: 600; }
  .pin .group { background: var(--raised); border-bottom: 1px solid var(--line); }
  .group .n { font-weight: 400; font-variant-numeric: tabular-nums; }
  /* A long group name, such as a room's, keeps to the head's one line. */
  .group span:not(.n) { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .group :global(svg) { transition: transform 120ms; }
  .group.folded :global(svg) { transform: rotate(-90deg); }
  /* What a page's row draws: two lines of text, and a cell for each column. */
  .row :global(.lines) { flex: 1; min-width: 0; display: flex; flex-direction: column; line-height: 1.3; }
  .row :global(.title) { display: flex; align-items: center; gap: 6px; min-width: 0; font-size: 14px; font-weight: 600; }
  .row :global(.title .name) { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .row :global(.line) { min-width: 0; color: var(--muted); font-size: 12.5px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .row :global(.cell) { display: grid; place-items: center; }
  .state { display: grid; justify-items: center; align-content: center; gap: 10px; flex: 1; padding: 40px 16px; color: var(--muted); font-size: 14px; text-align: center; }
  .bones { padding: 6px; }
  .bone-row { display: flex; align-items: center; gap: 12px; height: 52px; padding: 0 8px; }
  .bone-row .lines { display: grid; gap: 6px; }
  .bone { display: block; border-radius: 6px; background: var(--selected); }
  .foot { flex: none; min-height: 18px; padding: 0 4px; color: var(--muted); font-size: 12px; font-variant-numeric: tabular-nums; }
  @media (max-width: 640px) { .column { padding: 10px 12px 8px; } }
  @media (prefers-reduced-motion: reduce) { .group :global(svg) { transition: none; } }
</style>
