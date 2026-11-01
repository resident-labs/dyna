<script lang="ts">
  import { onMount, tick } from "svelte";
  import { APIError, call } from "../client.ts";
  import { instructionDiff, instructionHunks, type DiffLine } from "../instruction-diff.ts";
  import { recoverInstructionDraft } from "../instruction-draft.ts";
  import Icon from "./Icon.svelte";
  import StatusPill from "./StatusPill.svelte";
  import { count } from "../feed.ts";

  type Scope = "room" | "global";
  type File = { text: string; hash: string; savedAt?: string | null };
  // The page opens on its two choices, the file for all rooms and this room's own. Choosing one opens it in a plain
  // editor, with a review beside it only once there are changes to review.
  let { room, roomName, rooms, sessions, onclose }: { room: string; roomName: string; rooms: number; sessions: number; onclose: () => void } = $props();
  let selected = $state<Scope>();
  let files = $state<Record<Scope, File>>();
  let unsaved = $state<Record<Scope, boolean>>({ room: false, global: false });
  let base = $state(""), hash = $state(""), savedAt = $state<string>(), draft = $state("");
  let other = $state<File>();
  let loading = $state(true), failed = $state(false), saveFailed = $state(false), saving = $state(false);
  let mobile = $state<"edit" | "changes">("edit");
  let search = $state("");
  let editor: HTMLTextAreaElement, finder: HTMLInputElement;
  let generation = 0;
  const prefixOf = (which: Scope) => `dyna.instructions.${which === "room" ? room : "global"}.`;
  const prefix = () => prefixOf(selected ?? "global");
  const request = () => selected === "room" ? { room } : {};
  const title = (which: Scope) => which === "room" ? `Only ${roomName}` : "All rooms";
  const reach = (which: Scope) => which === "room" ? `${count(sessions, "open session")} in this room` : `Every session in ${count(rooms, "room")}`;
  const tokensOf = (text: string) => Math.ceil(text.length / 4);
  let changes = $derived(instructionDiff(base, draft));
  let changed = $derived(changes.filter(line => line.kind !== "same"));
  let tokens = $derived(tokensOf(draft));
  let delta = $derived(tokens - tokensOf(base));
  let savedLabel = $derived(savedAt ? `Saved ${relative(savedAt)}` : "Not saved yet");
  let reviewable = $derived(changed.length > 0 || !!other);
  let reviewing = $derived(mobile === "changes" && reviewable);
  function relative(value: string) {
    const minutes = Math.floor((Date.now() - new Date(value).getTime()) / 60000);
    if (minutes < 1) return "just now";
    if (minutes < 60) return `${minutes} minutes ago`;
    const hours = Math.floor(minutes / 60);
    if (hours < 24) return `${hours} ${hours === 1 ? "hour" : "hours"} ago`;
    const days = Math.floor(hours / 24);
    return `${days} ${days === 1 ? "day" : "days"} ago`;
  }
  function clearDrafts() {
    const keys = Object.keys(localStorage).filter(key => key.startsWith(prefix()));
    for (const key of keys) localStorage.removeItem(key);
  }
  function keepDraft() {
    clearDrafts();
    if (draft !== base) localStorage.setItem(prefix() + hash, JSON.stringify({ text: draft, base }));
  }
  // Both files for the choices. A quiet load keeps the cards in place while it runs.
  async function loadChoices(quiet = false) {
    const current = ++generation;
    if (!quiet) { loading = true; failed = false; }
    try {
      const [global, own] = await Promise.all([call("instructions.get", {}), call("instructions.get", { room })]);
      if (current !== generation) return;
      const stored = Object.keys(localStorage);
      files = { global, room: own };
      unsaved = { global: stored.some(key => key.startsWith(prefixOf("global"))), room: stored.some(key => key.startsWith(prefixOf("room"))) };
    } catch { if (current === generation && !quiet) failed = true; }
    finally { if (current === generation) loading = false; }
  }
  async function load() {
    const current = ++generation, key = prefix();
    loading = true; failed = false; saveFailed = false; other = undefined;
    try {
      const result = await call("instructions.get", request());
      if (current !== generation) return;
      base = result.text; hash = result.hash; savedAt = result.savedAt ?? undefined; draft = result.text;
      const storedKey = Object.keys(localStorage).find(item => item.startsWith(key));
      if (storedKey) {
        const recovered = recoverInstructionDraft(localStorage.getItem(storedKey) ?? "", storedKey.slice(key.length), result);
        if (recovered) {
          draft = recovered.draft; base = recovered.base; hash = recovered.hash;
          if (recovered.stale) other = result;
        }
      }
    } catch { if (current === generation) failed = true; }
    finally { if (current === generation) loading = false; }
  }
  async function refresh() {
    if (!selected) { if (!loading) void loadChoices(true); return; }
    if (loading || failed || saving) return;
    const current = generation;
    try {
      const result = await call("instructions.get", request());
      if (current !== generation || saving) return;
      if (result.hash !== hash) {
        if (draft !== base) other = result;
        else { base = draft = result.text; hash = result.hash; savedAt = result.savedAt ?? undefined; }
      }
    } catch { /* The open editor keeps its draft when the engine is offline. */ }
  }
  async function save(overwrite = false) {
    if (saving || loading || failed || (draft === base && !(overwrite && other))) return;
    saving = true; saveFailed = false;
    const text = draft;
    try {
      const result = await call("instructions.set", { ...request(), text, base: overwrite && other ? other.hash : hash });
      clearDrafts(); hash = result.hash; base = text; other = undefined;
      keepDraft();
      savedAt = new Date().toISOString();
      try {
        const current = await call("instructions.get", request());
        if (current.hash === hash) savedAt = current.savedAt ?? savedAt;
      } catch { /* The save succeeded even if its timestamp cannot be refreshed. */ }
    } catch (error: unknown) {
      if (error instanceof APIError && error.detail.code === "stale") {
        try { other = await call("instructions.get", request()); }
        catch { saveFailed = true; }
      } else saveFailed = true;
    } finally { saving = false; }
  }
  function useTheirs() {
    if (!other) return;
    clearDrafts(); base = draft = other.text; hash = other.hash; savedAt = other.savedAt ?? undefined; other = undefined;
  }
  function discard() {
    if (other) { useTheirs(); return; }
    clearDrafts(); draft = base;
  }
  async function find() {
    if (!search) return;
    mobile = "edit"; await tick();
    if (!editor) return;
    const at = draft.toLocaleLowerCase().indexOf(search.toLocaleLowerCase(), editor.selectionEnd);
    const found = at < 0 ? draft.toLocaleLowerCase().indexOf(search.toLocaleLowerCase()) : at;
    if (found >= 0) { editor.focus(); editor.setSelectionRange(found, found + search.length); }
  }
  async function jump(line: number) {
    mobile = "edit"; await tick();
    const offset = draft.split("\n").slice(0, line - 1).reduce((sum, part) => sum + part.length + 1, 0);
    editor.focus(); editor.setSelectionRange(offset, offset);
    editor.scrollTop = Math.max(0, (line - 3) * 20);
  }
  function keys(event: KeyboardEvent) {
    if (!selected) return;
    if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === "s") { event.preventDefault(); if (!other) void save(); }
    else if (event.key === "/" && !(event.target instanceof HTMLInputElement) && !(event.target instanceof HTMLTextAreaElement)) {
      event.preventDefault(); finder?.focus();
    }
  }
  function choose(which: Scope) { selected = which; mobile = "edit"; search = ""; void load(); }
  // Back leaves a draft in the browser, where the choice marks it and the editor restores it.
  function back() {
    if (saving) return;
    if (!selected) { onclose(); return; }
    selected = undefined;
    void loadChoices();
  }
  onMount(() => { void loadChoices(); window.addEventListener("focus", refresh); return () => { generation++; window.removeEventListener("focus", refresh); }; });
</script>

{#snippet choice(which: Scope, file: File)}
  <button type="button" class="choice" onclick={() => choose(which)}>
    <span class="choice-head"><strong>{title(which)}</strong><Icon name="chevron" size={14} /></span>
    <span class="reach">{reach(which)}</span>
    <span class="preview" class:empty={!file.text.trim()}>{file.text.trim() || "Nothing written yet"}</span>
    <span class="meta">{#if file.text.trim()}about {tokensOf(file.text).toLocaleString()} tokens{#if file.savedAt}{" · "}saved {relative(file.savedAt)}{/if}{/if}
      {#if unsaved[which]}<StatusPill tone="warning" label="Unsaved draft" />{/if}</span>
  </button>
{/snippet}
{#snippet diffView(lines: DiffLine[])}
  {#each instructionHunks(lines) as hunk}
    <button class="line-number" type="button" onclick={() => void jump(hunk.line)}>Line {hunk.line}</button>
    {#each hunk.lines as line}
      <div class:add={line.kind === "add"} class:remove={line.kind === "remove"} class:same={line.kind === "same"} class="diff"><span>{line.kind === "add" ? "+" : line.kind === "remove" ? "−" : ""}</span>{line.text}</div>
    {/each}
  {/each}
{/snippet}
<svelte:window onkeydown={keys} />
<div class="instructions">
  <header>
    <button class="back" type="button" aria-label={selected ? "Back to the instructions" : "Back to room"} title="Back" onclick={back}><Icon name="backChevron" /></button>
    {#if selected}<span class="crumb">Instructions</span><span class="slash">/</span><strong>{title(selected)}</strong>
    {:else}<span class="crumb">{roomName}</span><span class="slash">/</span><strong>Instructions</strong>{/if}
    {#if selected && !loading && !failed && !other}<span class="actions">
      <span class="state">{draft === base ? savedLabel : "Not saved"}</span>
      {#if draft !== base}<button type="button" onclick={discard}>Discard</button>{/if}
      <button class="primary" type="button" title="Ctrl S" disabled={saving || draft === base} onclick={() => void save()}>Save</button>
    </span>{/if}
  </header>
  {#if loading}
    <div class="loading" aria-label="Loading instructions"><span></span><span></span><span></span></div>
  {:else if failed}
    <p class="error">Instructions could not be loaded. <button type="button" onclick={() => void (selected ? load() : loadChoices())}>Retry</button></p>
  {:else if !selected && files}
    <div class="choices">
      <div class="column">
        <h2>Where should these instructions apply?</h2>
        <div class="grid">{@render choice("global", files.global)}{@render choice("room", files.room)}</div>
        <p class="order">Sessions read all rooms first, then this room, then each repository’s AGENTS.md. A change reaches running sessions at their next run.</p>
      </div>
    </div>
  {:else if selected}
    {#if saveFailed}<p class="error" role="status">Your changes were not saved. <button type="button" onclick={() => void save()}>Retry</button></p>{/if}
    {#if reviewable}<nav class="mobile-tabs" aria-label="Editor view">
      <button type="button" class:active={!reviewing} onclick={() => mobile = "edit"}>Edit</button>
      <button type="button" class:active={reviewing} onclick={() => mobile = "changes"}>Changes <span>{changed.length || ""}</span></button>
    </nav>{/if}
    <div class="body">
      <section class:mobile-hidden={reviewing} class="edit" aria-label="Instruction editor">
        <div class="tools">
          <p class="facts">{reach(selected)} <span>·</span> about <b>{tokens.toLocaleString()}</b> tokens</p>
          <label class="find"><svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true"><circle cx="11" cy="11" r="6"/><path d="m20 20-4.5-4.5"/></svg><input aria-label="Find in file" bind:this={finder} bind:value={search} onkeydown={event => { if (event.key === "Enter") { event.preventDefault(); void find(); } }} placeholder="Find" /><kbd>/</kbd></label>
        </div>
        <textarea bind:this={editor} value={draft} oninput={event => { draft = event.currentTarget.value; keepDraft(); }} spellcheck="false"
          placeholder={selected === "room" ? `Write instructions for every session in ${roomName}` : "Write instructions for every session"} aria-label="Markdown instructions"></textarea>
      </section>
      {#if reviewable}<aside class:mobile-hidden={!reviewing} aria-label="Instruction review">
        {#if other}
          <div class="side-head"><StatusPill tone="warning" label="Changed on another device" /><p>Nothing was saved. Your text is unchanged.</p></div>
          <div class="review"><strong>Their change</strong>
            {@render diffView(instructionDiff(base, other.text))}
            <strong>Yours</strong>
            {@render diffView(changes)}
          </div>
          <p class="overwrite-note">Save mine over it replaces their change with your file.</p>
          <div class="resolve"><button class="outlined" type="button" disabled={saving} onclick={useTheirs}>Use theirs</button><button class="primary" type="button" disabled={saving} onclick={() => void save(true)}>Save mine over it</button></div>
        {:else}
          <div class="side-head review-title"><strong>What sessions get</strong> <span>{count(changed.length, "line")} changed</span></div>
          <div class="review">
            <p>{delta >= 0 ? "Adds" : "Removes"} about {Math.abs(delta).toLocaleString()} tokens {delta >= 0 ? "to" : "from"} every prompt</p>
            {@render diffView(changes)}
          </div>
        {/if}
      </aside>{/if}
    </div>
    <div class="mobile-save">
      <span>{draft === base ? savedLabel : `${count(changed.length, "line")} changed`}</span>
      {#if other}<button class="outlined" type="button" disabled={saving} onclick={useTheirs}>Use theirs</button><button class="primary" type="button" disabled={saving} onclick={() => void save(true)}>Save mine</button>
      {:else}<button type="button" disabled={draft === base} onclick={discard}>Discard</button><button class="primary" type="button" disabled={saving || draft === base} onclick={() => void save()}>Save</button>{/if}
    </div>
  {/if}
</div>

<style>
  .instructions { position: absolute; inset: 0; z-index: 4; display: flex; flex-direction: column; background: var(--bg); color: var(--text); }
  header { height: 52px; flex: none; display: flex; align-items: center; gap: 8px; padding: 0 16px; border-bottom: 1px solid var(--line); }
  .crumb, .slash, .facts, .state, .review p, .side-head span, .mobile-tabs span { color: var(--muted); }
  .back { display: grid; place-items: center; width: 32px; height: 32px; padding: 0; color: var(--muted); }
  .actions { margin-left: auto; display: flex; align-items: center; gap: 8px; }
  .state { font-size: 12.5px; margin-right: 4px; }
  button { font: inherit; color: inherit; cursor: pointer; border: 0; border-radius: 6px; background: transparent; padding: 6px 10px; }
  button:hover, button.active { background: var(--selected); }
  button:disabled { opacity: .5; cursor: default; }
  .primary { padding-inline: 14px; background: var(--focus); color: var(--bg); }
  .primary:hover { background: var(--focus); opacity: .85; }
  .primary:disabled:hover { opacity: .5; }
  .outlined { border: 1px solid var(--muted); }

  .choices { flex: 1; min-height: 0; overflow-y: auto; display: flex; padding: 32px 24px; }
  .column { width: min(760px, 100%); margin: auto; }
  h2 { margin: 0 0 20px; font-size: 20px; font-weight: 600; line-height: 1.25; }
  .grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 12px; }
  .choice { display: flex; flex-direction: column; gap: 4px; min-width: 0; min-height: 168px; padding: 16px; border: 1px solid var(--line); border-radius: 12px; background: var(--raised); text-align: left; }
  .choice:hover { background: var(--hover); border-color: var(--muted); }
  .choice:focus-visible { outline: 2px solid var(--focus); outline-offset: 2px; }
  .choice-head { display: flex; align-items: center; justify-content: space-between; gap: 8px; font-size: 15px; }
  .choice-head strong { min-width: 0; overflow-wrap: anywhere; }
  .choice-head :global(svg) { flex: none; color: var(--muted); transform: rotate(-90deg); }
  .reach { color: var(--muted); font-size: 12.5px; }
  .preview { display: -webkit-box; -webkit-box-orient: vertical; -webkit-line-clamp: 4; line-clamp: 4; overflow: hidden; width: 100%; margin-top: 12px; text-align: left; color: var(--text); font-size: 13px; line-height: 1.45; white-space: pre-wrap; overflow-wrap: anywhere; }
  .preview.empty { color: var(--muted); }
  .meta { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; margin-top: auto; padding-top: 12px; color: var(--muted); font-size: 12.5px; font-variant-numeric: tabular-nums; }
  .order { margin: 16px 0 0; color: var(--muted); font-size: 12.5px; line-height: 1.45; }

  .body { display: flex; flex: 1; min-height: 0; }
  .edit { display: flex; flex: 1; min-width: 0; flex-direction: column; gap: 12px; width: 100%; max-width: 960px; margin: 0 auto; padding: 16px 24px 24px; }
  .tools { display: flex; align-items: center; justify-content: space-between; gap: 16px; }
  .facts { margin: 0; min-width: 0; font-size: 12.5px; font-variant-numeric: tabular-nums; }
  .facts span { padding: 0 4px; }
  .facts b { color: var(--text); }
  .find { display: flex; flex: 0 1 200px; align-items: center; gap: 6px; min-width: 120px; padding: 0 6px 0 10px; border: 1px solid var(--line); border-radius: 6px; color: var(--muted); font-size: 12.5px; }
  .find:focus-within { border-color: var(--focus); }
  .find input { flex: 1; min-width: 0; height: 28px; padding: 0; border: 0; outline: 0; background: transparent; color: var(--text); font: inherit; }
  kbd { padding: 0 5px; border: 1px solid var(--line); border-radius: 4px; color: var(--muted); font: inherit; font-size: 11px; }
  textarea { flex: 1; resize: none; width: 100%; min-height: 0; box-sizing: border-box; padding: 16px 20px; border: 1px solid var(--line); border-radius: 12px; background: var(--raised); color: var(--text); font: inherit; font-size: 14px; line-height: 1.5; }
  textarea:focus { outline: 2px solid var(--focus); outline-offset: -1px; }

  aside { width: 360px; flex: none; display: flex; flex-direction: column; min-height: 0; border-left: 1px solid var(--line); }
  .side-head { padding: 16px; border-bottom: 1px solid var(--line); }
  .side-head span { margin-left: 4px; font-size: 12.5px; }
  .side-head p { margin: 8px 0 0; color: var(--muted); font-size: 12.5px; }
  .review { flex: 1; overflow: auto; padding: 16px; font-size: 12.5px; }
  .review strong { display: block; margin-bottom: 12px; }
  .review p { margin: 0 0 12px; line-height: 1.45; font-variant-numeric: tabular-nums; }
  .line-number { display: block; width: 100%; border-radius: 0; background: var(--hover); color: var(--muted); text-align: left; font-size: 11px; padding: 3px 10px; }
  .diff { display: flex; gap: 8px; padding: 4px 10px; white-space: pre-wrap; overflow-wrap: anywhere; }
  .diff span { flex: none; width: 8px; }
  .diff.same { color: var(--muted); }
  .diff.add { background: color-mix(in srgb, var(--success) 12%, transparent); }
  .diff.remove { background: color-mix(in srgb, var(--danger) 12%, transparent); }
  .overwrite-note { margin: 0; padding: 12px 16px 0; color: var(--muted); font-size: 12.5px; }
  .resolve { display: flex; justify-content: space-between; padding: 12px 16px; border-top: 1px solid var(--line); }

  .mobile-tabs, .mobile-save { display: none; }
  .loading { padding: 24px; display: grid; gap: 12px; }
  .loading span { display: block; height: 14px; width: 70%; background: var(--hover); border-radius: 6px; }
  .loading span:nth-child(2) { width: 90%; }
  .error { margin: 0; padding: 16px; color: var(--muted); }
  @media (max-width: 700px) {
    header { padding: 0 8px; }
    .back { width: 40px; height: 40px; }
    .crumb, .slash, .actions { display: none; }
    .choices { padding: 20px 16px; }
    .column { margin-block: 0 auto; }
    .grid { grid-template-columns: minmax(0, 1fr); }
    .choice { min-height: 160px; }
    .mobile-tabs { display: flex; padding: 4px 8px; border-bottom: 1px solid var(--line); }
    .mobile-tabs button { min-height: 40px; border-radius: 0; }
    .mobile-tabs button.active { background: transparent; border-bottom: 2px solid var(--focus); }
    .review-title { display: none; }
    .review { font-size: 13px; }
    .mobile-hidden { display: none !important; }
    .edit { padding: 12px; }
    .facts { line-height: 1.4; }
    .find { flex-basis: 140px; }
    .find input { height: 40px; }
    .find kbd { display: none; }
    .body aside { width: 100%; border: 0; }
    .mobile-save { display: flex; align-items: center; justify-content: space-between; gap: 8px; padding: 8px 12px; border-top: 1px solid var(--line); color: var(--muted); font-size: 12.5px; }
    .mobile-save span { margin-right: auto; }
    .mobile-save button, .line-number, .error button { min-height: 40px; }
  }
</style>
