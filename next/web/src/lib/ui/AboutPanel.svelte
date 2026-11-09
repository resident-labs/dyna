<script lang="ts">
  import AccountMenu from "./AccountMenu.svelte";
  import BoxFacts from "./BoxFacts.svelte";
  import FoldRow from "./FoldRow.svelte";
  import HooksList from "./HooksList.svelte";
  import Icon, { type IconName } from "./Icon.svelte";
  import Modal from "./Modal.svelte";
  import ProfileMenu from "./ProfileMenu.svelte";
  import StatusPill from "./StatusPill.svelte";
  import WrapText from "./WrapText.svelte";
  import { fromNow, fullTime } from "../feed.ts";
  import { goalTree, openLeaves } from "../goals.ts";
  import { call } from "../client.ts";
  import { confirmClose } from "../session-confirms.ts";
  import { descendants, holdRows } from "../session-tree.ts";
  import { subscriptionSummary } from "../subscriptions.ts";
  import type { RoomStore } from "../room.svelte.ts";
  import type { Responsibility, ResponsibilityKind, SessionAccountResponse, SubscriptionInfo } from "../contract.gen.ts";
  // What a DM's session is, in the order the owner asks: who it is, the box it runs in, then what it holds. Goals fold to
  // a line or two, and each kind of hold is one line that opens its whole list in a modal, so the first two never leave
  // the screen and nothing pushes the panel sideways. A row without a value does not show, and a session holding nothing
  // simply ends after the box. The header and the composer already show the turn, its stop and the jobs.
  // `shown` is false while the panel is closed, and stops the box's figures.
  let { store, room, session, switchable, shown, navigate, onclosed }: {
    store: RoomStore; room: string; session: string; switchable: boolean; shown: boolean; navigate: (event: MouseEvent & { currentTarget: HTMLAnchorElement }) => void; onclosed: () => void;
  } = $props();
  let open = $derived(store.sessions.get(session));
  // A session holds one goal; its todo list nests one level of subtasks.
  let goals = $derived(store.goals.get(session) ?? []);
  let goal = $derived(goals[0]);
  let parent = $derived(open?.parent && store.names.get(open.parent));
  // The relative time stays roughly true while the panel stays open.
  let now = $state(Date.now());
  $effect(() => { const timer = setInterval(() => { now = Date.now(); }, 30_000); return () => clearInterval(timer); });
  const ago = (at: string) => fromNow(at, Math.max(now, Date.parse(at)));
  // Only the owner gets an answer, so anyone else sees no Account row. A profile change can drop the choice.
  let account: SessionAccountResponse | undefined = $state();
  function loadAccount(): void {
    const asked = session;
    void call("session.account", { session: asked }).then(answer => { if (asked === session) account = answer; }, () => { if (asked === session) account = undefined; });
  }
  $effect(() => { void open?.profile; loadAccount(); });
  // Root's panel is the room's, so it shows what the room is subscribed to. A list that cannot load shows nothing.
  let isRoot = $derived(open !== undefined && !open.parent);
  let subscriptions: SubscriptionInfo[] = $state([]);
  $effect(() => {
    if (!isRoot) { subscriptions = []; return; }
    const asked = room;
    void call("subscription.list", { room: asked }).then(answer => { if (asked === room) subscriptions = answer.subscriptions ?? []; }, () => { subscriptions = []; });
  });
  // The engine's list of what this session still holds. Anything that can change it reads it again; one that cannot load shows nothing.
  let holds: Responsibility[] = $state([]);
  function loadHolds(): void {
    const asked = session;
    void call("session.responsibilities", { session: asked }).then(answer => { if (asked === session) holds = answer.items ?? []; }, () => { if (asked === session) holds = []; });
  }
  $effect(() => { void store.boxes.get(session); void open?.lastTurnAt; void goals.length; void store.running(session); loadHolds(); });
  const childOf = (name: string) => [...store.sessions.values()].find(item => item.name === name && item.parent === session);
  let held = $derived(holdRows(holds, name => {
    const child = childOf(name);
    return child && `/r/${encodeURIComponent(room)}/s/${encodeURIComponent(child.session)}`;
  }));
  let hooks = $derived(subscriptionSummary(subscriptions));
  const holdIcons: Partial<Record<ResponsibilityKind, IconName>> = { pr: "pr", issue: "issue", child: "people", terminal: "terminal", workflow: "list", question: "chat" };
  // Each hold, and the room's hooks, is one line here; the full list opens in a modal. A list that empties closes it.
  let showing: ResponsibilityKind | "hooks" | undefined = $state();
  $effect(() => { void session; showing = undefined; });
  let shownHold = $derived(held.find(row => row.kind === showing && row.opens));
  // A link in a modal goes where it says and takes the modal away with it.
  const follow = (event: MouseEvent & { currentTarget: HTMLAnchorElement }) => { showing = undefined; navigate(event); };
  // Closing ends this session and every session under it; the confirm says so, and what it hands over.
  async function askClose(event: MouseEvent & { currentTarget: HTMLButtonElement }): Promise<void> {
    const closing = session, done = onclosed;
    const under = descendants([...store.sessions.values()], closing);
    if (await confirmClose(closing, open?.name ?? "this session", under, id => store.names.get(id), event.currentTarget)) done();
  }
</script>

<div class="about">
  <div class="facts-scroll">
  <dl class="facts">
    {#if parent && open?.parent}<dt>Parent</dt><dd><a href={`/r/${encodeURIComponent(room)}/s/${encodeURIComponent(open.parent)}`} onclick={navigate}>{parent}</a></dd>{/if}
    {#if open?.profile}<dt>Profile</dt><dd><ProfileMenu {session} profile={open.profile} {switchable} /></dd>{/if}
    {#if account}<dt>Account</dt><dd><AccountMenu {session} {account} {switchable} onchanged={loadAccount} /></dd>{/if}
  </dl>
  {#key session}<BoxFacts {store} {session} {shown} />{/key}
  {#if goal}
    {@const items = goal.items ?? []}
    {@const left = openLeaves(items)}
    <section>
      <h3>Goal</h3>
      <FoldRow more={Boolean(goal.doneWhen || items.length)}>
        {#snippet head()}<WrapText text={goal.text} />{/snippet}
        {#snippet aside()}{#if left}{left} open{/if}{/snippet}
        {#if goal.doneWhen}<p class="when">Done when: <WrapText text={goal.doneWhen} /></p>{/if}
        {#if items.length}<ul class="todo">{#each goalTree(items) as item (item.number)}<li class:quiet={item.state !== "open"} class:dropped={item.state === "dropped"}><span class="mark" aria-label={item.state}>{item.state === "done" ? "✓" : item.state === "dropped" ? "–" : "○"}</span><WrapText text={item.text} />{#if item.subtasks.length}<ul>{#each item.subtasks as sub (sub.number)}<li class:quiet={sub.state !== "open"} class:dropped={sub.state === "dropped"}><span class="mark" aria-label={sub.state}>{sub.state === "done" ? "✓" : sub.state === "dropped" ? "–" : "○"}</span><WrapText text={sub.text} /></li>{/each}</ul>{/if}</li>{/each}</ul>{/if}
      </FoldRow>
    </section>
  {/if}
  {#if held.length || hooks.count}
    <section>
      <h3>Holds</h3>
      {#snippet line(icon: IconName, label: string, count: number, blocked: boolean, health = "", failing = false)}
        <Icon name={icon} size={14} /><span class="hold-label">{label}<span class="hold-count">{count}</span></span>
        {#if blocked}<StatusPill tone="blocked" word label="Blocked" />{:else if failing}<StatusPill tone="failed" word label={health} />{:else if health}<span class="hold-health">{health}</span>{/if}
      {/snippet}
      <div class="holds">
        {#each held as row (row.kind)}
          {#if row.opens}<button type="button" class="hold" aria-haspopup="dialog" onclick={() => { showing = row.kind; }}>{@render line(holdIcons[row.kind] ?? "list", row.label, row.count, row.blocked)}<span class="go"><Icon name="forwardChevron" size={12} /></span></button>
          {:else}<div class="hold">{@render line(holdIcons[row.kind] ?? "list", row.label, row.count, row.blocked)}</div>{/if}
        {/each}
        {#if hooks.count}<button type="button" class="hold" aria-haspopup="dialog" onclick={() => { showing = "hooks"; }}>{@render line("plug", "Hooks", hooks.count, false, hooks.health, hooks.failing)}<span class="go"><Icon name="forwardChevron" size={12} /></span></button>{/if}
      </div>
    </section>
  {/if}
  </div>
  <div class="end">
  {#if open?.startedAt || open?.parent}
    <div class="endnote">
      {#if open?.startedAt}<p class="times"><time datetime={open.startedAt} title={fullTime(open.startedAt)}>Started {ago(open.startedAt)}</time>{#if open.lastTurnAt}<time datetime={open.lastTurnAt} title={fullTime(open.lastTurnAt)}>Last turn {ago(open.lastTurnAt)}</time>{/if}</p>{/if}
      {#if open?.parent}<button type="button" class="close-link" aria-haspopup="dialog" onclick={event => void askClose(event)}>Close session</button>{/if}
    </div>
  {/if}
  </div>
</div>

{#if shownHold}
  <Modal title={shownHold.label} count={shownHold.count} onclose={() => { showing = undefined; }}>
    <ul class="hold-items">
      {#each shownHold.items as item, index (index)}
        {@const child = shownHold.kind === "child" ? childOf(item.label) : undefined}
        <li>
          <p class="item-head"><Icon name={holdIcons[shownHold.kind] ?? "list"} size={14} />
            {#if item.href && item.internal}<a href={item.href} onclick={follow}>{item.label}</a>
            {:else if item.href}<a href={item.href} target="_blank" rel="noreferrer">{item.label}</a>
            {:else}<span>{item.label}</span>{/if}
            {#if child?.profile}<span class="item-meta">{child.profile}</span>{/if}
            {#if item.blocked}<StatusPill tone="blocked" word label="Blocked" />{/if}</p>
          {#if child?.goal}<p class="item-text"><WrapText text={child.goal} /></p>{/if}
        </li>
      {/each}
    </ul>
  </Modal>
{:else if showing === "hooks" && hooks.count}
  <Modal title="Hooks" count={hooks.count} onclose={() => { showing = undefined; }}>
    <HooksList {subscriptions} name={id => store.names.get(id) ?? id} />
  </Modal>
{/if}

<style>
  .about { display: flex; flex-direction: column; flex: 1; min-width: 0; min-height: 0; font-size: 14px; }
  /* Never sideways: every row below wraps or clips its own text, and this keeps a stray long word from scrolling the panel. */
  .facts-scroll { display: flex; flex-direction: column; gap: 16px; flex: 1; min-height: 0; overflow-x: hidden; overflow-y: auto; padding: 16px; }
  /* The times and the close stay in one place above the machines, however much the session holds. */
  .end { display: grid; gap: 8px; padding: 8px 16px 12px; }
  /* An empty list of facts, as for a session still loading, takes no gap. */
  .about :global(.facts:empty) { display: none; }
  h3 { display: flex; align-items: baseline; gap: 6px; margin: 0 0 4px; font-size: 13px; font-weight: 600; line-height: 20px; }
  section { min-width: 0; }
  p { margin: 0; }
  .when, .times { color: var(--muted); font-size: 12.5px; line-height: 1.45; }
  .when { overflow-wrap: break-word; }
  /* Each time stays whole, so a narrow panel stacks them instead of leaving "ago" alone on a line. */
  .times { display: flex; flex-wrap: wrap; gap: 0 12px; }
  ul { display: grid; gap: 4px; margin: 0; padding: 0 0 0 16px; line-height: 1.45; overflow-wrap: break-word; }
  .quiet { color: var(--muted); }
  .dropped { text-decoration: line-through; }
  .todo, .todo ul { list-style: none; padding-left: 0; }
  .todo ul { margin-top: 4px; padding-left: 16px; }
  .todo li { display: grid; grid-template-columns: 14px minmax(0, 1fr); column-gap: 6px; }
  .todo li > ul { grid-column: 2; }
  .mark { color: var(--muted); text-decoration: none; display: inline-block; }
  a, button { padding: 0; border: 0; border-radius: 4px; background: none; color: var(--focus); font: inherit; text-decoration: none; cursor: pointer; }
  .endnote { display: flex; align-items: flex-end; justify-content: space-between; gap: 12px; }
  .close-link { flex: none; color: var(--muted); font-size: 12.5px; line-height: 1.45; }
  .close-link:hover { color: var(--danger); text-decoration: none; }
  a:hover, .close-link:hover { text-decoration: underline; }
  a:focus-visible, button:focus-visible { outline: 2px solid var(--focus); outline-offset: 2px; }
  /* A hold is one line: its icon, its label with the count beside it, and a state only when it needs the owner. The wash
     reaches past the text, and the margin takes it back, so the label lines up with the panel's text, as FoldRow's does. */
  .holds { display: grid; }
  .hold { display: grid; grid-template-columns: 14px minmax(0, 1fr) auto 12px; align-items: center; column-gap: 8px; box-sizing: border-box; width: calc(100% + 16px); min-height: 28px;
    margin: 0 -8px; padding: 4px 8px; border-radius: 6px; color: var(--text); line-height: 20px; text-align: left; }
  div.hold { grid-template-columns: 14px minmax(0, 1fr) auto; }
  .hold :global(svg) { color: var(--muted); }
  .hold-label { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .hold-count, .hold-health { color: var(--muted); font-size: 12.5px; font-variant-numeric: tabular-nums; }
  .hold-count { margin-left: 6px; }
  .hold-health { white-space: nowrap; }
  .go { grid-column: 4; display: grid; }
  button.hold:focus-visible { outline-offset: -2px; }
  @media (hover: hover) { button.hold:hover { background: var(--hover); } }
  @media (hover: none) { .hold { min-height: 40px; } }
  /* The modal's list: one item to a row, its whole name wrapped, its link, and its session's goal under a child. */
  .hold-items { display: grid; gap: 0; padding: 0; list-style: none; }
  .hold-items li { display: grid; gap: 2px; min-width: 0; padding: 8px 0; border-top: 1px solid var(--line); }
  .hold-items li:first-child { padding-top: 0; border-top: 0; }
  .hold-items li:last-child { padding-bottom: 0; }
  .item-head { display: flex; flex-wrap: wrap; align-items: center; gap: 2px 8px; min-width: 0; line-height: 20px; overflow-wrap: anywhere; }
  .item-head :global(svg) { flex: none; color: var(--muted); }
  .item-head a:hover { text-decoration: underline; }
  .item-meta { color: var(--muted); font-size: 12.5px; }
  .item-text { padding-left: 22px; color: var(--muted); font-size: 12.5px; overflow-wrap: anywhere; }
  @media (hover: none) { .hold-items li { padding: 12px 0; } }
</style>
