<script lang="ts">
  import { onMount, tick, type Snippet } from "svelte";
  import { APIError, call, SignedOut } from "../client.ts";
  import { addonDetail, addonKind, addonKinds, addonOrigin, addonScopes, draftReady, emptyDraft, mcpRequest, packageRequest, skillRequest, type AddonDraft } from "../addons.ts";
  import Icon from "./Icon.svelte";
  import type { Addon, AddonKind, RoomSummary } from "../contract.gen.ts";
  // Settings > Skills & tools. Built-ins, what every room shares and what each room holds, one card per scope. A device without
  // admin reads this page and changes nothing, since the engine would refuse it.
  let { bar, admin, signedout }: { bar: Snippet<[string, number | undefined, Snippet | undefined]>; admin: boolean; signedout: () => void } = $props();
  let items: Addon[] = $state.raw([]), rooms: RoomSummary[] = $state.raw([]);
  let loaded = $state(false), failed = $state(false), busy = $state(false);
  let draft: AddonDraft | undefined = $state();
  let addButton: HTMLButtonElement | undefined = $state();
  let removing: string | undefined = $state();
  // One failed change, in the engine's words, under the row or the form it belongs to.
  let problem: { id: string; text: string } | undefined = $state();
  let scopes = $derived(addonScopes(items, rooms));
  let added = $derived(items.filter(item => !item.builtIn).length);
  const rowId = (item: Addon) => `${item.room ?? ""}/${item.kind}/${item.name}`;
  const where = (item: Addon) => item.room ? { room: item.room } : { global: true };
  const words = (error: unknown) => error instanceof APIError ? error.detail.message : "That did not work. Try again.";

  function reload() {
    return Promise.all([call("addon.list", {}), call("room.list", {})]).then(([list, known]) => { items = list.items ?? []; rooms = known.rooms ?? []; loaded = true; failed = false; },
      (error: unknown) => { if (error instanceof SignedOut) signedout(); else if (!loaded) failed = true; });
  }
  onMount(() => { void reload(); window.addEventListener("focus", reload); return () => window.removeEventListener("focus", reload); });

  async function act(id: string, run: () => Promise<unknown>, done?: () => void) {
    busy = true; problem = undefined;
    await run().then(() => { done?.(); return reload(); }, (error: unknown) => { if (error instanceof SignedOut) signedout(); else problem = { id, text: words(error) }; });
    busy = false;
  }
  async function closeForm() { draft = undefined; problem = undefined; await tick(); addButton?.focus(); }
  function add() {
    if (!draft || !draftReady(draft)) return;
    const form = draft;
    void act("form", () => form.kind === "skill" ? call("skill.save", skillRequest(form)) : form.kind === "package" ? call("package.add", packageRequest(form)) : call("mcp.add", mcpRequest(form)), () => void closeForm());
  }
  const remove = (item: Addon) => {
    const request = { ...where(item), name: item.name };
    void act(rowId(item), () => item.kind === "skill" ? call("skill.remove", request) : item.kind === "package" ? call("package.remove", request) : call("mcp.remove", request), () => { removing = undefined; void tick().then(() => addButton?.focus()); });
  };
  const toggle = (item: Addon, enabled: boolean) => act(rowId(item), () => call("addon.set_enabled", { kind: item.kind, name: item.name, room: item.room, enabled }));
  const sourceLabel: Record<AddonKind, string> = { skill: "Link", package: "Source", mcp: "URL or command" };
  const sourceHint: Record<AddonKind, string> = {
    skill: "A link to a SKILL.md or a GitHub folder. To save a folder from a box, use dyna skill save.",
    package: "npm:name, git:host/owner/repo or a repository URL. Dyna pins it to one version now.",
    mcp: "A server URL, or one executable with its arguments. No shell reads it.",
  };
</script>

{#snippet action()}{#if admin && !draft}<button type="button" class="btn add" bind:this={addButton} onclick={() => { problem = undefined; draft = emptyDraft(rooms[0]?.id ?? "global"); }}>Add</button>{/if}{/snippet}
{@render bar("Skills & tools", loaded ? added : undefined, loaded ? action : undefined)}
<div class="body" data-page-ready={loaded || failed}>
  {#if failed}<p class="load" role="alert">This list did not load. <button type="button" class="retry" onclick={() => { failed = false; void reload(); }}>Try again</button></p>
  {:else if !loaded}<div class="list" aria-label="Loading skills and tools">{#each [0, 1, 2] as row (row)}<div class="row"><i class="bone"></i><span class="lines"><i class="bone name"></i><i class="bone line"></i></span></div>{/each}</div>
  {:else}
    <p class="load">A change reaches a session at its next box start.{#if !admin}{" "}This device can read this page. Changing it needs Admin access.{/if}</p>
    {#if draft}
      <form class="form" aria-label="Add a skill or tool" onsubmit={event => { event.preventDefault(); add(); }}>
        <fieldset class="kinds"><legend>Kind</legend>
          {#each addonKinds as { kind, label } (kind)}<label class:on={draft.kind === kind}><input type="radio" name="addon-kind" value={kind} bind:group={draft.kind} disabled={busy} />{label}</label>{/each}
        </fieldset>
        <label class="field">Where
          <select bind:value={draft.scope} disabled={busy}>
            {#each rooms as room (room.id)}<option value={room.id}>{room.name}</option>{/each}
            <option value="global">All rooms</option>
          </select>
          <span>Pick a room unless every room needs it.</span>
        </label>
        {#if draft.kind === "mcp"}<label class="field">Name<input class="mono" bind:value={draft.name} disabled={busy} autocomplete="off" /></label>{/if}
        <label class="field">{sourceLabel[draft.kind]}<input class="mono" bind:value={draft.source} disabled={busy} autocomplete="off" spellcheck="false" />
          <span>{sourceHint[draft.kind]}</span></label>
        {#if problem?.id === "form"}<p class="problem" role="alert">{problem.text}</p>{/if}
        <span class="choices"><button type="submit" class="btn fill" disabled={busy || !draftReady(draft)}>{busy ? "Adding…" : "Add"}</button>
          <button type="button" class="btn ghost" disabled={busy} onclick={() => void closeForm()}>Cancel</button></span>
      </form>
    {/if}
    {#each scopes as scope (scope.id)}
      <section aria-labelledby={`addons-${scope.id}`}>
        <header class="scope"><h2 id={`addons-${scope.id}`}>{scope.title}</h2><span>{scope.items.length ? scope.note : "Nothing added yet."}</span></header>
        {#if scope.items.length}<div class="list">
          {#each scope.items as item (rowId(item))}{@const id = rowId(item)}{@const kind = addonKind(item.kind)}
            <div class="row" class:off={item.disabled}>
              <Icon name={kind.icon} size={20} />
              <span class="lines">
                <span class="title"><span class="name">{item.name}</span><span class="badge">{kind.label}</span></span>
                <span class="line detail">{addonDetail(item)}</span>
                <span class="line">{addonOrigin(item, Date.now())}</span>
              </span>
              {#if admin && !item.builtIn}
                {#if removing === id}
                  <span class="ask">Remove?
                    <span class="choices"><button type="button" class="btn danger" disabled={busy} onclick={() => remove(item)}>Remove</button>
                    <button type="button" class="btn ghost" disabled={busy} onclick={() => { removing = undefined; }}>Cancel</button></span></span>
                {:else}
                  <span class="acts"><label class="switch"><input type="checkbox" checked={!item.disabled} disabled={busy} aria-label={`Enable ${item.name}`} onchange={event => void toggle(item, event.currentTarget.checked)} />On</label>
                    <button type="button" class="btn ghost" aria-label={`Remove ${item.name}`} onclick={() => { problem = undefined; removing = id; }}>Remove</button></span>
                {/if}
              {:else if item.disabled}<span class="badge">Off</span>{/if}
            </div>
            {#if problem?.id === id}<p class="problem" role="alert">{problem.text}</p>{/if}
          {/each}
        </div>{/if}
      </section>
    {/each}
  {/if}
</div>

<style>
  .body { flex: 1; min-height: 0; display: grid; gap: 20px; align-content: start; width: min(1040px, 100%); padding: 20px 24px; overflow-y: auto; }
  .add { margin-left: auto; }
  .load { margin: 0; color: var(--muted); font-size: 14px; }
  .retry { padding: 0; border: 0; background: none; color: var(--text); font: inherit; text-decoration: underline; cursor: pointer; }
  section { display: grid; gap: 8px; min-width: 0; }
  .scope { display: flex; align-items: baseline; flex-wrap: wrap; gap: 2px 10px; padding: 0 2px; }
  h2 { margin: 0; font-size: 14px; }
  .scope span { color: var(--muted); font-size: 13px; }
  .list, .form { padding: 6px; border: 1px solid var(--line); border-radius: 12px; background: var(--raised); }
  .list { display: grid; }
  .row { display: flex; align-items: center; gap: 12px; min-height: 52px; padding: 6px 8px; border-radius: 6px; }
  .row > :global(svg) { color: var(--muted); }
  .row.off .lines { opacity: 0.6; }
  .lines { flex: 1; min-width: 0; display: flex; flex-direction: column; line-height: 1.3; }
  .title { display: flex; align-items: center; flex-wrap: wrap; gap: 2px 6px; min-width: 0; font-size: 14px; font-weight: 600; }
  .name { min-width: 0; overflow-wrap: anywhere; }
  .badge { flex: none; color: var(--muted); font-size: 12px; font-weight: 500; }
  .line { min-width: 0; color: var(--muted); font-size: 12.5px; overflow-wrap: anywhere; }
  .detail { color: var(--text); }
  .acts, .choices { flex: none; display: inline-flex; align-items: center; gap: 4px; }
  .ask { flex: none; display: inline-flex; align-items: center; flex-wrap: wrap; gap: 8px; font-size: 13px; font-weight: 500; }
  .switch { display: inline-flex; align-items: center; gap: 6px; padding: 0 6px; color: var(--muted); font-size: 13px; font-weight: 500; cursor: pointer; }
  .switch input { appearance: none; display: grid; place-items: center; width: 16px; height: 16px; margin: 0; border: 1px solid var(--firm); border-radius: 4px; background: none; cursor: pointer; }
  .switch input:checked { border-color: var(--focus); background: var(--focus); }
  .switch input:checked::after { content: ""; width: 8px; height: 4px; margin-top: -2px; border: solid var(--bg); border-width: 0 0 2px 2px; transform: rotate(-45deg); }
  .switch input:focus-visible { outline-offset: 2px; }
  .switch input:disabled { cursor: default; }
  .problem { margin: 0; padding: 0 8px 8px 40px; color: var(--danger); font-size: 13px; font-weight: 500; overflow-wrap: anywhere; }
  .form { display: grid; gap: 14px; justify-items: start; padding: 14px; }
  .form .problem { padding: 0; }
  .kinds { display: flex; flex-wrap: wrap; align-items: center; gap: 4px; min-width: 0; margin: 0; padding: 0; border: 0; }
  legend { padding: 0 0 6px; font-size: 13px; font-weight: 600; }
  .kinds label { display: inline-flex; align-items: center; height: 30px; padding: 0 12px; border: 1px solid var(--firm); border-radius: 6px; color: var(--muted); font-size: 13px; font-weight: 500; cursor: pointer; }
  .kinds label.on { border-color: var(--focus); color: var(--text); }
  .kinds label:has(:focus-visible) { outline: 2px solid var(--focus); outline-offset: 2px; }
  .kinds input { position: absolute; opacity: 0; pointer-events: none; }
  .field { display: grid; gap: 6px; width: min(560px, 100%); font-size: 13px; font-weight: 600; }
  .field span { color: var(--muted); font-weight: 400; }
  .field input, .field select { box-sizing: border-box; width: 100%; height: 36px; padding: 0 10px; border: 1px solid var(--firm); border-radius: 6px; background: var(--bg); color: var(--text); font: inherit; font-weight: 400; }
  .mono { font-family: var(--mono, monospace); }
  .bone { display: block; flex: none; width: 20px; height: 20px; border-radius: 6px; background: var(--selected); }
  .bone.name { width: 140px; height: 12px; margin-bottom: 6px; }
  .bone.line { width: 240px; height: 10px; }
  @media (hover: none) { .kinds label { height: 40px; } .switch { min-height: 40px; } }
  @media (max-width: 640px) {
    .body { padding: 12px 16px; }
    .row { flex-wrap: wrap; padding: 6px 8px; }
    .lines { flex-basis: calc(100% - 32px); }
    .acts, .ask { width: 100%; margin-left: 32px; }
    .acts .btn { padding: 0 10px; }
    .switch { padding-left: 0; }
    .problem { padding-left: 40px; }
  }
</style>
