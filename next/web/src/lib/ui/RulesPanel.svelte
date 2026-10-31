<script lang="ts">
  import { onMount, tick } from "svelte";
  import { SvelteMap } from "svelte/reactivity";
  import { call } from "../client.ts";
  import { hue } from "../identity.ts";
  import { marks, ruleAge, ruleLimit, ruleParts, ruleTokens, splitRule } from "../rules.ts";
  import Avatar from "./Avatar.svelte";
  import Icon from "./Icon.svelte";
  import PageCrumb from "./PageCrumb.svelte";
  import RuleBox from "./RuleBox.svelte";
  import type { RoomStore } from "../room.svelte.ts";
  import type { RoomRule } from "../contract.gen.ts";
  // The room's rules as a numbered charter with a side column: what the rules cost, who set them and what changed last.
  // Numbers are stable: removing a rule never renumbers the others, and Undo adds the same text again under the next number.
  let { store, room, onback, element = $bindable() }: { store: RoomStore; room: string; onback: (event: MouseEvent) => void; element?: HTMLElement } = $props();
  // A removed rule keeps its place for 10 seconds as "Rule r4 removed · Undo".
  const removed = new SvelteMap<number, RoomRule>();
  const timers = new Map<number, ReturnType<typeof setTimeout>>();
  let rules = $derived([...store.rules.values()]);
  // Set by and Latest help only in a long list.
  let long = $derived(rules.length > 10);
  const author = (rule: RoomRule) => rule.by.kind === "owner" ? "owner" : rule.by.session ?? "dyna";
  const name = (rule: RoomRule) => rule.by.kind === "owner" ? "You" : rule.by.session ? store.names.get(rule.by.session) ?? "A closed session" : "Dyna";
  // The owner comes first, then the authors with the most rules.
  let authors = $derived([...Map.groupBy(rules, author).entries()].map(([key, set]) => ({ key, rule: set[0], total: set.length }))
    .sort((a, b) => Number(b.key === "owner") - Number(a.key === "owner") || b.total - a.total));
  let by: string | undefined = $state();
  let query = $state("");
  let wanted = $derived(query.trim());
  const found = (rule: RoomRule) => rule.text.toLowerCase().includes(wanted.toLowerCase()) || `r${rule.number}` === wanted.toLowerCase();
  // The filter applies only while its controls show.
  let matched = $derived(rules.filter(rule => (!long || by === undefined || author(rule) === by) && found(rule)));
  let rows = $derived([...matched.filter(rule => !removed.has(rule.number)), ...removed.values()].sort((a, b) => a.number - b.number));
  let latest = $derived(rules.toSorted((a, b) => Date.parse(b.at) - Date.parse(a.at) || b.number - a.number).slice(0, 4));
  let tokens = $derived(ruleTokens(rules.reduce((sum, rule) => sum + [...rule.text].length, 0)));
  let draft = $state("");
  let busy = $state(false);
  let failure = $state("");
  // The rule whose "…" menu is open, on screens without hover.
  let menuFor: number | undefined = $state();
  let copied: { number: number; word: string } | undefined = $state();
  let flash: number | undefined = $state();
  // The rule being edited keeps its number and its place. A rule that goes away closes its edit.
  let editing: { number: number; text: string } | undefined = $state();
  let list: HTMLElement | undefined = $state();
  let addHeight = $state(0);
  let find: HTMLInputElement | undefined = $state();
  let now = $state(Date.now());
  onMount(() => {
    element?.focus();
    const timer = setInterval(() => { now = Date.now(); }, 30_000);
    return () => { clearInterval(timer); for (const pending of timers.values()) clearTimeout(pending); };
  });

  async function add(text: string): Promise<boolean> {
    busy = true;
    failure = "";
    const added = await call("rule.add", { room: store.room, text }).catch(() => undefined);
    busy = false;
    // The rule shows when its rule.added event arrives, so the event stream alone orders adds and removes.
    if (!added) { failure = "The rule was not added. Try again."; return false; }
    return true;
  }
  async function submit() {
    const text = draft.trim();
    if (!text || busy) return;
    if (await add(text)) draft = "";
  }
  async function remove(rule: RoomRule) {
    menuFor = undefined;
    failure = "";
    const done = await call("rule.remove", { room: store.room, number: rule.number }).then(() => true, () => false);
    if (!done) {
      failure = `Rule r${rule.number} was not removed. Try again.`;
      return;
    }
    store.rules.delete(rule.number);
    removed.set(rule.number, rule);
    timers.set(rule.number, setTimeout(() => void expire(rule.number), 10_000));
    await tick();
    list?.querySelector<HTMLElement>(`[data-undo="${rule.number}"]`)?.focus();
  }
  // Focus on an Undo that goes away returns to the page.
  async function expire(number: number) {
    const held = list?.querySelector(`[data-undo="${number}"]`) === document.activeElement;
    removed.delete(number);
    timers.delete(number);
    await tick();
    if (held) element?.focus();
  }
  async function undo(rule: RoomRule) {
    if (!await add(rule.text)) return;
    clearTimeout(timers.get(rule.number));
    void expire(rule.number);
  }
  // A jump clears the filters that could hide its rule, then washes the rule for 2 seconds and moves focus to it.
  let flashing: ReturnType<typeof setTimeout> | undefined;
  async function jump(number: number) {
    by = undefined;
    query = "";
    await tick();
    const row = list?.querySelector<HTMLElement>(`[data-rule="${number}"]`);
    row?.scrollIntoView({ block: "center" });
    row?.focus({ preventScroll: true });
    flash = number;
    clearTimeout(flashing);
    flashing = setTimeout(() => { flash = undefined; }, 2000);
  }
  // A tall box must not leave Save behind the add box.
  const showEdit = (form: HTMLElement) => form.scrollIntoView({ block: "nearest" });
  function startEdit(rule: RoomRule) {
    menuFor = undefined;
    failure = "";
    editing = { number: rule.number, text: rule.text };
  }
  async function endEdit(number: number) {
    editing = undefined;
    failure = "";
    await tick();
    (list?.querySelector<HTMLElement>(`[data-edit="${number}"]`) ?? element)?.focus();
  }
  async function saveEdit(rule: RoomRule) {
    const text = editing?.text.trim();
    if (!text || busy) return;
    if (text === rule.text) { await endEdit(rule.number); return; }
    busy = true;
    failure = "";
    const done = await call("rule.edit", { room: store.room, number: rule.number, text }).then(() => true, () => false);
    busy = false;
    // The new text shows when its rule.edited event arrives.
    if (done) await endEdit(rule.number);
    else failure = `Rule r${rule.number} was not changed. Try again.`;
  }
  function copy(rule: RoomRule) {
    menuFor = undefined;
    navigator.clipboard.writeText(rule.text).then(() => "Copied", () => "Could not copy").then(word => {
      copied = { number: rule.number, word };
      setTimeout(() => { if (copied?.number === rule.number) copied = undefined; }, 2000);
    });
  }
  function slash(event: KeyboardEvent) {
    if (event.key !== "/" || event.ctrlKey || event.metaKey || event.altKey) return;
    if (event.target instanceof HTMLElement && event.target.closest("input, textarea, [contenteditable]")) return;
    event.preventDefault();
    find?.focus();
  }
</script>

<svelte:window onkeydown={slash} onpointerdown={event => { if (menuFor !== undefined && event.target instanceof Element && !event.target.closest(".more")) menuFor = undefined; }} />

{#snippet words(value: string)}
  {#each ruleParts(value, store.rules) as part, i (i)}
    {#if part.kind === "link"}<button type="button" class="ref" onclick={() => void jump(part.number)}>{part.text}</button>
    {:else if part.kind === "code"}<code>{@render marked(part.text)}</code>
    {:else}{@render marked(part.text)}{/if}
  {/each}
{/snippet}
{#snippet marked(value: string)}{#each marks(value, wanted) as cut, i (i)}{#if cut.hit}<mark>{cut.text}</mark>{:else}{cut.text}{/if}{/each}{/snippet}
{#snippet filters(faces: boolean)}
  {#each authors as { key, rule, total } (key)}
    <button type="button" class="author" aria-pressed={by === key} onclick={() => { by = by === key ? undefined : key; }}>
      {#if faces}<Avatar kind={rule.by.kind} name={name(rule)} root={!!rule.by.session && store.sessions.has(rule.by.session) && !store.sessions.get(rule.by.session)?.parent} size={18} />{/if}
      <span class="who" style:--hue={hue(name(rule))} class:you={key === "owner"}>{name(rule)}</span><span class="total">{total}</span></button>
  {/each}
{/snippet}

<section class="panel" aria-label="Rules" tabindex="-1" bind:this={element}>
  <!-- One way back: the room name in the crumb when wide, a 40 px arrow on a phone. -->
  <div class="top"><PageCrumb {room} page="Rules" {onback} />{#if rules.length}<span class="total">{rules.length}</span>
    <label class="find"><Icon name="search" size={14} /><input type="search" bind:this={find} bind:value={query} placeholder="Find a rule" aria-label="Find a rule"
      onkeydown={event => { if (event.key === "Escape" && query) { event.stopPropagation(); query = ""; } }} />
      {#if wanted}<span class="total" role="status">{matched.length} of {rules.length}</span>{:else}<kbd aria-hidden="true">/</kbd>{/if}</label>{/if}</div>
  <div class="list" bind:this={list} style:scroll-padding-bottom="{addHeight}px">
    <div class="page">
      <div class="doc">
        {#if rules.length}<p class="lead">Every session in {room} reads these on its next turn.</p>
          <!-- The side column folds to this line when the page is narrow, whatever makes it narrow. -->
          <p class="fold"><span>About {tokens} tokens, read on every turn</span>{#if long}{@render filters(false)}{/if}</p>
        {/if}
        {#if wanted && rules.length && !rows.length}<p class="none">No rule has these words.</p>{/if}
        <ol>
          {#each rows as rule (rule.number)}
            {#if removed.has(rule.number)}
              <li class="gone"><p>Rule r{rule.number} removed · <button type="button" class="link" data-undo={rule.number}
                aria-label={`Undo: add rule r${rule.number}'s text again`} onclick={() => void undo(rule)} disabled={busy}>Undo</button></p></li>
            {:else if editing?.number === rule.number}
              <li data-rule={rule.number} class="editing"><span class="number">r{rule.number}</span>
                <!-- svelte-ignore a11y_no_noninteractive_element_interactions (Esc cancels from either button) -->
                <form class="edit" {@attach showEdit} oninput={event => showEdit(event.currentTarget)} onkeydown={event => { if (event.key === "Escape") { event.stopPropagation(); void endEdit(rule.number); } }} onsubmit={event => { event.preventDefault(); void saveEdit(rule); }}>
                  <RuleBox bind:value={editing.text} label={`Edit rule r${rule.number}`} {busy} onsave={() => void saveEdit(rule)} oncancel={() => void endEdit(rule.number)} />
                  <p class="keys"><span>Enter saves · Shift+Enter adds a line · Esc cancels</span>
                    <button type="button" class="plain" onclick={() => void endEdit(rule.number)}>Cancel</button>
                    <button type="submit" class="plain save" disabled={busy || !editing.text.trim() || [...editing.text].length > ruleLimit}>Save</button></p>
                </form></li>
            {:else}
              {@const { lead, rest } = splitRule(rule.text)}
              <li data-rule={rule.number} tabindex="-1" class:flash={flash === rule.number}><span class="number">r{rule.number}</span>
                <p class="text"><span class="first">{@render words(lead)}</span>{@render words(rest)}<span class="cite"><span class="who" class:you={rule.by.kind === "owner"}
                  style:--hue={hue(name(rule))}>{name(rule)}</span>, <time datetime={rule.at} title={new Date(rule.at).toLocaleString()}>{ruleAge(rule.at, now)}</time></span></p>
                <!-- The bar sits on the row's top edge, in the padding and the gap above, so it never covers text. -->
                <span class="bar">
                  <button type="button" data-edit={rule.number} onclick={() => startEdit(rule)} aria-label={`Edit rule r${rule.number}`}>Edit</button>
                  <button type="button" onclick={() => copy(rule)} aria-label={`Copy rule r${rule.number}`}>{copied?.number === rule.number ? copied.word : "Copy"}</button>
                  <button type="button" class="remove" onclick={() => void remove(rule)} aria-label={`Remove rule r${rule.number}`}>Remove</button></span>
                <span class="more"><button type="button" aria-label={`Rule r${rule.number} actions`} aria-haspopup="menu" aria-expanded={menuFor === rule.number}
                  onclick={async () => { menuFor = menuFor === rule.number ? undefined : rule.number; await tick(); list?.querySelector<HTMLElement>(".menu [role=menuitem]")?.focus(); }}><Icon name="more" /></button>
                  {#if menuFor === rule.number}
                  <span class="menu" role="menu" tabindex="-1" onkeydown={event => { if (event.key === "Escape") { event.stopPropagation(); menuFor = undefined; } }}>
                    <button type="button" role="menuitem" onclick={() => startEdit(rule)}>Edit</button>
                    <button type="button" role="menuitem" onclick={() => copy(rule)}>Copy</button>
                    <button type="button" role="menuitem" onclick={() => void remove(rule)}>Remove</button></span>{/if}</span>
              </li>
            {/if}
          {/each}
        </ol>
        {#if store.ready && !rules.length && !rows.length}<p class="none empty">No rules yet. A rule you add here reaches every session in {room}.</p>{/if}
        <!-- Where the composer is, so adding a rule feels like posting one. -->
        <form class="add" bind:clientHeight={addHeight} onsubmit={event => { event.preventDefault(); void submit(); }}>
          {#if failure}<p class="failure" role="alert">{failure}</p>{/if}
          <RuleBox bind:value={draft} label="New rule" placeholder={`Add a rule for every session in ${room}`} {busy} onsave={() => void submit()} />
        </form>
      </div>
      {#if rules.length}<div class="side" role="complementary" aria-label="About these rules">
        <div class="tile" title="Estimated at 4 characters to a token"><p class="figure"><span>About</span> <strong>{tokens}</strong> <span>tokens</span></p><p>read on every turn</p></div>
        {#if long}
          <section aria-labelledby="rules-by"><h2 id="rules-by">Set by</h2>
            <button type="button" class="author" aria-pressed={by === undefined} onclick={() => { by = undefined; }}><Icon name="list" size={18} /><span>All</span><span class="total">{rules.length}</span></button>
            {@render filters(true)}</section>
          <section aria-labelledby="rules-latest"><h2 id="rules-latest">Latest</h2>
            {#each latest as rule (rule.number)}<button type="button" class="recent" onclick={() => void jump(rule.number)}><span class="number">r{rule.number}</span>
              <span class="clip">{rule.text.replaceAll("`", "")}</span><span class="total">{ruleAge(rule.at, now)}</span></button>{/each}</section>
        {/if}
      </div>{/if}
    </div>
  </div>
</section>

<style>
  .panel { flex: 1; min-height: 0; display: flex; flex-direction: column; container-type: inline-size; }
  .top { display: flex; align-items: center; gap: 8px; padding: 10px 16px; border-bottom: 1px solid var(--line); }
  .total { flex: none; color: var(--muted); font-size: 12.5px; font-variant-numeric: tabular-nums; }
  .find { display: flex; align-items: center; gap: 8px; margin-left: auto; width: 228px; min-width: 0; height: 30px; padding: 0 8px; border: 1px solid var(--line); border-radius: 6px; color: var(--muted); }
  .find:has(input:focus-visible) { border-color: var(--focus); box-shadow: 0 0 0 1px var(--focus); }
  .find input { flex: 1; min-width: 0; padding: 0; border: 0; background: none; color: var(--text); font: inherit; font-size: 13px; }
  .find input:focus-visible { outline: none; }
  .find input::-webkit-search-cancel-button { display: none; }
  .find input::placeholder { color: var(--muted); }
  kbd { padding: 0 5px; border: 1px solid var(--line); border-radius: 4px; font: 11px var(--mono); }
  /* The scroller holds both columns, so the side column sticks and the add box stays at the foot. */
  .list { flex: 1; min-height: 0; overflow-y: auto; display: flex; flex-direction: column; }
  .page { flex: 1 0 auto; display: grid; grid-template-columns: minmax(0, 880px) 264px; justify-content: center; gap: 16px; padding: 0 16px; }
  .page:not(:has(.side)) { grid-template-columns: minmax(0, 880px); }
  .doc { min-width: 0; display: flex; flex-direction: column; padding-top: 16px; }
  .lead { margin: 0 0 12px 12px; color: var(--muted); font-size: 14px; }
  .fold { display: none; flex-wrap: wrap; align-items: center; gap: 0 12px; margin: 0 0 12px 12px; color: var(--muted); font-size: 12.5px; font-variant-numeric: tabular-nums; }
  .none { margin: 0 0 12px 12px; color: var(--muted); font-size: 14px; }
  .empty { margin-top: auto; }
  .empty + .add { margin-top: 0; }
  ol { margin: 0 0 12px; padding: 0; list-style: none; display: grid; gap: 3px; }
  li { position: relative; display: grid; grid-template-columns: 44px 1fr auto; align-items: start; padding: 13px 12px; border-radius: 6px; transition: background 200ms ease-out; }
  li:hover, li:focus-within { background: var(--hover); }
  li.flash { background: var(--selected); }
  .number { color: var(--muted); font-size: 13px; line-height: 22.5px; font-variant-numeric: tabular-nums; }
  .text { margin: 0; min-width: 0; color: var(--muted); font-size: 15px; line-height: 1.5; white-space: pre-wrap; overflow-wrap: anywhere; }
  .first { color: var(--text); }
  code { font: 13px var(--mono); }
  mark { padding: 0 1px; border-radius: 2px; background: color-mix(in srgb, var(--attention) 22%, transparent); color: var(--text); }
  .ref { all: unset; color: var(--focus); cursor: pointer; border-radius: 2px; }
  .ref:hover { text-decoration: underline; }
  /* No space comes before it, so the citation stays on a line with the rule's last word. */
  .cite { margin-left: 8px; font-size: 12.5px; white-space: nowrap; font-variant-numeric: tabular-nums; }
  .who { color: oklch(var(--id-name) var(--hue)); font-weight: 500; }
  .who.you { color: var(--focus); }
  .gone { align-items: center; color: var(--muted); font-size: 14px; }
  .gone p { margin: 0; grid-column: 2 / -1; }
  .link { padding: 2px 4px; border: 0; border-radius: 6px; background: none; color: var(--focus); font: inherit; font-weight: 500; cursor: pointer; }
  .link:hover { background: var(--hover); }
  /* 26 px high and 13 px above the row: the row's padding and the gap take all of it, so showing it hides no text. */
  .bar { position: absolute; top: -13px; right: 8px; z-index: 1; display: flex; gap: 2px; height: 26px; padding: 1px; box-sizing: border-box; border: 1px solid var(--line); border-radius: 6px; background: var(--raised); box-shadow: 0 2px 8px rgb(0 0 0 / 12%); }
  li:not(:hover, :focus-within) .bar { opacity: 0; pointer-events: none; }
  .bar button { padding: 0 8px; border: 0; border-radius: 4px; background: none; color: var(--muted); font: inherit; font-size: 12.5px; font-weight: 500; cursor: pointer; }
  .bar button:hover { background: var(--hover); color: var(--text); }
  /* Neutral at rest: danger shows only when the owner is about to act (r16). */
  .bar .remove:hover, .bar .remove:focus-visible { color: var(--danger); }
  .editing:hover, .editing:focus-within { background: none; }
  .editing { padding-block: 8px; }
  .editing .number { padding-top: 10px; }
  .edit { grid-column: 2 / -1; min-width: 0; }
  .keys { display: flex; align-items: center; gap: 4px; margin: 6px 0 0; color: var(--muted); font-size: 12.5px; }
  .keys span { margin-right: auto; }
  .plain { min-height: 28px; padding: 0 10px; border: 0; border-radius: 6px; background: none; color: var(--muted); font: inherit; font-size: 13px; font-weight: 500; cursor: pointer; }
  .plain:hover { background: var(--hover); color: var(--text); }
  .save { background: var(--selected); color: var(--text); }
  .save:disabled { opacity: 0.5; cursor: default; }
  .more { display: none; position: relative; }
  @media (hover: none) {
    .bar { display: none; }
    .more { display: block; }
    .keys span { display: none; }
    .keys { justify-content: flex-end; }
    .plain { min-height: 40px; }
    li:hover, li:focus-within { background: none; }
    li.flash { background: var(--selected); }
  }
  .more > button { display: grid; place-items: center; width: 40px; height: 40px; min-width: 40px; min-height: 40px; padding: 0; margin: -8px -8px -8px 0; border: 0; border-radius: 6px; background: none; color: var(--muted); }
  .menu { position: absolute; right: 0; top: calc(100% + 4px); z-index: 3; min-width: 180px; padding: 4px; display: grid;
    background: var(--raised); border: 1px solid var(--line); border-radius: 8px; box-shadow: 0 6px 20px rgb(0 0 0 / 18%); }
  .menu button { min-height: 44px; padding: 0 10px; border: 0; border-radius: 6px; background: none; color: var(--text); font: inherit; font-size: 14px; text-align: left; }
  .add { position: sticky; bottom: 0; z-index: 2; margin-top: auto; padding: 0 0 16px; background: var(--bg); }
  .failure { margin: 0 0 8px; color: var(--danger); font-size: 13px; }
  .side { position: sticky; top: 16px; align-self: start; display: grid; gap: 20px; margin-top: 16px; padding-bottom: 16px; min-width: 0; }
  .side section { display: grid; }
  h2 { margin: 0 0 6px 8px; font-size: 16px; font-weight: 600; line-height: 1.25; }
  .tile { padding: 12px 14px; border: 1px solid var(--line); border-radius: 12px; background: var(--raised); color: var(--muted); font-size: 12.5px; }
  .tile p { margin: 0; }
  .figure { font-size: 14px; }
  .figure strong { color: var(--text); font-size: 24px; font-weight: 600; line-height: 1.25; font-variant-numeric: tabular-nums; }
  .author { all: unset; box-sizing: border-box; display: flex; align-items: center; gap: 8px; min-height: 32px; padding: 0 8px; border-radius: 6px; font-size: 13px; color: var(--text); cursor: pointer; }
  .author > :global(svg) { color: var(--muted); }
  .author:hover, .recent:hover { background: var(--hover); }
  .author[aria-pressed="true"] { background: var(--selected); }
  .fold .author { display: inline-flex; font-size: 12.5px; min-height: 24px; padding: 0 4px; }
  .recent { all: unset; box-sizing: border-box; display: grid; grid-template-columns: 32px 1fr auto; gap: 4px; padding: 6px 8px; border-radius: 6px; font-size: 12.5px; line-height: 1.4; cursor: pointer; }
  .recent .number { font-size: 12.5px; line-height: 1.4; }
  .clip { display: -webkit-box; -webkit-box-orient: vertical; -webkit-line-clamp: 2; line-clamp: 2; overflow: hidden; overflow-wrap: anywhere; }
  .ref:focus-visible, .author:focus-visible, .recent:focus-visible { outline: 2px solid var(--focus); outline-offset: 0; }
  /* The side column folds by the page's own width, so the open Sessions panel folds it too. */
  @container (max-width: 899px) {
    .page { grid-template-columns: minmax(0, 880px); }
    .side { display: none; }
    .fold { display: flex; }
  }
  @container (max-width: 480px) {
    .find { flex: 1; width: auto; }
    .find kbd { display: none; }
    li { grid-template-columns: 36px 1fr auto; padding-inline: 8px; }
  }
  @media (hover: none) { .find kbd { display: none; } .fold .author { min-height: 40px; } }
  @media (prefers-reduced-motion: reduce) { li { transition: none; } }
  :focus-visible { outline: 2px solid var(--focus); outline-offset: 2px; }
</style>
