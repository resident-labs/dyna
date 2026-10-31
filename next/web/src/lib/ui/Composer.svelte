<script lang="ts" module>
  // A session the mention list offers, with its muted status ("working · read spec.md", "starting", or none).
  export type Mentionable = { name: string; root: boolean; status: string };
</script>

<script lang="ts">
  import { tick } from "svelte";
  import Icon from "./Icon.svelte";
  import Avatar from "./Avatar.svelte";
  import { render } from "./PostRow.svelte";
  import { count, fileSize, type Quote } from "../feed.ts";
  import { maxFiles, type FileDraft } from "../attach.svelte.ts";
  import { hue } from "../identity.ts";
  import { mentionToken } from "../mentions.ts";
  // Enter sends and Shift+Enter starts a new line. The action row keeps room for the attach and mention buttons.
  // A forwarded post waits above the text as in v1; the × or Escape removes it.
  // As in v1, an @word at the caret opens a list of sessions above the box: arrows move, Enter or Tab picks,
  // Escape closes. A DM passes no sessions, since the engine refuses mentions there.
  // Files wait above the text as chips, as in v1's tray; the paperclip, a paste or the picker adds them.
  let { value = $bindable(""), placeholder, onsend, quote, onunquote, sessions = [], files, element = $bindable() }: {
    value?: string; placeholder: string; onsend: () => void; quote?: Quote; onunquote: () => void; sessions?: Mentionable[]; files: FileDraft;
    element?: HTMLTextAreaElement;
  } = $props();
  let picker: HTMLInputElement | undefined = $state();
  // The line that names a refused file and why shows until the next add or send.
  let note = $state("");
  // A forwarded post's files count toward the engine's limit, so a post never carries more than it accepts.
  let forwarded = $derived(quote?.attachments.length ?? 0);
  let over = $derived(files.items.length + forwarded - maxFiles);
  let canSend = $derived(!files.busy && over <= 0 && (!!value.trim() || !!quote || files.items.length > 0));
  const extension = (name: string) => /\.([A-Za-z0-9]{1,6})$/.exec(name)?.[1].toUpperCase() ?? "FILE";
  // v1's order (attach.ts pastedFiles): the files, else file items, else an image from the async clipboard. Files win over
  // text, since Finder also puts the file name in text/plain; WebKitGTK may leave both empty for a screenshot.
  // The draft and its forwarded files are read before the clipboard wait, so a paste lands in the conversation it began in.
  async function paste(event: ClipboardEvent) {
    const data = event.clipboardData, pasted = [...data?.files ?? []], draft = files, reserved = forwarded;
    if (!pasted.length) for (const item of data?.items ?? []) { const file = item.kind === "file" ? item.getAsFile() : null; if (file) pasted.push(file); }
    if (!pasted.length && data?.getData("text/plain")) return;
    event.preventDefault();
    if (!pasted.length) for (const item of await navigator.clipboard.read().catch(() => [])) {
      const type = item.types.find(kind => /^image\/(png|jpeg|gif|webp)$/.test(kind));
      if (type) pasted.push(new File([await item.getType(type)], `image.${type.split("/")[1]}`, { type }));
    }
    if (!pasted.length) return;
    const message = draft.add(pasted, reserved);
    if (draft === files) note = message;
  }
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
      if (canSend) { note = ""; onsend(); }
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
  {#if files.items.length}<ul class="tray" aria-label="Files">{#each files.items as item (item.id)}
    <li class="chip" class:failed={item.state === "failed"}>
      {#if item.preview}<img src={item.preview} alt="" />
      {:else}<span class="ext" aria-hidden="true">{extension(item.file.name)}</span>{/if}
      <span class="meta"><span class="file-name" title={item.file.name}>{item.file.name}</span>
        <span class="sub">{item.state === "saving" ? "Saving…" : item.state === "failed" ? "Not saved" : fileSize(item.file.size)}</span></span>
      {#if item.state === "failed"}<button type="button" class="retry" onclick={() => files.save(item.id)}>Retry</button>{/if}
      <button type="button" class="remove" aria-label="Remove {item.file.name}" title="Remove {item.file.name}" onclick={() => files.remove(item.id)}><Icon name="close" size={14} /></button>
    </li>{/each}</ul>{/if}
  {#if note || over > 0}<p class="note" role="status">{over > 0 ? `A post can carry ${maxFiles} files; remove ${over} to send.` : note}</p>{/if}
  <textarea rows="2" aria-label="Message" bind:this={element} bind:value onkeydown={keydown} onpaste={paste} {placeholder}
    oninput={() => { dismissed = -1; track(); }} onclick={track} onkeyup={track} onblur={() => { dismissed = token?.start ?? -1; }} onfocus={() => { dismissed = -1; }}
    role="combobox" aria-autocomplete="list" aria-expanded={matches.length > 0} aria-controls={matches.length ? `${id}-list` : undefined}
    aria-activedescendant={matches.length ? `${id}-${pick}` : undefined}></textarea>
  <div class="actions">
    <button type="button" class="attach" aria-label="Attach files" title="Attach files" onclick={() => picker?.click()}><Icon name="clip" /></button>
    <input bind:this={picker} type="file" multiple hidden onchange={event => { note = files.add(event.currentTarget.files ?? [], forwarded); event.currentTarget.value = ""; element?.focus(); }} />
    <span class="keys">Enter to send · Shift+Enter for a new line</span>
    <button type="button" class="send" aria-label="Send" title="Send" disabled={!canSend} onclick={() => { note = ""; onsend(); element?.focus(); }}><Icon name="send" /></button>
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
  .actions { display: flex; align-items: center; gap: 8px; margin-top: 4px; }
  .keys { flex: 1; }
  /* v1's tray chip: a 28 px thumbnail or type badge, the name over a muted line, and a remove button. */
  .tray { display: flex; flex-wrap: wrap; gap: 8px; margin: 0 0 8px; padding: 0; list-style: none; }
  .chip { display: flex; align-items: center; gap: 8px; max-width: min(260px, 100%); padding: 4px 4px 4px 6px; border: 1px solid var(--line); border-radius: 8px; background: var(--sidebar); }
  .chip img, .ext { flex: none; width: 28px; height: 28px; border-radius: 6px; object-fit: cover; }
  .ext { display: grid; place-items: center; background: var(--raised); color: var(--muted); font: 600 10.5px var(--mono); }
  .meta { display: flex; flex-direction: column; min-width: 0; line-height: 1.3; }
  .file-name { font-size: 13px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .sub { color: var(--muted); font-size: 12px; }
  .failed { border-color: var(--danger); }
  .failed .sub { color: var(--danger); }
  .retry { all: unset; cursor: pointer; color: var(--focus); font-size: 12px; border-radius: 3px; }
  .retry:focus-visible { outline: 2px solid var(--focus); outline-offset: 2px; }
  .note { margin: 0 0 8px; color: var(--muted); font-size: 12px; }
  /* The key hint shows only while the empty message box has focus. */
  .keys { font-size: 12px; color: var(--muted); visibility: hidden; }
  .composer:has(textarea:focus:placeholder-shown) .keys { visibility: visible; }
  .actions button, .remove { display: grid; place-items: center; flex: none; width: 32px; height: 32px; border: 0; border-radius: 8px; cursor: pointer; }
  .attach, .remove { background: none; color: var(--muted); }
  .remove { width: 24px; height: 24px; border-radius: 6px; }
  .attach:hover, .remove:hover { background: var(--hover); color: var(--text); }
  @media (hover: none) { .attach, .remove { width: 40px; height: 40px; } }
  .send { background: var(--focus); color: var(--bg); }
  .send:disabled { background: var(--selected); color: var(--muted); cursor: default; }
  .actions button:focus-visible, .remove:focus-visible { outline: 2px solid var(--focus); outline-offset: 2px; }
</style>
