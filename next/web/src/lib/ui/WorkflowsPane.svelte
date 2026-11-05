<script lang="ts">
  import { onMount, type Snippet } from "svelte";
  import { APIError, call, SignedOut } from "../client.ts";
  import { boxSizes, clearRequest, duration, maxCount, maxTimeout, numberChange, overrides, profileChoices, profilesChange, scopeKey, scopeLine, scopeName, scopeOrder,
    refusedSetting, serial, setRequest, settingShape, source, starterOrder, starters, valueText, workflowChips, workflowSummary } from "../workflows.ts";
  import type { ProfileName, WorkflowScope, WorkflowSetRequest, WorkflowSetting, WorkflowSettingInfo, WorkflowSettingsResponse } from "../contract.gen.ts";
  import Avatar from "./Avatar.svelte";
  import Icon from "./Icon.svelte";
  import ItemList from "./settings/ItemList.svelte";
  import ListDetail from "./settings/ListDetail.svelte";
  // Settings > Workflows (spec 5.13, 9.2): the global default and every open room, each with the workflow settings it sets,
  // and the open scope's detail: each setting in the engine's order with the engine's own line of help, where its value
  // comes from and a control typed to it. Each change is one workflow.set, queued so they go in order; a value the scope
  // sets can return to inheriting. The pane is admin only (routes.ts), as workflow.settings and workflow.set are the owner's.
  let { bar, signedout, selected, onselect, onclose }: {
    bar: Snippet<[string, number | undefined, Snippet | undefined]>; signedout: () => void;
    selected: string | undefined; onselect: (key: string, open: boolean) => void; onclose: () => void;
  } = $props();
  let settings: WorkflowSettingsResponse | undefined = $state.raw();
  let listed: ProfileName[] = $state.raw([]);
  let failed = $state(false);
  // A refused change on the open scope, in the engine's words, under the setting it was about.
  let problem: { key: string; setting: WorkflowSetting; text: string } | undefined = $state();
  let order = $derived((settings?.settings ?? []).map(info => info.setting));
  let items = $derived(settings && scopeOrder(settings.global, settings.rooms ?? []));
  let item = $derived(items?.find(each => scopeKey(each) === selected));
  let chips = $derived(workflowChips(order));
  const gone = (reason: unknown) => { if (reason instanceof SignedOut) signedout(); return reason instanceof SignedOut; };
  const words = (reason: unknown) => reason instanceof APIError ? reason.detail.message : "That did not work. Try again.";

  // A read that answers after a later one started, or while a change waits or saves, may hold the settings from before
  // it, so it is dropped; each change reads them again once it is saved.
  const queue = serial();
  let reads = 0;
  const read = () => { const ticket = ++reads; return call("workflow.settings", {}).then(answer => ticket === reads ? answer : undefined); };
  const reload = () => read().then(answer => { if (answer && !queue.pending()) { settings = answer; failed = false; } },
    (reason: unknown) => { if (!gone(reason) && !settings) failed = true; });
  onMount(() => {
    void reload();
    // The profiles to pick from; without them the boxes show the profiles in force.
    call("profile.list", {}).then(list => { listed = (list.profiles ?? []).map(entry => entry.profile.name).sort((a, b) => a.localeCompare(b, "en", { numeric: true })); }, gone);
    window.addEventListener("focus", reload);
    return () => window.removeEventListener("focus", reload);
  });
  // Another scope, or none, starts without the last one's problem.
  $effect.pre(() => { void selected; problem = undefined; });

  // Saves at once and keeps the focus; no control is disabled. A refusal shows under its setting while its scope is
  // open; back shows the value in force again, and a number field has none, so it keeps what was typed.
  function change(scope: WorkflowScope, setting: WorkflowSetting, request: WorkflowSetRequest | undefined, back?: () => void) {
    const key = scopeKey(scope);
    if (!request) { back?.(); return; }
    if (problem?.key === key) problem = undefined;
    void queue(async () => { await call("workflow.set", request); return read(); }).then(answer => { if (answer) { settings = answer; failed = false; } }, (reason: unknown) => {
      back?.();
      const about = refusedSetting(request, reason instanceof APIError ? reason.detail.workflow?.setting : undefined) ?? setting;
      if (!gone(reason) && selected === key) problem = { key, setting: about, text: words(reason) };
    });
  }
  const now = (scope: WorkflowScope) => items?.find(each => scopeKey(each) === scopeKey(scope))?.effective ?? scope.effective;
  function pickStarter(scope: WorkflowScope, select: HTMLSelectElement) {
    const value = starterOrder.find(each => each === select.value);
    const back = () => { select.value = now(scope).start; };
    change(scope, "start", value && value !== scope.effective.start ? setRequest(scope, { setting: "start", value }) : undefined, back);
  }
  function pickSize(scope: WorkflowScope, select: HTMLSelectElement) {
    const value = boxSizes.find(each => each === select.value);
    const back = () => { select.value = now(scope).size ?? ""; };
    // The engine's choice is no value: it is the unset size, so choosing it returns the size to inheriting.
    change(scope, "size", value ? setRequest(scope, { setting: "size", value }) : source(scope, "size") === "here" ? clearRequest(scope, "size") : undefined, back);
  }
  function type(scope: WorkflowScope, setting: "maxRuns" | "maxWorkers" | "timeout", input: HTMLInputElement) {
    const request = numberChange(scope, setting, input.value);
    if (request) change(scope, setting, request);
    else input.value = String(scope.effective[setting]);
  }
  const capital = (text: string) => text.charAt(0).toUpperCase() + text.slice(1);
  const refused = (scope: WorkflowScope, setting: WorkflowSetting) => problem?.key === scopeKey(scope) && problem.setting === setting;
  const from = { here: "Set here", global: "From global", default: "Default" };
  const plural = (n: number, one: string, many: string) => `${n.toLocaleString("en")} ${n === 1 ? one : many}`;
</script>

{#snippet row(scope: WorkflowScope)}{@const own = overrides(scope, order).length}
  {#if scope.room}<Avatar kind="room" name={scopeName(scope)} size={24} />{:else}<span class="icon"><Icon name="globe" size={20} /></span>{/if}
  <span class="lines"><span class="title"><span class="name" title={scopeName(scope)}>{scopeName(scope)}</span>{#if scope.room && own}<span class="badge">Overrides {own.toLocaleString("en")}</span>{/if}</span>
    <span class="line">{scopeLine(scope, order)}</span></span>
{/snippet}

{#snippet list({ drill, select }: { drill: boolean; select: (key: string, open: boolean) => void })}
  <ItemList {items} {failed} retry={() => { failed = false; void reload(); }} key={scopeKey} text={scope => scope.room ? scopeName(scope) : ""} noun={["scope", "scopes"]} label="Workflow settings"
    {chips} {row} {selected} {drill} onselect={select} />
{/snippet}

{#snippet control(scope: WorkflowScope, setting: WorkflowSetting)}{@const label = settingShape[setting].label}{@const e = scope.effective}
  {#if setting === "start"}<select class="input" aria-label={label} value={e.start} onchange={event => pickStarter(scope, event.currentTarget)}>
      {#each starterOrder as value (value)}<option {value}>{capital(starters[value])}</option>{/each}</select>
  {:else if setting === "size"}<select class="input" aria-label={label} value={e.size ?? ""} onchange={event => pickSize(scope, event.currentTarget)}>
      <option value="">Engine's choice</option>{#each boxSizes as value (value)}<option {value}>{capital(value)}</option>{/each}</select>
  {:else if setting === "maxRuns" || setting === "maxWorkers" || setting === "timeout"}{@const field = setting}
    <span class="number"><input class="input" type="number" aria-label={label} min={field === "timeout" ? 1 : 0} max={field === "timeout" ? maxTimeout : maxCount} step="1" value={e[field]}
      aria-invalid={refused(scope, field) ? "true" : undefined} onchange={event => type(scope, field, event.currentTarget)} />
      <span class="unit">{field === "timeout" ? duration(e.timeout) : e[field] ? "" : "No limit"}</span></span>
  {:else if setting === "profiles"}<span class="value">{capital(valueText(e, "profiles"))}</span>
  {:else}<input type="checkbox" role="switch" aria-label={label} checked={e[setting]}
      onchange={event => { const box = event.currentTarget; change(scope, setting, setRequest(scope, { setting, value: box.checked }), () => { box.checked = now(scope)[setting]; }); }} />
  {/if}
{/snippet}

{#snippet setting(scope: WorkflowScope, info: WorkflowSettingInfo)}{@const where = source(scope, info.setting)}
  <div class="set">
    <span class="name">{settingShape[info.setting].label} <span class="from" class:here={where === "here"}>{from[where]}</span></span>
    {@render control(scope, info.setting)}
    <span class="help">{info.help}</span>
    {#if info.setting === "profiles"}<span class="more boxes" role="group" aria-label="Profiles">{#each profileChoices(listed, scope.effective) as choice (choice.name)}
      <label class="pick"><input type="checkbox" checked={choice.on}
        onchange={event => { const box = event.currentTarget; change(scope, "profiles", profilesChange(scope, listed, choice.name, box.checked), () => { box.checked = profileChoices(listed, now(scope)).find(each => each.name === choice.name)?.on ?? false; }); }} />{choice.name}</label>{/each}</span>{/if}
    {#if where === "here" || refused(scope, info.setting)}<span class="more foot">
      {#if refused(scope, info.setting)}<span class="problem" role="alert">{problem?.text}</span>{/if}
      {#if where === "here"}<button type="button" class="btn ghost reset" onclick={() => change(scope, info.setting, clearRequest(scope, info.setting))}>{scope.room ? "Use global" : "Use default"}</button>{/if}</span>{/if}
  </div>
{/snippet}

{#snippet detail()}
  {#if item}{@const scope = item}{@const all = settings?.settings ?? []}
    <p class="small muted">{#if scope.room}A value set here wins over the global default; the rest follow it.{:else}These values hold in every room that does not set its own. A value not set here is Dyna's built-in default.{/if}</p>
    {#key scopeKey(scope)}
      <div class="section"><strong class="head">Runs</strong><div class="stack">{#each all.filter(info => settingShape[info.setting].kind !== "switch") as info (info.setting)}{@render setting(scope, info)}{/each}</div></div>
      <div class="section"><strong class="head">Script features</strong><div class="stack">{#each all.filter(info => settingShape[info.setting].kind === "switch") as info (info.setting)}{@render setting(scope, info)}{/each}</div></div>
    {/key}
  {:else}<p class="muted">That room is gone, or the link is wrong.</p>{/if}
{/snippet}

{#snippet summary()}
  {#if items}{@const sum = workflowSummary(items, order)}<p class="muted"><span class="fact">{plural(sum.rooms, "room", "rooms")}</span> <span class="fact">· {sum.overriding.toLocaleString("en")} with overrides</span>
    <span class="fact">· the global default sets {plural(sum.global, "value", "values")}</span></p>{/if}
{/snippet}

{#snippet header()}{@render bar("Workflows", items?.length, undefined)}{/snippet}

<ListDetail bar={header} label="Workflows" title={item ? scopeName(item) : "Not found"} open={!!items && selected !== undefined} {onselect} {onclose} {list} {detail} {summary} />

<style>
  .icon { display: grid; place-items: center; flex: none; width: 24px; color: var(--muted); }
  .head { font-size: 14px; font-weight: 600; }
  .set { cursor: default !important; }
  .from { margin-left: 4px; color: var(--muted); font-size: 12px; font-weight: 400; }
  .from.here { color: var(--text); font-weight: 500; }
  .more { grid-column: 1 / -1; }
  .foot { display: flex; flex-wrap: wrap; align-items: center; gap: 4px 12px; margin-top: 4px; }
  .reset { height: 26px; margin-left: -12px; padding: 0 12px; font-size: 12.5px; }
  .problem { flex-basis: 100%; color: var(--danger); font-size: 13px; font-weight: 500; overflow-wrap: anywhere; }
  .problem::first-letter { text-transform: uppercase; }
  .value { max-width: 160px; color: var(--muted); font-size: 13px; text-align: right; overflow-wrap: anywhere; }
  .number { display: grid; justify-items: end; gap: 2px; }
  .unit { color: var(--muted); font-size: 12px; }
  .unit:empty { display: none; }
  .boxes { display: flex; flex-wrap: wrap; gap: 6px; margin-top: 6px; }
  .pick { display: inline-flex; align-items: center; gap: 6px; min-height: 28px; padding: 0 10px 0 8px; border: 1px solid var(--line); border-radius: 6px; background: var(--bg); font-family: var(--mono); font-size: 12.5px; cursor: pointer; }
  .pick:has(input:checked) { border-color: var(--firm); }
  .pick:has(input:focus-visible) { outline: 2px solid var(--focus); outline-offset: 1px; }
  .input { box-sizing: border-box; min-height: 32px; padding: 0 8px; border: 1px solid var(--firm); border-radius: 6px; background: var(--bg); color: var(--text); font: inherit; font-size: 13.5px; }
  input.input { width: 110px; font-variant-numeric: tabular-nums; }
  .input:focus { outline: none; border-color: var(--focus); }
  .input[aria-invalid] { border-color: var(--danger); }
  @media (hover: none) { .input { min-height: 40px; } .pick { min-height: 40px; } }
</style>
