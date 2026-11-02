<script lang="ts">
  import AccountMenu from "./AccountMenu.svelte";
  import BoxFacts from "./BoxFacts.svelte";
  import FoldRow from "./FoldRow.svelte";
  import HooksRow from "./HooksRow.svelte";
  import ProfileMenu from "./ProfileMenu.svelte";
  import StatusPill from "./StatusPill.svelte";
  import WrapText from "./WrapText.svelte";
  import { fromNow, fullTime } from "../feed.ts";
  import { call } from "../client.ts";
  import { closeNote, descendants, handNote, holdRows } from "../session-tree.ts";
  import type { RoomStore } from "../room.svelte.ts";
  import type { Responsibility, SessionAccountResponse, SessionResponsibilitiesResponse, SubscriptionInfo } from "../contract.gen.ts";
  // What a DM's session is, in the order the owner asks: who it is, the box it runs in, then what it holds. Anything long
  // folds to a line or two, so the first two never leave the screen. A row without a value does not show, and a session
  // holding nothing simply ends after the box. The header and the composer already show the turn, its stop and the jobs.
  // `shown` is false while the panel is closed, and stops the box's figures.
  let { store, room, session, switchable, shown, navigate, onclosed }: {
    store: RoomStore; room: string; session: string; switchable: boolean; shown: boolean; navigate: (event: MouseEvent & { currentTarget: HTMLAnchorElement }) => void; onclosed: () => void;
  } = $props();
  let open = $derived(store.sessions.get(session));
  let goals = $derived(store.goals.get(session) ?? []);
  let parent = $derived(open?.parent && store.names.get(open.parent));
  // The relative time stays roughly true while the panel stays open.
  let now = $state(Date.now());
  $effect(() => { const timer = setInterval(() => { now = Date.now(); }, 30_000); return () => clearInterval(timer); });
  const ago = (at: string) => fromNow(at, Math.max(now, Date.parse(at)));
  // Closing ends this session and every session under it. The engine archives their work first, deepest child first.
  let archiveNote = $derived(closeNote(descendants([...store.sessions.values()], session).map(id => store.names.get(id) ?? id)));
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
  let plan: SessionResponsibilitiesResponse["close"] | undefined = $state();
  function loadHolds(): Promise<void> {
    const asked = session;
    return call("session.responsibilities", { session: asked }).then(answer => {
      if (asked === session) { holds = answer.items ?? []; plan = answer.close; }
    }, () => { if (asked === session) { holds = []; plan = undefined; } });
  }
  $effect(() => { void store.boxes.get(session); void open?.lastTurnAt; void goals.length; void store.running(session); void loadHolds(); });
  let held = $derived(holdRows(holds));
  let handed = $derived(plan ? handNote(plan.handed ?? [], store.names.get(plan.to) ?? "the parent") : "");
  let confirming = $state(false);
  let closing = $state(false);
  let closeFailed = $state(false);
  $effect(() => { void session; confirming = false; });
  // The confirm reads the list again, so it names what the close will really hand over.
  function confirmClose(): void {
    confirming = true;
    void loadHolds();
  }
  function close(): void {
    closing = true;
    closeFailed = false;
    void call("session.close", { session }).then(onclosed, () => { closing = false; closeFailed = true; });
  }
</script>

<div class="about">
  <dl class="facts">
    {#if parent && open?.parent}<dt>Parent</dt><dd><a href={`/r/${encodeURIComponent(room)}/s/${encodeURIComponent(open.parent)}`} onclick={navigate}>{parent}</a></dd>{/if}
    {#if open?.profile}<dt>Profile</dt><dd><ProfileMenu {session} profile={open.profile} {switchable} /></dd>{/if}
    {#if account}<dt>Account</dt><dd><AccountMenu {session} {account} {switchable} onchanged={loadAccount} /></dd>{/if}
  </dl>
  {#key session}<BoxFacts {store} {session} {shown} />{/key}
  {#if goals.length || open?.goal}
    <section>
      <h3>{goals.length > 1 ? "Goals" : "Goal"}{#if goals.length > 1}<span>{goals.length}</span>{/if}</h3>
      {#each goals as goal (goal.id)}
        {@const items = goal.items?.filter(item => item.state === "open").length}
        <FoldRow more={Boolean(goal.doneWhen || goal.items?.length)}>
          {#snippet head()}<WrapText text={goal.text} />{/snippet}
          {#snippet aside()}{#if items}{items} open{/if}{/snippet}
          {#if goal.doneWhen}<p class="when">Done when: <WrapText text={goal.doneWhen} /></p>{/if}
          {#if goal.items?.length}<ul>{#each goal.items as item (item.number)}<li class:quiet={item.state !== "open"} class:dropped={item.state === "dropped"}>{item.state === "done" ? "Done · " : ""}<WrapText text={item.text} /></li>{/each}</ul>{/if}
        </FoldRow>
      {:else}
        {#if open?.goal}<FoldRow>{#snippet head()}<WrapText text={open?.goal ?? ""} />{/snippet}</FoldRow>{/if}
      {/each}
    </section>
  {/if}
  {#if held.length}
    <section>
      <h3>Holds</h3>
      {#each held as row (row.kind)}
        <FoldRow>
          {#snippet head()}<WrapText text={row.text} />{/snippet}
          {#snippet aside()}{#if row.blocked}<StatusPill tone="blocked" word label="Blocked" />{/if}{/snippet}
        </FoldRow>
      {/each}
    </section>
  {/if}
  {#if subscriptions.length}<section><HooksRow {subscriptions} name={id => store.names.get(id) ?? id} /></section>{/if}
  {#if open?.startedAt}<p class="times"><time datetime={open.startedAt} title={fullTime(open.startedAt)}>Started {ago(open.startedAt)}</time>{#if open.lastTurnAt}<time datetime={open.lastTurnAt} title={fullTime(open.lastTurnAt)}>Last turn {ago(open.lastTurnAt)}</time>{/if}</p>{/if}
  {#if open?.parent}
    <div class="closebox">
      {#if confirming}
        {#if handed}<p class="handed">{handed}</p>{/if}
        <p class="note">{archiveNote}</p>
        <div class="confirm">
          <button type="button" class="close-button" disabled={closing} onclick={() => { confirming = false; }}>Cancel</button>
          <button type="button" class="close-button danger" disabled={closing} onclick={close}>Close session</button>
        </div>
      {:else}
        <button type="button" class="close-button" onclick={confirmClose}>Close session…</button>
        <p class="note">{archiveNote}</p>
      {/if}
      {#if closeFailed}<span class="failed">The session did not close. Try again.</span>{/if}
    </div>
  {/if}
</div>

<style>
  .about { display: flex; flex-direction: column; gap: 16px; flex: 1; min-height: 0; overflow-y: auto; padding: 16px; font-size: 14px; }
  /* An empty list of facts, as for a session still loading, takes no gap. */
  .about :global(.facts:empty) { display: none; }
  h3 { display: flex; align-items: baseline; gap: 6px; margin: 0 0 4px; font-size: 13px; font-weight: 600; line-height: 20px; }
  h3 span { color: var(--muted); font-size: 12.5px; font-weight: 400; font-variant-numeric: tabular-nums; }
  section { min-width: 0; }
  p { margin: 0; }
  .when, .note, .handed, .times, .failed { color: var(--muted); font-size: 12.5px; line-height: 1.45; }
  .handed { color: var(--text); }
  .when { overflow-wrap: break-word; }
  /* Each time stays whole, so a narrow panel stacks them instead of leaving "ago" alone on a line. */
  .times { display: flex; flex-wrap: wrap; gap: 0 12px; }
  ul { display: grid; gap: 4px; margin: 0; padding: 0 0 0 16px; line-height: 1.45; overflow-wrap: break-word; }
  .quiet { color: var(--muted); }
  .dropped { text-decoration: line-through; }
  a, button { padding: 0; border: 0; border-radius: 4px; background: none; color: var(--focus); font: inherit; text-decoration: none; cursor: pointer; }
  .failed { display: block; }
  .closebox { display: grid; gap: 8px; justify-items: start; padding-top: 16px; border-top: 1px solid var(--line); }
  .close-button { height: 28px; padding: 0 12px; border: 1px solid var(--line); border-radius: 6px; color: var(--text); font-size: 13px; font-weight: 500; }
  .close-button:hover { background: var(--hover); text-decoration: none; }
  .close-button:disabled { background: none; color: var(--muted); cursor: default; text-decoration: none; }
  .close-button.danger { color: var(--danger); }
  .confirm { display: flex; gap: 8px; }
  a:hover, button:hover { text-decoration: underline; }
  a:focus-visible, button:focus-visible { outline: 2px solid var(--focus); outline-offset: 2px; }
</style>
