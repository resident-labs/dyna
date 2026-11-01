<script lang="ts">
  import { onMount, tick } from "svelte";
  import { requestDecision } from "../request-decision.ts";
  import { hue } from "../identity.ts";
  import { closedSession, fromNow } from "../feed.ts";
  import { requestGroups, requestOrder, requestsFor } from "../request-groups.ts";
  import type { Request, RoomStore } from "../room.svelte.ts";
  import type { Question } from "../contract.gen.ts";
  import Avatar from "./Avatar.svelte";
  import RequestCard from "./RequestCard.svelte";
  import RequestRow from "./RequestRow.svelte";
  import QuestionCard from "./QuestionCard.svelte";
  let { store, session, admin, operate, anchor, onclose, element = $bindable() }: { store: RoomStore; session?: string; admin: boolean; operate: boolean; anchor?: HTMLElement; onclose: () => void; element?: HTMLElement } = $props();
  // Like the Jobs popover, it hangs from its button: a desktop's right edge lines up with the button's.
  let right = $state<number>();
  const groupThreshold = 12, initialGroups = 3;
  let now = $state(Date.now()), all = $state(false), moreGroups = $state(false), chosen = $state("");
  let expanded = $state<string[]>([]), denying = $state(""), reason = $state(""), failure = $state("");
  let touchStart = 0;
  let rows: Record<string, ReturnType<typeof RequestRow>> = $state({});
  let requests = $derived(requestsFor(store.requests.values(), session));
  let questions = $derived(store.pendingQuestions(session));
  let answered = $derived([...store.questions.values()].filter(item => item.state === "answered" && (!session || item.session === session) && new Date(item.answered ?? 0).toDateString() === new Date(now).toDateString()));
  let waiting = $derived(requests.filter(item => item.state === "pending" && item.class === "owner")
    .sort((a, b) => Date.parse(a.expires) - Date.parse(b.expires)));
  let shared = $derived(requests.filter(item => item.state === "pending" && item.class !== "owner")
    .sort((a, b) => Date.parse(a.expires) - Date.parse(b.expires)));
  let groups = $derived(requestGroups(waiting));
  let pending = $derived(requestOrder(waiting, shared, groups, waiting.length > groupThreshold));
  let decided = $derived(requests.filter(item => item.decision && new Date(item.decision.at).toDateString() === new Date(now).toDateString())
    .sort((a, b) => (b.decision?.at ?? "").localeCompare(a.decision?.at ?? "")));
  let current = $derived(pending.some(item => item.id === chosen) ? chosen : pending[0]?.id);
  function keys(event: KeyboardEvent) {
    if (event.target instanceof HTMLInputElement || event.target instanceof HTMLTextAreaElement || event.metaKey || event.ctrlKey || event.altKey || event.shiftKey || !current) return;
    const at = pending.findIndex(item => item.id === current), key = event.key.toLowerCase();
    if (key === "j" || key === "k") {
      const next = pending[Math.max(0, Math.min(pending.length - 1, at + (key === "j" ? 1 : -1)))];
      chosen = next.id;
      const group = next.by.session ?? "";
      if (!expanded.includes(group)) expanded = [...expanded, group];
      moreGroups = true;
    }
    else if (admin && (key === "a" || key === "d") && !event.repeat) {
      const id = current;
      void tick().then(() => {
        element?.querySelector(".current")?.scrollIntoView({ block: "nearest" });
        if (id === current) void rows[id]?.decide(key === "a");
      });
    }
    else return;
    event.preventDefault();
  }
  async function denyGroup(items: Request[]) {
    failure = "";
    const results = await Promise.allSettled(items.map(item => requestDecision(admin, { id: item.id, approve: false, reason: reason.trim() || undefined })));
    if (results.some(result => result.status === "rejected")) failure = "Some decisions were not saved. Try again.";
    else { denying = ""; reason = ""; }
  }
  $effect(() => {
    void current; void expanded; void moreGroups;
    void tick().then(() => element?.querySelector(".current")?.scrollIntoView({ block: "nearest" }));
  });
  onMount(() => {
    const parent = element?.offsetParent;
    if (anchor?.offsetParent && parent && window.matchMedia("(min-width: 641px)").matches) right = parent.getBoundingClientRect().right - anchor.getBoundingClientRect().right;
    element?.focus();
    const timer = setInterval(() => { now = Date.now(); }, 30_000); return () => clearInterval(timer); });
</script>
{#snippet row(request: Request, grouped = false)}
  <RequestRow bind:this={() => rows[request.id], value => { if (value) rows[request.id] = value; else delete rows[request.id]; }} row={{ kind: "approval", request }} {admin} {store} {now} {grouped} current={request.id === current} />
{/snippet}
{#snippet asked(question: Question)}
  <QuestionCard {question} name={store.names.get(question.session) ?? closedSession} root={!!store.sessions.get(question.session) && !store.sessions.get(question.session)?.parent} {operate} />
{/snippet}
<!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
<section class="panel" aria-label="Requests" tabindex="-1" style:right={right === undefined ? null : `${right}px`} bind:this={element} onkeydown={keys}>
  <!-- svelte-ignore a11y_no_static_element_interactions -->
  <div class="handle" ontouchstart={event => { touchStart = event.touches[0].clientY; }} ontouchend={event => { if (event.changedTouches[0].clientY - touchStart > 60) onclose(); }}><span></span></div>
  <header><div><strong>Requests</strong>{#if waiting.length + questions.length}<span>{waiting.length + questions.length} waiting</span>{/if}</div><button type="button" aria-label="Close requests" onclick={onclose}>×</button></header>
  <div class="list">
    {#each questions as question (question.id)}{@render asked(question)}{/each}
    {#if pending.length && !admin}<p class="none">Needs a device with full access.</p>{/if}
    {#if waiting.length > groupThreshold}
      {#each moreGroups ? groups : groups.slice(0, initialGroups) as [id, items] (id)}
        {@const name = id ? store.names.get(id) ?? closedSession : "Dyna"}
        <div class="group"><Avatar kind="session" {name} size={24} /><strong style:--hue={hue(name)}>{name}</strong><span>{items.length} waiting · earliest expires {fromNow(items[0].expires, now)}</span>
          {#if admin}<button type="button" onclick={() => { denying = id || "engine"; reason = ""; }}>Deny all {items.length}…</button>{/if}</div>
        {#if denying === (id || "engine")}<div class="deny"><input aria-label="Note for all denials" placeholder="Add a note" bind:value={reason} /><button type="button" onclick={() => void denyGroup(items)}>Confirm denial</button><button type="button" onclick={() => { denying = ""; }}>Cancel</button></div>{/if}
        {#each expanded.includes(id) ? items : items.slice(0, 1) as request (request.id)}{@render row(request, true)}{/each}
        {#if items.length > 1 && !expanded.includes(id)}<button class="more" type="button" onclick={() => { expanded = [...expanded, id]; }}>Show {items.length - 1} more from this session</button>{/if}
      {/each}
      {#if groups.length > initialGroups && !moreGroups}<button class="more" type="button" onclick={() => { moreGroups = true; }}>{groups.length - initialGroups} more sessions · {groups.slice(initialGroups).reduce((n, group) => n + group[1].length, 0)} requests</button>{/if}
    {:else}
      {#each waiting as request (request.id)}{@render row(request)}{:else}{#if store.ready && !questions.length}<p class="none">Nothing waits for you.</p>{/if}{/each}
    {/if}
    {#if shared.length}<h2>Waiting for a session <span>{shared.length}</span></h2>
      {#each shared as request (request.id)}{@render row(request)}{/each}
    {/if}
    {#if decided.length || answered.length}
      <h2>Decided today <span>{decided.length + answered.length}</span></h2>
      {#each answered as question (question.id)}{@render asked(question)}{/each}
      {#each all ? decided : decided.slice(0, 5) as request (request.id)}<RequestCard {admin} {request} {store} {now} />{/each}
      {#if decided.length > 5 && !all}<button class="more" type="button" onclick={() => { all = true; }}>Show all {decided.length}</button>{/if}
    {/if}
    {#if failure}<p class="none" role="alert">{failure}</p>{/if}
  </div>
  {#if pending.length}<footer><kbd>J</kbd><kbd>K</kbd> move {#if admin}<kbd>A</kbd> approve <kbd>D</kbd> deny{/if} <kbd>Esc</kbd> close</footer>{/if}
</section>
<style>
  .panel { position: absolute; z-index: 20; top: 46px; right: 16px; width: min(460px, calc(100% - 32px)); max-height: min(560px, calc(100% - 64px)); display: flex; flex-direction: column; background: var(--raised); border: 1px solid var(--line); border-radius: 10px; box-shadow: 0 12px 40px var(--shadow-md); }
  header { flex: none; display: flex; align-items: center; justify-content: space-between; padding: 12px 14px 6px; }
  header strong { font-size: 16px; } header span, h2 span { margin-left: 8px; color: var(--muted); font-size: 13px; font-weight: 400; }
  button { border: 0; background: none; color: var(--muted); font: inherit; cursor: pointer; }
  /* A click away or Escape closes it on a desktop, as with Jobs; a phone's sheet keeps its button. */
  header button { display: none; font-size: 24px; line-height: 24px; }
  button:hover { color: var(--text); } :focus-visible { outline: 2px solid var(--focus); outline-offset: 2px; }
  .list { min-height: 0; overflow-y: auto; } .none { margin: 8px 14px 12px; color: var(--muted); font-size: 13px; }
  h2 { margin: 0; padding: 16px 16px 4px; font-size: 16px; }
  footer { flex: none; padding: 10px 16px; border-top: 1px solid var(--line); color: var(--muted); font-size: 12.5px; }
  kbd { margin-left: 8px; font: 11px var(--mono); } kbd:first-child { margin-left: 0; }
  .group { display: flex; align-items: center; flex-wrap: wrap; gap: 8px; padding: 14px 16px 4px; font-size: 13px; }
  .group strong { color: oklch(var(--id-name) var(--hue)); overflow-wrap: anywhere; } .group span { color: var(--muted); white-space: nowrap; }
  .group button { margin-left: auto; white-space: nowrap; } .more { padding: 10px 16px; font-size: 13px; }
  .deny { display: flex; gap: 8px; padding: 8px 16px; } .deny input { min-width: 0; flex: 1; background: var(--bg); color: var(--text); border: 1px solid var(--line); padding: 6px; }
  .handle { display: none; }
  @media(max-width: 640px) { header { padding: 6px 16px 8px; } header button { display: block; } .none { margin: 8px 16px 16px; font-size: 14px; } .handle { display: grid; place-items: center; height: 24px; touch-action: none; } .handle span { width: 36px; height: 4px; border-radius: 4px; background: var(--muted); } .panel { position: fixed; top: auto; right: 0; bottom: 0; width: 100%; max-height: 85dvh; border-radius: 14px 14px 0 0; } footer { display: none; } }
</style>
