<script lang="ts">
  import { onMount, tick } from "svelte";
  import { APIError, call } from "../client.ts";
  import { instructionDiff } from "../instruction-diff.ts";
  import StatusPill from "./StatusPill.svelte";

  let { room, roomName, sessions, onclose }: { room: string; roomName: string; sessions: number; onclose: () => void } = $props();
  let selected = $state<"room" | "global">("room");
  let base = $state(""), hash = $state(""), savedAt = $state<string>(), draft = $state("");
  let other = $state<{ text: string; hash: string; savedAt?: string | null }>();
  let peer = $state<{ text: string; savedAt?: string | null }>({ text: "" });
  let loading = $state(true), failed = $state(false), saveFailed = $state(false), saving = $state(false);
  let mobile = $state<"edit" | "changes">("edit");
  let search = $state("");
  let editor: HTMLTextAreaElement, finder: HTMLInputElement;
  let generation = 0;
  const prefix = () => `dyna.instructions.${selected === "room" ? room : "global"}.`;
  const request = () => selected === "room" ? { room } : {};
  let changes = $derived(instructionDiff(base, draft));
  let changed = $derived(changes.filter(line => line.kind !== "same"));
  let tokens = $derived(Math.ceil(draft.length / 4));
  let delta = $derived(tokens - Math.ceil(base.length / 4));
  let count = $derived(draft.split("\n").filter(line => /^\s*[-*] /.test(line)).length);
  let savedLabel = $derived(savedAt ? `Saved ${relative(savedAt)}` : "Not saved yet");
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
  async function load() {
    const current = ++generation, key = prefix(), req = request();
    loading = true; failed = false; saveFailed = false; other = undefined;
    try {
      const [result, companion] = await Promise.all([call("instructions.get", req), call("instructions.get", selected === "room" ? {} : { room })]);
      if (current !== generation) return;
      peer = companion;
      base = result.text; hash = result.hash; savedAt = result.savedAt ?? undefined; draft = result.text;
      const storedKey = Object.keys(localStorage).find(item => item.startsWith(key));
      if (storedKey) {
        const raw: unknown = JSON.parse(localStorage.getItem(storedKey) ?? "null");
        if (raw && typeof raw === "object" && "text" in raw && typeof raw.text === "string" &&
          "base" in raw && typeof raw.base === "string") {
          draft = raw.text;
          if (storedKey !== key + result.hash) {
            base = raw.base; hash = storedKey.slice(key.length); other = result;
          }
        }
      }
    } catch { if (current === generation) failed = true; }
    finally { if (current === generation) loading = false; }
  }
  async function refresh() {
    if (loading || failed || saving) return;
    const current = generation;
    try {
      const result = await call("instructions.get", request());
      if (current !== generation) return;
      if (result.hash !== hash) {
        if (draft !== base) other = result;
        else { base = draft = result.text; hash = result.hash; savedAt = result.savedAt ?? undefined; }
      }
    } catch { /* The open editor keeps its draft when the engine is offline. */ }
  }
  async function save(overwrite = false) {
    if (saving || loading || failed || draft === base) return;
    saving = true; saveFailed = false;
    const text = draft;
    try {
      const result = await call("instructions.set", { ...request(), text, base: overwrite && other ? other.hash : hash });
      clearDrafts(); hash = result.hash; base = text; other = undefined;
      keepDraft();
      const current = await call("instructions.get", request());
      savedAt = current.savedAt ?? undefined;
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
  function discard() { clearDrafts(); draft = base; other = undefined; }
  function find() {
    if (!search || !editor) return;
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
    if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === "s") { event.preventDefault(); void save(); }
    else if (event.key === "/" && !(event.target instanceof HTMLInputElement) && !(event.target instanceof HTMLTextAreaElement)) {
      event.preventDefault(); finder?.focus();
    }
  }
  onMount(() => { void load(); window.addEventListener("focus", refresh); return () => { generation++; window.removeEventListener("focus", refresh); }; });
  function select(which: "room" | "global") { if (!saving && selected !== which) { selected = which; mobile = "edit"; void load(); } }
</script>

<svelte:window onkeydown={keys} />
<div class="instructions">
  <header>
    <button class="back" type="button" aria-label="Back to room" onclick={onclose}>‹</button>
    <span class="crumb">{roomName}</span><span class="slash">/</span><strong>Instructions</strong>
    <nav class="files" aria-label="Instruction file">
      <button type="button" class:active={selected === "room"} onclick={() => select("room")}>{roomName}</button>
      <button type="button" class:active={selected === "global"} onclick={() => select("global")}>All rooms</button>
    </nav>
  </header>
  {#if loading}
    <div class="loading" aria-label="Loading instructions"><span></span><span></span><span></span></div>
  {:else if failed}
    <p class="error">Instructions could not be loaded or saved. <button type="button" onclick={() => void load()}>Retry</button></p>
  {:else}
    {#if saveFailed}<p class="error" role="status">Your changes were not saved. <button type="button" onclick={() => void save()}>Retry</button></p>{/if}
    <nav class="mobile-tabs" aria-label="Editor view">
      <button type="button" class:active={mobile === "edit"} onclick={() => mobile = "edit"}>Edit</button>
      <button type="button" class:active={mobile === "changes"} onclick={() => mobile = "changes"}>Changes {changed.length || ""}</button>
    </nav>
    <div class="body">
      <section class:mobile-hidden={mobile !== "edit"} class="edit" aria-label="Instruction editor">
        <p class="facts">{selected === "room" ? `Every session in ${roomName} reads this file after the file for all rooms.` : "Every session reads this file first."}
          <span>·</span> <b>{count}</b> items <span>·</span> about <b>{tokens.toLocaleString()}</b> tokens <span>·</span> {savedLabel.toLowerCase()}</p>
        <label class="find">Find in file <input bind:this={finder} bind:value={search} onkeydown={event => { if (event.key === "Enter") { event.preventDefault(); find(); } }} placeholder="Find text" /></label>
        <textarea bind:this={editor} value={draft} oninput={event => { draft = event.currentTarget.value; keepDraft(); }} spellcheck="false"
          placeholder={selected === "room" ? `Write instructions for every session in ${roomName}` : "Write instructions for every session"} aria-label="Markdown instructions"></textarea>
        <p class="editor-foot">{draft === base ? savedLabel : `${changed.length} lines changed`}</p>
      </section>
      <aside class:mobile-hidden={mobile !== "changes"} aria-label="Instruction review">
        {#if other}
          <div class="side-head"><StatusPill tone="warning" label="Changed on another device" /><p>Nothing was saved. Your text is unchanged.</p></div>
          <div class="review"><strong>Their change</strong>
            {#each instructionDiff(base, other.text).filter(line => line.kind !== "same") as line}
              <button class="line-number" type="button" onclick={() => void jump(line.line)}>Line {line.line}</button>
              <div class:add={line.kind === "add"} class:remove={line.kind === "remove"} class="diff"><span>{line.kind === "add" ? "+" : "−"}</span>{line.text}</div>
            {/each}
            <strong>Yours</strong>
            {#each changed as line}
              <button class="line-number" type="button" onclick={() => void jump(line.line)}>Line {line.line}</button>
              <div class:add={line.kind === "add"} class:remove={line.kind === "remove"} class="diff"><span>{line.kind === "add" ? "+" : "−"}</span>{line.text}</div>
            {/each}
          </div>
        {:else if changed.length}
          <div class="side-head"><strong>What sessions get</strong> <span>{changed.length} lines changed</span></div>
          <div class="review">
            <p>{delta >= 0 ? "Adds" : "Removes"} about {Math.abs(delta).toLocaleString()} tokens {delta >= 0 ? "to" : "from"} every prompt · {sessions} open sessions in {roomName}</p>
            {#each changed as line}
              <button class="line-number" type="button" onclick={() => void jump(line.line)}>Line {line.line}</button>
              <div class:add={line.kind === "add"} class:remove={line.kind === "remove"} class="diff"><span>{line.kind === "add" ? "+" : "−"}</span>{line.text}</div>
            {/each}
            <p>Running sessions get these changed lines at their next run.</p>
          </div>
        {:else}
          <div class="side-head"><strong>Read by</strong></div>
          <div class="review"><strong>Sessions in {roomName} read, in this order</strong>
            <ol><li>All rooms <span>{selected === "global" ? (!savedAt ? "No file yet" : `about ${tokens.toLocaleString()} tokens`) : (!peer.savedAt ? "No file yet" : `about ${Math.ceil(peer.text.length / 4).toLocaleString()} tokens`)}</span></li>
              <li>{roomName} <span>{selected === "room" ? (!savedAt ? "No file yet" : `about ${tokens.toLocaleString()} tokens`) : (!peer.savedAt ? "No file yet" : `about ${Math.ceil(peer.text.length / 4).toLocaleString()} tokens`)}</span></li>
              <li>Each repository’s AGENTS.md</li></ol>
            <p>Changes reach running sessions at their next run.</p>
          </div>
        {/if}
        <div class="actions">
          {#if other}<button type="button" onclick={useTheirs}>Use theirs</button><button class="primary" type="button" disabled={saving} onclick={() => void save(true)}>Save mine over it</button>
          {:else}<button type="button" disabled={draft === base} onclick={discard}>Discard</button><button class="primary" type="button" disabled={saving || draft === base} onclick={() => void save()}>Save</button>{/if}
        </div>
      </aside>
    </div>
    <div class="mobile-save">
      <span>{draft === base ? savedLabel : `${changed.length} lines changed`}</span>
      {#if other}<button type="button" onclick={useTheirs}>Use theirs</button><button class="primary" type="button" onclick={() => void save(true)}>Save mine</button>
      {:else}<button class="primary" type="button" disabled={saving || draft === base} onclick={() => void save()}>Save</button>{/if}
    </div>
  {/if}
</div>

<style>
  .instructions { position: absolute; inset: 0; z-index: 4; display: flex; flex-direction: column; background: var(--bg); color: var(--text); }
  header { height: 52px; flex: none; display: flex; align-items: center; gap: 8px; padding: 0 16px; border-bottom: 1px solid var(--line); }
  .crumb, .slash, .facts, .editor-foot, .review p, .side-head span { color: var(--muted); }
  .back { font-size: 24px; }
  .files { margin-left: auto; display: flex; gap: 4px; }
  button { font: inherit; color: inherit; cursor: pointer; border: 0; border-radius: 6px; background: transparent; padding: 6px 10px; }
  button:hover, button.active { background: var(--selected); }
  button:disabled { opacity: .5; cursor: default; }
  .body { display: flex; flex: 1; min-height: 0; }
  .edit { display: flex; flex: 1; min-width: 0; flex-direction: column; gap: 8px; padding: 16px 24px; }
  .facts { margin: 0; font-size: 12.5px; font-variant-numeric: tabular-nums; }
  .facts span { padding: 0 4px; }.facts b { color: var(--text); }
  .find { display: flex; align-items: center; gap: 8px; color: var(--muted); font-size: 12.5px; }
  input { background: var(--bg); color: var(--text); border: 1px solid var(--line); border-radius: 6px; padding: 6px 8px; font: inherit; }
  textarea { flex: 1; resize: none; width: 100%; min-height: 0; box-sizing: border-box; background: var(--bg); color: var(--text);
    border: 1px solid var(--line); border-radius: 6px; padding: 12px 16px; font: inherit; line-height: 1.45; }
  textarea:focus { outline: 2px solid var(--focus); outline-offset: -1px; }
  .editor-foot { margin: 0; font-size: 12.5px; }
  aside { width: 380px; flex: none; display: flex; flex-direction: column; border-left: 1px solid var(--line); min-height: 0; }
  .side-head { padding: 16px; border-bottom: 1px solid var(--line); }.side-head span { font-size: 12.5px; margin-left: 4px; }.side-head p { color: var(--muted); font-size: 12.5px; margin: 8px 0 0; }
  .review { flex: 1; overflow: auto; padding: 16px; font-size: 12.5px; }.review strong { display: block; margin-bottom: 12px; }.review p { line-height: 1.45; font-variant-numeric: tabular-nums; }.review ol { padding-left: 28px; }.review li { padding: 8px 0; }.review li span { float: right; color: var(--muted); font-variant-numeric: tabular-nums; }
  .line-number { display: block; width: 100%; border-radius: 0; background: var(--hover); color: var(--muted); text-align: left; font-size: 11px; padding: 3px 10px; }
  .diff { display: flex; gap: 8px; padding: 4px 10px; white-space: pre-wrap; overflow-wrap: anywhere; }.diff span { flex: none; }.diff.add { background: color-mix(in srgb, var(--success) 12%, transparent); }.diff.remove { background: color-mix(in srgb, var(--danger) 12%, transparent); }
  .actions { display: flex; justify-content: space-between; padding: 12px 16px; border-top: 1px solid var(--line); }.primary { background: var(--focus); color: var(--bg); }.primary:hover { background: var(--focus); opacity: .85; }
  .mobile-tabs, .mobile-save { display: none; }.loading { padding: 24px; display: grid; gap: 12px; }.loading span { display: block; height: 14px; width: 70%; background: var(--hover); border-radius: 6px; }.loading span:nth-child(2) { width: 90%; }.error { color: var(--muted); padding: 16px; }
  @media (max-width: 700px) {
    header { padding: 0 8px; }.crumb, .slash { display: none; }.files button { min-height: 40px; }
    .mobile-tabs { display: flex; padding: 4px 8px; border-bottom: 1px solid var(--line); }.mobile-tabs button { min-height: 40px; }
    .mobile-hidden { display: none !important; }.edit { padding: 12px; }.facts { line-height: 1.4; }.find input { min-height: 40px; }.body aside { width: 100%; border: 0; }.actions { display: none; }
    .mobile-save { display: flex; align-items: center; justify-content: space-between; gap: 8px; padding: 8px 12px; border-top: 1px solid var(--line); color: var(--muted); font-size: 12.5px; }.mobile-save button { min-height: 40px; }.back { min-width: 40px; min-height: 40px; }
  }
</style>
