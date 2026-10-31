<script lang="ts">
  import { onMount, tick } from "svelte";
  import { SvelteMap } from "svelte/reactivity";
  import { call } from "../client.ts";
  import { fromNow } from "../feed.ts";
  import { hue } from "../identity.ts";
  import Avatar from "./Avatar.svelte";
  import Icon from "./Icon.svelte";
  import type { RoomStore } from "../room.svelte.ts";
  import type { RoomRule } from "../contract.gen.ts";
  // The room's rules as a short numbered document, with the add box where the composer sits. Numbers are stable:
  // removing a rule never renumbers the others, and Undo adds the same text again under the next number.
  let { store, room, onback, element = $bindable() }: { store: RoomStore; room: string; onback: () => void; element?: HTMLElement } = $props();
  type Filter = "all" | "owner" | "agents";
  let filter: Filter = $state("all");
  // A removed rule keeps its place for 10 seconds as "Rule r4 removed · Undo".
  const removed = new SvelteMap<number, RoomRule>();
  const timers = new Map<number, ReturnType<typeof setTimeout>>();
  let rules = $derived([...store.rules.values()]);
  let owner = $derived(rules.filter(rule => rule.by.kind === "owner").length);
  let rows = $derived([...rules.filter(rule => filter === "all" || (rule.by.kind === "owner") === (filter === "owner")), ...removed.values()]
    .sort((a, b) => a.number - b.number));
  let draft = $state("");
  let busy = $state(false);
  let failure = $state("");
  // The rule whose "…" menu is open, on screens without hover.
  let menuFor: number | undefined = $state();
  let list: HTMLElement | undefined = $state();
  let now = $state(Date.now());
  onMount(() => {
    element?.focus();
    const timer = setInterval(() => { now = Date.now(); }, 30_000);
    return () => { clearInterval(timer); for (const pending of timers.values()) clearTimeout(pending); };
  });
  const name = (rule: RoomRule) => rule.by.kind === "owner" ? "You" : rule.by.session ? store.names.get(rule.by.session) ?? "A closed session" : "Dyna";
  // Within a day the time is relative, as in the feed; older rules show their date. A rule newer than the
  // half-minute clock reads "just now", never as in the future.
  const when = (at: string) => now - Date.parse(at) < 86_400_000 ? fromNow(at, Math.max(now, Date.parse(at)))
    : new Date(at).toLocaleDateString([], { month: "short", day: "numeric", year: new Date(at).getFullYear() === new Date(now).getFullYear() ? undefined : "numeric" });

  async function add(text: string): Promise<boolean> {
    busy = true;
    failure = "";
    const added = await call("rule.add", { room: store.room, text }).catch(() => undefined);
    busy = false;
    if (!added) { failure = "The rule was not added. Try again."; return false; }
    // The rule.added event brings the same rule; setting it now shows it at once.
    store.rules.set(added.number, { number: added.number, text, by: { kind: "owner" }, at: new Date().toISOString() });
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
</script>

<svelte:window onpointerdown={event => { if (menuFor !== undefined && event.target instanceof Element && !event.target.closest(".more")) menuFor = undefined; }} />
<section class="panel" aria-label="Rules" tabindex="-1" bind:this={element}>
  <!-- One way back: the room name in the crumb when wide, a 40 px arrow on a phone. -->
  <div class="top"><button type="button" class="back" onclick={onback} aria-label={`Back to ${room}`} title={`Back to ${room}`}><Icon name="back" /></button>
    <button type="button" class="room" onclick={onback}>{room}</button><span class="slash" aria-hidden="true">/</span><strong>Rules</strong>{#if rules.length}<span class="count">{rules.length}</span>{/if}
    <!-- Filters help only in a long list. -->
    {#if rules.length > 10}<div class="filters" role="group" aria-label="Show rules by">
      {#each [["all", "All", undefined], ["owner", "Yours", owner], ["agents", "Agents", rules.length - owner]] as const as [value, label, total] (value)}
        <button type="button" aria-pressed={filter === value} onclick={() => { filter = value; }}>{label}{#if total !== undefined}<span class="count">{total}</span>{/if}</button>{/each}
    </div>{/if}</div>
  <div class="list" bind:this={list}>
    <div class="doc">
      <p class="lead">Every session in {room} gets these rules on its next turn.</p>
      {#if store.ready && !rows.length}<p class="none">No rules yet. A rule you add here reaches every session in {room}.</p>{/if}
      <ol>
        {#each rows as rule (rule.number)}
          {#if removed.has(rule.number)}
            <li class="gone"><p>Rule r{rule.number} removed · <button type="button" class="link" data-undo={rule.number}
              aria-label={`Undo: add rule r${rule.number}'s text again`} onclick={() => void undo(rule)} disabled={busy}>Undo</button></p></li>
          {:else}
            <li><span class="number">r{rule.number}</span>
              <div class="body"><p class="text">{rule.text}</p>
                <p class="meta"><span class="by">{#if rule.by.kind === "owner"}You{:else}<Avatar kind={rule.by.kind} name={name(rule)} size={16} />
                  <span class="who" style:--hue={hue(name(rule))}>{name(rule)}</span>{/if}</span><span aria-hidden="true">·</span><time datetime={rule.at} title={new Date(rule.at).toLocaleString()}>{when(rule.at)}</time></p></div>
              <button type="button" class="remove" onclick={() => void remove(rule)} aria-label={`Remove rule r${rule.number}`}>Remove</button>
              <span class="more"><button type="button" aria-label={`Rule r${rule.number} actions`} aria-haspopup="menu" aria-expanded={menuFor === rule.number}
                onclick={async () => { menuFor = menuFor === rule.number ? undefined : rule.number; await tick(); list?.querySelector<HTMLElement>(".menu [role=menuitem]")?.focus(); }}><Icon name="more" /></button>
                {#if menuFor === rule.number}
                <span class="menu" role="menu" tabindex="-1" onkeydown={event => { if (event.key === "Escape") { event.stopPropagation(); menuFor = undefined; } }}><button type="button" role="menuitem" onclick={() => void remove(rule)}>Remove rule r{rule.number}</button></span>{/if}</span>
            </li>
          {/if}
        {/each}
      </ol>
    </div>
  </div>
  <!-- Where the composer is, so adding a rule feels like posting one. Shift+Enter starts a new line. -->
  <form class="add" onsubmit={event => { event.preventDefault(); void submit(); }}>
    {#if failure}<p class="failure" role="alert">{failure}</p>{/if}
    <div class="box"><textarea rows="1" maxlength="2000" bind:value={draft} placeholder={`Add a rule for every session in ${room}`} aria-label="New rule" readonly={busy}
      onkeydown={event => {
        if (event.key === "Enter" && !event.shiftKey && !event.isComposing) { event.preventDefault(); void submit(); }
        // Escape keeps a typed rule: the page closes only on Escape in an empty box.
        if (event.key === "Escape" && draft) event.stopPropagation();
      }}></textarea>
      <span class="hint" aria-hidden="true"><kbd>Enter</kbd> adds</span></div>
  </form>
</section>

<style>
  .panel { flex: 1; min-height: 0; display: flex; flex-direction: column; container-type: inline-size; }
  .top { display: flex; align-items: center; gap: 8px; padding: 10px 16px; border-bottom: 1px solid var(--line); }
  .back, .room { border: 0; border-radius: 6px; background: none; color: var(--muted); font: inherit; cursor: pointer; }
  .back:hover, .room:hover { background: var(--hover); color: var(--text); }
  .back { display: none; place-items: center; width: 40px; height: 40px; margin: -8px 0 -8px -12px; padding: 0; }
  .room { margin-left: -6px; padding: 2px 6px; font-size: 13px; }
  .slash { color: var(--muted); font-size: 13px; }
  @media (max-width: 640px) { .back { display: grid; } .room, .slash { display: none; } }
  .count { color: var(--muted); font-size: 12.5px; font-variant-numeric: tabular-nums; }
  .filters { margin-left: auto; display: flex; gap: 2px; }
  .filters button { display: flex; gap: 6px; padding: 4px 10px; border: 0; border-radius: 6px; background: none; color: var(--muted); font: inherit; font-size: 13px; font-weight: 500; cursor: pointer; }
  .filters button:hover { background: var(--hover); color: var(--text); }
  .filters button[aria-pressed="true"] { background: var(--selected); color: var(--text); }
  .list { flex: 1; min-height: 0; overflow-y: auto; }
  /* A reading column, as a short document: rules wrap at 920 px and are never cut. */
  .doc { max-width: 920px; margin: 0 auto; padding: 20px 16px 72px; }
  .lead { margin: 0 0 12px 12px; color: var(--muted); font-size: 14px; }
  .none { margin: 0 0 0 12px; color: var(--muted); font-size: 14px; }
  ol { margin: 0; padding: 0; list-style: none; display: grid; gap: 2px; }
  li { position: relative; display: grid; grid-template-columns: 40px 1fr auto; align-items: start; gap: 4px; padding: 10px 12px; border-radius: 6px; }
  li:hover, li:focus-within { background: var(--hover); }
  .number { padding-top: 1px; color: var(--muted); font-size: 14px; font-variant-numeric: tabular-nums; }
  .body { min-width: 0; }
  .text { margin: 0; font-size: 15px; line-height: 1.5; white-space: pre-wrap; overflow-wrap: anywhere; }
  .meta { margin: 4px 0 0; display: flex; flex-wrap: wrap; align-items: center; gap: 4px; color: var(--muted); font-size: 12.5px; }
  .by { display: inline-flex; align-items: center; gap: 6px; }
  .who { color: oklch(var(--id-name) var(--hue)); font-weight: 500; }
  .gone { align-items: center; color: var(--muted); font-size: 14px; }
  .gone p { margin: 0; grid-column: 2 / -1; }
  .link { padding: 2px 4px; border: 0; border-radius: 6px; background: none; color: var(--focus); font: inherit; font-weight: 500; cursor: pointer; }
  .link:hover { background: var(--hover); }
  /* Remove sits at the right end of the author line, so it takes no width from the rule's text. */
  .remove { position: absolute; right: 8px; bottom: 6px; padding: 4px 10px; border: 0; border-radius: 6px; background: none; color: var(--muted); font: inherit; font-size: 13px; font-weight: 500; cursor: pointer; }
  .remove:hover { background: var(--selected); color: var(--danger); }
  /* A pointer shows Remove on the hovered or focused rule; a touch screen has an always-visible "…" menu instead. */
  li:not(:hover, :focus-within) .remove { opacity: 0; }
  .more { display: none; position: relative; }
  @media (hover: none) {
    .remove { display: none; }
    .more { display: block; }
    li:hover, li:focus-within { background: none; }
  }
  .more > button { display: grid; place-items: center; width: 40px; height: 40px; min-width: 40px; min-height: 40px; padding: 0; margin: -8px -8px -8px 0; border: 0; border-radius: 6px; background: none; color: var(--muted); }
  .menu { position: absolute; right: 0; top: calc(100% + 4px); z-index: 2; min-width: 180px; padding: 4px; display: grid;
    background: var(--raised); border: 1px solid var(--line); border-radius: 8px; box-shadow: 0 6px 20px rgb(0 0 0 / 18%); }
  .menu button { min-height: 44px; padding: 0 10px; border: 0; border-radius: 6px; background: none; color: var(--danger); font: inherit; font-size: 14px; text-align: left; }
  .add { flex: none; width: 100%; max-width: 952px; margin: 0 auto; padding: 0 16px 16px; }
  .failure { margin: 0 0 8px; color: var(--danger); font-size: 13px; }
  .box { display: flex; align-items: center; gap: 8px; padding: 10px 14px; border: 1px solid var(--line); border-radius: 12px; background: var(--raised); }
  .box:has(textarea:focus-visible) { border-color: var(--focus); box-shadow: 0 0 0 1px var(--focus); }
  textarea { flex: 1; padding: 0; border: 0; background: none; color: inherit; font: inherit; font-size: 15px; resize: none; field-sizing: content; max-height: 30vh; }
  @media (pointer: coarse) { textarea { font-size: 16px; } }
  textarea:focus-visible { outline: none; }
  textarea::placeholder { color: var(--muted); }
  .hint { flex: none; color: var(--muted); font-size: 12.5px; }
  kbd { padding: 1px 5px; border: 1px solid var(--line); border-radius: 4px; font: 11px var(--mono); }
  @media (hover: none) { .hint { display: none; } }
  @container (max-width: 560px) { .hint { display: none; } }
  :focus-visible { outline: 2px solid var(--focus); outline-offset: 2px; }
</style>
