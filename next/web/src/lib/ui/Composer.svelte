<script lang="ts" module>
  // A session the mention list offers, with its muted status ("working · read spec.md", "starting", or none).
  export type Mentionable = { name: string; root: boolean; status: string };
</script>

<script lang="ts">
  import { tick } from "svelte";
  import Icon from "./Icon.svelte";
  import Avatar from "./Avatar.svelte";
  import { render } from "./PostRow.svelte";
  import { count, type Quote } from "../feed.ts";
  import { hue } from "../identity.ts";
  import { mentionToken } from "../mentions.ts";
  // Enter sends and Shift+Enter starts a new line. The action row keeps room for the attach and mention buttons.
  // A forwarded post waits above the text as in v1; the × or Escape removes it.
  // As in v1, an @word at the caret opens a list of sessions above the box: arrows move, Enter or Tab picks,
  // Escape closes. A DM passes no sessions, since the engine refuses mentions there.
  let { value = $bindable(""), placeholder, onsend, quote, onunquote, sessions = [], element = $bindable() }: {
    value?: string; placeholder: string; onsend: () => void; quote?: Quote; onunquote: () => void; sessions?: Mentionable[]; element?: HTMLTextAreaElement;
  } = $props();
  const id = $props.id();
  let caret = $state(0);
  // Escape closes the list until the next typed character, as in v1.
  let dismissed = $state(-1);
  let pick = $state(0);
  let token = $derived(sessions.length ? mentionToken(value, caret) : undefined);
  let matches = $derived(token && token.start !== dismissed
    ? sessions.filter(session => session.name.toLowerCase().startsWith(token.query.toLowerCase())).slice(0, 8) : []);
  // A new query or a shorter list starts at the first row, so pick never points past the end.
  $effect(() => { void token?.query; void matches.length; pick = 0; });
  const track = () => { caret = element?.selectionStart ?? 0; };
  async function complete(name: string) {
    if (!token) return;
    // The pick replaces the whole word, also the part after the caret, and adds a space only when none follows.
    const rest = value.slice(caret).replace(/^[A-Za-z0-9._:-]*/, "");
    const text = /^\s/.test(rest) ? `@${name}` : `@${name} `, at = token.start + text.length + (/^\s/.test(rest) ? 1 : 0);
    value = value.slice(0, token.start) + text + rest;
    caret = at;
    await tick();
    element?.setSelectionRange(at, at);
  }
  function keydown(event: KeyboardEvent) {
    // keyCode 229 is a key that Safari sends while an input method composes.
    if (event.isComposing || event.keyCode === 229) return;
    if (matches.length && (event.key === "ArrowDown" || event.key === "ArrowUp")) {
      event.preventDefault();
      pick = (pick + (event.key === "ArrowDown" ? 1 : matches.length - 1)) % matches.length;
    } else if (matches.length && (event.key === "Tab" || (event.key === "Enter" && !event.shiftKey))) {
      event.preventDefault();
      void complete(matches[pick].name);
    } else if (event.key === "Escape" && (matches.length || quote)) {
      // The open list closes first, then the forwarded post goes.
      event.stopPropagation();
      if (matches.length) dismissed = token?.start ?? -1; else onunquote();
    } else if (event.key === "Enter" && !event.shiftKey) {
      event.preventDefault();
      onsend();
    }
  }
</script>

<div class="composer">
  {#if matches.length}
    <div class="mentions" id="{id}-list" role="listbox" aria-label="Sessions">
      <p class="head" aria-hidden="true">Sessions</p>
      {#each matches as session, i (session.name)}
        <button type="button" id="{id}-{i}" class="option" class:active={i === pick} role="option" aria-selected={i === pick} tabindex="-1"
          onmousedown={event => event.preventDefault()} onmousemove={() => { pick = i; }} onclick={() => void complete(session.name)}>
          <Avatar kind="session" name={session.name} root={session.root} size={24} />
          <span class="name" style:--hue={hue(session.name)}>{session.name}</span>
          {#if session.status}<span class="status">{session.status}</span>{/if}
        </button>
      {/each}
    </div>
  {/if}
  {#if quote}<div class="quote"><p class="label"><Icon name="forward" size={14} /><span>Forwarding a post from <strong>{quote.author}</strong></span>
    <button type="button" aria-label="Remove forwarded post" title="Remove forwarded post" onclick={() => { onunquote(); element?.focus(); }}><Icon name="close" size={14} /></button></p>
    <div class="quoted">{@html render(quote.text)}</div>
    {#if quote.attachments.length}<p class="files">{count(quote.attachments.length, "file")}: {quote.attachments.map(file => file.name).join(", ")}</p>{/if}</div>{/if}
  <textarea rows="2" aria-label="Message" bind:this={element} bind:value onkeydown={keydown} {placeholder}
    oninput={() => { dismissed = -1; track(); }} onclick={track} onkeyup={track} onblur={() => { dismissed = token?.start ?? -1; }} onfocus={() => { dismissed = -1; }}
    role="combobox" aria-autocomplete="list" aria-expanded={matches.length > 0} aria-controls={matches.length ? `${id}-list` : undefined}
    aria-activedescendant={matches.length ? `${id}-${pick}` : undefined}></textarea>
  <div class="actions">
    <span class="keys">Enter to send · Shift+Enter for a new line</span>
    <button type="button" aria-label="Send" title="Send" disabled={!value.trim() && !quote} onclick={() => { onsend(); element?.focus(); }}><Icon name="send" /></button>
  </div>
</div>

<style>
  .composer { position: relative; flex: none; margin: 0 16px 16px; padding: 12px 14px 8px; border: 1px solid var(--line); border-radius: 12px; background: var(--raised); }
  /* The box border is the textarea's focus ring. */
  .composer:has(textarea:focus-visible) { border-color: var(--focus); box-shadow: 0 0 0 1px var(--focus); }
  /* A raised card over the feed at the box's top left, as Discord's mention list; v1's list spans the whole box. */
  .mentions { position: absolute; left: 0; bottom: calc(100% + 8px); z-index: 2; width: min(320px, 100%); padding: 4px;
    background: var(--raised); border: 1px solid var(--line); border-radius: 12px; box-shadow: 0 8px 24px rgb(0 0 0 / 22%); }
  .head { margin: 0; padding: 4px 8px 6px; color: var(--muted); font-size: 12px; font-weight: 600; }
  .option { all: unset; box-sizing: border-box; display: flex; align-items: center; gap: 8px; width: 100%; min-height: 36px; padding: 0 8px; border-radius: 6px; cursor: pointer; }
  .option.active { background: var(--selected); }
  @media (hover: none) { .option { min-height: 44px; } }
  .name { flex: none; font-weight: 600; font-size: 14px; color: oklch(var(--id-name) var(--hue)); }
  .status { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--muted); font-size: 12px; }
  /* v1's draft chip: a left rule in the focus color, the label, and the post folded to three lines. */
  .quote { margin-bottom: 8px; padding: 6px 10px; border: 1px solid var(--line); border-left: 3px solid var(--focus); border-radius: 6px; background: var(--sidebar); }
  .label { margin: 0; display: flex; align-items: center; gap: 6px; color: var(--muted); font-size: 12px; }
  .label span { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .label strong { color: var(--text); font-weight: 500; }
  .quote button { all: unset; display: grid; place-items: center; width: 24px; height: 24px; border-radius: 4px; color: var(--muted); cursor: pointer; }
  .quote button:hover { background: var(--hover); color: var(--text); }
  .quote button:focus-visible { outline: 2px solid var(--focus); outline-offset: 0; }
  @media (hover: none) { .quote button { width: 40px; height: 40px; } }
  .quoted { max-height: 4.5em; overflow: hidden; color: var(--muted); font-size: 13px; overflow-wrap: anywhere; }
  .quoted :global(:is(p, ul, ol, pre, blockquote)) { margin: 0; }
  .files { margin: 4px 0 0; color: var(--muted); font-size: 12px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  textarea { display: block; width: 100%; padding: 0; border: 0; background: none; color: inherit; font: inherit; resize: none; field-sizing: content; min-height: 3em; max-height: 40vh; }
  textarea:focus-visible { outline: none; }
  textarea::placeholder { color: var(--muted); }
  .actions { display: flex; justify-content: space-between; align-items: center; gap: 8px; margin-top: 4px; }
  /* The key hint shows only while the empty message box has focus. */
  .keys { font-size: 12px; color: var(--muted); visibility: hidden; }
  .composer:has(textarea:focus:placeholder-shown) .keys { visibility: visible; }
  .actions button { display: grid; place-items: center; width: 32px; height: 32px; border: 0; border-radius: 8px; background: var(--focus); color: var(--bg); cursor: pointer; }
  .actions button:disabled { background: var(--selected); color: var(--muted); cursor: default; }
  .actions button:focus-visible { outline-offset: 2px; }
</style>
