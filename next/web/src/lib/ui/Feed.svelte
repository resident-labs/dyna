<script lang="ts">
  import { onDestroy, tick, type Snippet } from "svelte";
  import { SvelteSet } from "svelte/reactivity";
  import { APIError } from "../client.ts";
  import VirtualList from "./VirtualList.svelte";
  import PostRow from "./PostRow.svelte";
  import FailedTurnRow from "./FailedTurnRow.svelte";
  import WorkRow from "./WorkRow.svelte";
  import WorkingDot from "./WorkingDot.svelte";
  import { feedRows, type Row } from "../feed.ts";
  import type { RoomStore } from "../room.svelte.ts";
  import type { Conversation, PostRequest, Principal } from "../contract.gen.ts";

  let { store, home, intro, element = $bindable() }: {
    store: RoomStore; home: Conversation; intro: Snippet; element?: HTMLDivElement;
  } = $props();

  // The view is keyed by its conversation, so its window lives as long as the view.
  // svelte-ignore state_referenced_locally
  const view = store.window(home);
  onDestroy(() => store.leave(view));
  type Item = Row | { kind: "working"; key: string; session: string } | { kind: "intro"; key: string };
  // A pending post's mentions are names; its row shows them as the sessions the engine will resolve.
  const mentioned = (names: string[]) => names.flatMap(name => {
    const session = [...store.sessions.values()].find(open => open.name === name)?.session;
    return session ? [{ kind: "session" as const, session }] : [];
  });
  let pending = $derived([...view.pendingPosts.values()].map(({ id, request, at, failed }) => ({
    id, at, failed, text: request.text, attachments: request.attachments ?? [], to: mentioned(request.mentions ?? []),
  })));
  // The intro opens the conversation once the window reaches its first event; it also stands in for an empty feed.
  // A DM ends with one muted line while its session runs a turn, only when the window shows the present.
  let items: Item[] = $derived([
    ...view.ready && !view.hasOlder ? [{ kind: "intro" as const, key: "intro" }] : [],
    ...feedRows(view.ordered, home, pending),
    ...(home.session && view.atPresent && store.turns.has(home.session) ? [{ kind: "working" as const, key: "working", session: home.session }] : []),
  ]);
  // Opened work rows stay open while the list recycles their elements.
  const opened = new SvelteSet<string>();
  const name = (session: string) => store.names.get(session) ?? "A closed session";
  const author = (from: Principal) => from.kind === "owner" ? "You" : from.session ? name(from.session) : "Dyna";
  // The root session is the open session with no parent.
  const root = (session?: string) => { const open = session ? store.sessions.get(session) : undefined; return open !== undefined && !open.parent; };
  const toggle = (key: string) => { if (!opened.delete(key)) opened.add(key); };
  let list: VirtualList<Item> | undefined = $state();
  // The owner's own post and a jump always land on the newest row, even from inside the present window.
  // It never rejects: a failed send stays in the feed as a row with Retry.
  export async function send(request: PostRequest): Promise<void> {
    const sending = view.post(request);
    void list?.toEnd();
    await sending;
    await list?.toEnd();
  }
  // Rows that do not fill the view cannot be scrolled up, so older pages load until they do.
  // fill() stops before it would leave the present; rows that are still short then get an
  // explicit control, so older history stays reachable.
  const filled = async () => { await tick(); return list?.full() ?? false; };
  let short = $state(false);
  async function grow() {
    await view.fill(filled);
    short = !(await filled());
  }
  $effect(() => { if (view.ready) void grow(); });
  async function older() {
    await view.loadOlder();
    await grow();
  }
  async function jump() {
    await view.jumpToPresent();
    await list?.toEnd();
    await grow();
  }
</script>

{#if short && view.hasOlder}
  <button type="button" class="jump" onclick={older}>Show older posts</button>
{/if}
{#if items.length}
  <VirtualList bind:this={list} {items} key={item => item.key} label="Feed" follow={view.atPresent} bind:element
    onstart={() => void view.loadOlder()} onend={() => void view.loadNewer()}>
    {#snippet row(item: Item)}
      {#if item.kind === "post"}<PostRow row={item} author={author(item.from)} to={home.kind === "dm" ? undefined : item.to.map(author).join(", ")} root={root(item.from.session)}
        onretry={id => void view.retry(id)} ondiscard={id => view.discard(id)} />
      {:else if item.kind === "failed"}<FailedTurnRow row={item} author={name(item.session)} root={root(item.session)} />
      {:else if item.kind === "work"}<WorkRow row={item} {name} open={opened.has(item.key)} ontoggle={() => toggle(item.key)} />
      {:else if item.kind === "working"}<p class="working"><WorkingDot />{name(item.session)} is working</p>
      {:else}{@render intro()}{/if}
    {/snippet}
  </VirtualList>
{/if}
{#if view.error !== undefined}
  <p class="hint" role="alert">{view.error instanceof APIError && view.error.detail.code === "forbidden"
    ? "Your sign-in ended. Run dyna web to sign in again." : view.ready ? "Some posts did not load. Scroll or jump to try again." : "Posts did not load."}
    {#if !view.ready}<button type="button" class="jump" onclick={() => void view.open()}>Try again</button>{/if}</p>
{/if}
{#if !view.atPresent}
  <button type="button" class="jump" onclick={jump}>New posts below. Jump to present</button>
{/if}

<style>
  .working { margin: 0; padding: 6px 16px; display: flex; align-items: center; gap: 8px; color: var(--muted); font-size: 12px; }
  .hint { margin: 0; padding: 6px 16px; color: var(--muted); font-size: 12px; }
  .jump { all: unset; cursor: pointer; padding: 6px 16px; color: var(--muted); font-size: 12px; }
  .jump:hover { color: var(--text); }
  .jump:focus-visible { outline: 2px solid var(--focus); outline-offset: -2px; }
</style>
