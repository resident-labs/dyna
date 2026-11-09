<script lang="ts">
  import { onMount, tick, type Snippet } from "svelte";
  import { APIError, call, SignedOut } from "../client.ts";
  import { confirm } from "../modal.ts";
  import { watch, watchHosts } from "../hosts.ts";
  import { enqueue, fieldChange, idleMinutes, poolLine, poolNodes, poolOrder, poolSummary, refusedField, requestFields, type PoolField } from "../pools.ts";
  import type { HostSummary, Pool, PoolSetRequest } from "../contract.gen.ts";
  import Icon from "./Icon.svelte";
  import StatusPill from "./StatusPill.svelte";
  import ItemList from "./settings/ItemList.svelte";
  import ListDetail from "./settings/ListDetail.svelte";
  // Settings > Pools (spec 5.13): the node pools, flat by name, and the open pool's detail, the one place that explains
  // what a pool costs, its problem, its nodes, At most, Idle time and One per session (pool.set) and removal. pool.list
  // and host.list, for each pool's nodes, are read every 10 s as Hosts reads its list. Add a pool opens, in the detail's
  // place, how a pool is added, since its script lives where it is written. The pane is admin only (routes.ts), as
  // pool.set and pool.remove are the owner's.
  let { bar, phone, navigate, signedout, selected, onselect, onclose }: {
    bar: Snippet<[string, number | undefined, Snippet | undefined]>; phone: boolean; navigate: (event: MouseEvent & { currentTarget: HTMLAnchorElement }) => void;
    signedout: () => void; selected: string | undefined; onselect: (name: string, open: boolean) => void; onclose: () => void;
  } = $props();
  let pools: Pool[] | undefined = $state.raw(), hosts: HostSummary[] = $state.raw([]);
  let failed = $state(false), busy = $state(false), creating = $state(false);
  // A refused change on the open pool, in the engine's words, under the field it is about.
  let problem: { name: string; field: PoolField; text: string } | undefined = $state();
  let items = $derived(pools && poolOrder(pools));
  let item = $derived(items?.find(each => each.name === selected));
  let anchor: HTMLElement | undefined = $state();
  const gone = (reason: unknown) => { if (reason instanceof SignedOut) signedout(); return reason instanceof SignedOut; };
  const words = (reason: unknown) => reason instanceof APIError ? reason.detail.message : "That did not work. Try again.";
  const panel = async () => { await tick(); anchor?.closest<HTMLElement>("aside")?.focus(); };

  // A change reads the list again before it settles; a beat that answers meanwhile, or any read that answers after a
  // later one started, may hold the list from before it, so it is dropped, and the next beat comes 10 s later.
  let reads = 0;
  const read = () => { const ticket = ++reads; return call("pool.list", {}).then(list => ticket === reads ? list.pools ?? [] : undefined); };
  const apply = (list: Pool[] | undefined) => { if (list) { pools = list; failed = false; } };
  const reload = () => read().then(apply, (reason: unknown) => { if (!gone(reason) && !pools) failed = true; });
  // Add is a step in history of its own, so Back leaves it as the back arrow does.
  function popped() { if (creating && !history.state?.creating) creating = false; }
  onMount(() => {
    window.addEventListener("popstate", popped);
    // A failed beat keeps the last lists, and the next one tries again.
    const stopPools = watch(read, list => { if (!busy) apply(list); }, (reason: unknown) => { if (!gone(reason) && !pools) failed = true; });
    const stopHosts = watchHosts(list => { hosts = list; }, () => {}, list => list);
    return () => { stopPools(); stopHosts(); window.removeEventListener("popstate", popped); };
  });
  // Another pool, or none, starts without the last one's problem.
  $effect.pre(() => { void selected; problem = undefined; });

  function start() {
    problem = undefined;
    // An open pool stays in the path under Add, so Back returns to it.
    if (!creating) history.pushState({ creating: true, overList: selected === undefined }, "", location.href);
    creating = true;
    void panel();
  }
  function close() {
    if (!creating) { onclose(); return; }
    if (history.state?.creating) history.back();
    else creating = false;
  }
  // A pool picked while Add is up takes Add's history step, so none is left behind that Back would land on.
  function select(key: string, open: boolean) {
    if (creating && history.state?.creating) history.replaceState({ fromList: !!history.state.overList }, "", location.href);
    creating = false;
    onselect(key, open);
  }

  // Each change saves at once and keeps the focus, and the controls stay enabled: a change made while one saves waits
  // and joins the others to its pool (enqueue), and goes when that save is done. A refusal shows the engine's words
  // under the field it is about while its pool is open; that field keeps what was typed, and the request's other fields,
  // and a refused switch, show the pool's value again.
  let queue: PoolSetRequest[] = [];
  // How each waiting or saving field shows its pool's value again, by pool and field.
  const backs = new Map<string, () => void>();
  const shown = (name: string) => pools?.find(each => each.name === name);
  function change(request: PoolSetRequest, field: PoolField, back: () => void) {
    if (problem?.name === request.name) problem = undefined;
    backs.set(`${request.name}/${field}`, back);
    queue = enqueue(queue, request);
    if (!busy) void drain();
  }
  async function drain() {
    busy = true;
    while (queue.length) {
      const [request, ...rest] = queue;
      queue = rest;
      const reason = await call("pool.set", request).then(async () => { await reload(); return undefined; }, (reason: unknown) => reason ?? "");
      const about = refusedField(request);
      // A field changed again meanwhile waits with its new value, so it is left as it is.
      for (const field of requestFields(request).filter(field => !queue.some(each => each.name === request.name && each[field] !== undefined))) {
        if (reason !== undefined && (field !== about || field === "perSession")) backs.get(`${request.name}/${field}`)?.();
        backs.delete(`${request.name}/${field}`);
      }
      if (reason !== undefined && !gone(reason) && selected === request.name) problem = { name: request.name, field: about, text: words(reason) };
    }
    busy = false;
  }
  function field(p: Pool, name: "max" | "idle", input: HTMLInputElement) {
    const back = () => { const now = shown(p.name); if (now) input.value = name === "max" ? String(now.max) : String(idleMinutes(now.idle ?? 0)); };
    const request = fieldChange(p, name, input.value);
    if (request) change(request, name, back);
    else back();
  }
  async function remove(p: Pool, opener: HTMLElement) {
    problem = undefined;
    const ok = await confirm({ title: `Remove ${p.name}?`, message: "Dyna deletes the pool and its script on the engine host.", action: "Remove", danger: true, opener, failed: words,
      run: () => call("pool.remove", { name: p.name }).catch((reason: unknown) => { gone(reason); throw reason; }) });
    if (!ok) return;
    // The engine removed it, so it leaves the list even if the next read fails.
    pools = pools?.filter(other => other.name !== p.name);
    onclose();
    await reload();
  }
  const refused = (p: Pool, field: PoolField) => problem?.name === p.name && problem.field === field;
  const plural = (n: number, one: string, many: string) => `${n.toLocaleString("en")} ${n === 1 ? one : many}`;
</script>

{#snippet row(p: Pool)}
  <span class="icon"><Icon name="stack" size={20} /></span>
  <span class="lines"><span class="title"><span class="name" title={p.name}>{p.name}</span>{#if p.problem}<StatusPill tone="warning" label="Failing" />{/if}</span>
    <span class="line">{poolLine(p)}</span></span>
{/snippet}

{#snippet empty()}<span class="small">A box adds one with the dyna-nodes skill.</span>{/snippet}

{#snippet list({ drill, select: pick }: { drill: boolean; select: (key: string, open: boolean) => void })}
  <ItemList {items} {failed} retry={() => { failed = false; void reload(); }} key={p => p.name} text={p => p.name} noun={["pool", "pools"]} label="Pools"
    {row} {empty} selected={creating ? undefined : selected} {drill} onselect={pick} />
{/snippet}

{#snippet adding()}
  <p class="small muted">A pool is added from a box with the dyna-nodes skill, and the box's request waits for your approval, or with <code>dyna pool add</code>, which reads its script from a file. The web has no form for it, as the script is written where it is tested.</p>
{/snippet}

{#snippet pool(p: Pool)}{@const nodes = poolNodes(p, hosts)}
  <p class="small muted">A pool runs its script on the engine host to create and delete machines when boxes need room, which can cost money.</p>
  {#if p.problem}<div class="section"><p class="warn">Failing. Boxes that need a node wait, and the pool tries again after a minute, doubling to an hour.</p><p class="reason">{p.problem}</p></div>{/if}
  <div class="section"><strong class="head">Nodes now <span class="n">{p.nodes.toLocaleString("en")}</span></strong>
    {#if nodes.hosts.length}<div class="nodes">{#each nodes.hosts as h (h.name)}<a class="node" href={`/settings/hosts/${encodeURIComponent(h.name)}`} onclick={navigate}>
      <span class="nname">{h.name}</span>{#if !h.reachable}<StatusPill tone="error" label="Unreachable" />{/if}<span class="go"><Icon name="forwardChevron" size={12} /></span></a>{/each}</div>{/if}
    {#if nodes.creating}<p class="small muted">{plural(nodes.creating, "node", "nodes")} being created</p>{:else if !nodes.hosts.length}<p class="small muted">None. A node comes when a box of a room whose rule names the pool needs one.</p>{/if}</div>
  <label class="field">At most<input class="input" type="number" min="1" step="1" value={p.max} aria-invalid={refused(p, "max") ? "true" : undefined} onchange={event => field(p, "max", event.currentTarget)} />
    {@render refusal(p, "max")}<span class="help">Nodes alive at once, counting those being created. A lower number deletes none; the pool makes no more until it is below it.</span></label>
  {#if !p.perSession}<label class="field">Idle time<input class="input" type="number" min="0" step="any" value={idleMinutes(p.idle ?? 0)} aria-invalid={refused(p, "idle") ? "true" : undefined} onchange={event => field(p, "idle", event.currentTarget)} />
    {@render refusal(p, "idle")}<span class="help">Minutes a node with no boxes waits before it is deleted. 0 makes the default, 30.</span></label>{/if}
  <div class="stack"><label class="set"><span class="name">One per session</span><input type="checkbox" role="switch" checked={!!p.perSession} aria-label="One per session"
    onchange={event => { const box = event.currentTarget; change({ name: p.name, perSession: box.checked }, "perSession", () => { box.checked = !!shown(p.name)?.perSession; }); }} />
    <span class="help">Each session gets a node of its own, which goes when its box is gone. Nodes already up keep the shape they were made with.</span></label>
    {@render refusal(p, "perSession")}</div>
  <div class="danger-zone"><span class="small muted">Refused while it has nodes or a room's rule names it.</span>
    <button type="button" class="btn danger" aria-haspopup="dialog" onclick={event => void remove(p, event.currentTarget)}>Remove</button></div>
{/snippet}

{#snippet refusal(p: Pool, field: PoolField)}{#if refused(p, field)}<span class="problem" role="alert">{problem?.text}</span>{/if}{/snippet}

{#snippet detail()}
  <div class="detail" bind:this={anchor}>
    {#if creating}{@render adding()}
    {:else if item}{#key item.name}{@render pool(item)}{/key}
    {:else}<p class="muted">That pool is gone, or the link is wrong.</p>{/if}
  </div>
{/snippet}

{#snippet summary()}
  {#if items}{@const sum = poolSummary(items)}<p class="muted"><span class="fact">{plural(sum.pools, "pool", "pools")}</span> <span class="fact">· {plural(sum.nodes, "node", "nodes")} now</span>
    {#if sum.failing}<span class="fact">· {sum.failing.toLocaleString("en")} failing</span>{/if}</p>{/if}
{/snippet}

{#snippet action()}<button type="button" class="btn add" onclick={start}>{#if phone}<Icon name="plus" size={16} />Add{:else}Add a pool{/if}</button>{/snippet}
{#snippet header()}{@render bar("Pools", items?.length, action)}{/snippet}

<ListDetail bar={header} empty={items?.length === 0} label="Pools" title={creating ? "Add a pool" : item ? item.name : "Not found"} open={creating || (!!items && selected !== undefined)} onselect={select} onclose={close} {list} {detail} {summary} />

<style>
  .add { margin-left: auto; gap: 4px; }
  .icon { display: grid; flex: none; color: var(--muted); }
  .detail { display: grid; gap: 18px; min-width: 0; overflow-wrap: anywhere; }
  .head { font-size: 14px; font-weight: 600; }
  .head .n { margin-left: 4px; color: var(--muted); font-weight: 400; font-variant-numeric: tabular-nums; }
  .warn { color: var(--attention); font-size: 13px; font-weight: 500; }
  .reason { padding: 10px 12px; border: 1px solid var(--line); border-radius: 8px; background: var(--raised); font: 12px/1.5 var(--mono); white-space: pre-wrap; }
  .problem { display: block; color: var(--danger); font-size: 13px; font-weight: 500; }
  .problem::first-letter { text-transform: uppercase; }
  .input[aria-invalid] { border-color: var(--danger); }
  .go { display: grid; flex: none; color: var(--muted); }
  code { font-family: var(--mono); font-size: 12px; }
  .nodes { display: grid; }
  .node { display: flex; align-items: center; gap: 10px; min-height: 32px; margin: 0 -8px; padding: 4px 8px; border-radius: 6px; color: var(--text); font-size: 13px; font-weight: 500; text-decoration: none; }
  .node:hover { background: var(--hover); }
  .node:focus-visible { outline: 2px solid var(--focus); outline-offset: -2px; }
  .nname { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-family: var(--mono); font-size: 12.5px; }
  .field { display: grid; gap: 6px; min-width: 0; font-size: 14px; font-weight: 600; }
  .help { color: var(--muted); font-size: 12.5px; font-weight: 400; line-height: 1.35; }
  .input { box-sizing: border-box; width: 120px; min-height: 34px; padding: 0 10px; border: 1px solid var(--firm); border-radius: 6px; background: var(--bg); color: var(--text); font: inherit; font-weight: 400; font-variant-numeric: tabular-nums; }
  .input:focus { outline: none; border-color: var(--focus); }
  @media (hover: none) { .input { min-height: 40px; } .node { min-height: 44px; } }
</style>
