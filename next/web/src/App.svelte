<script lang="ts">
  import { RoomStore } from "./lib/room.svelte.ts";
  import { APIError, call } from "./lib/client.ts";
  import type { Conversation, Principal, RoomSummary } from "./lib/contract.gen.ts";
  let rooms: RoomSummary[] = $state([]);
  let roomsError = $state("");
  void call("room.list", {}).then(result => { rooms = result.rooms ?? []; }).catch((error: unknown) => {
    roomsError = error instanceof APIError && error.detail.code === "forbidden"
      ? "Your sign-in ended. Run dyna web to sign in again." : "Rooms are not available.";
  });
  let path = $state(window.location.pathname);
  window.addEventListener("popstate", () => { path = window.location.pathname; });
  const segment = (pattern: RegExp) => { const match = pattern.exec(path)?.[1]; return match && decodeURIComponent(match); };
  let room = $derived(segment(/^\/r\/([^/]+)/));
  let session = $derived(segment(/^\/r\/[^/]+\/s\/([^/]+)/));
  let store: RoomStore | undefined = $state();
  let unavailable = $state(false);
  $effect(() => {
    if (!room) return;
    const current = new RoomStore(room);
    store = current;
    unavailable = false;
    void current.open().catch(() => { if (store === current) unavailable = true; });
    return () => current.close();
  });
  function navigate(event: MouseEvent & { currentTarget: HTMLAnchorElement }) {
    if (event.button || event.metaKey || event.ctrlKey || event.shiftKey) return;
    event.preventDefault();
    history.pushState(null, "", event.currentTarget.href);
    path = window.location.pathname;
  }
  let home: Conversation = $derived(session ? { kind: "dm", session } : { kind: "feed" });
  let posts = $derived(store?.posts(home) ?? []);
  let pending = $derived([...(store?.pendingPosts ?? [])].filter(post => post.home.session === session));
  const author = (from: Principal) => from.kind === "owner" ? "You" : from.kind === "engine" ? "Dyna"
    : store?.sessions.get(from.session ?? "")?.name ?? "Agent";
  const sameAuthor = (a: Principal, b: Principal | undefined) => a.kind === b?.kind && a.session === b.session;
  // Drafts and send errors belong to one conversation, keyed by room and session.
  let drafts: Record<string, string> = $state({});
  let failed: string | undefined = $state();
  let key = $derived(`${room}/${session ?? ""}`);
  function send(event: KeyboardEvent) {
    if (event.key !== "Enter" || event.shiftKey || event.isComposing || !store || !room) return;
    event.preventDefault();
    const sent = key;
    const text = drafts[sent] ?? "";
    if (!text.trim()) return;
    drafts[sent] = "";
    failed = undefined;
    store.post({ room, text, home: session ? home : undefined }).catch(() => { drafts[sent] = text; failed = sent; });
  }
  let rail: HTMLElement;
  let feed: HTMLElement;
  let composer: HTMLElement | undefined = $state();
  let members: HTMLElement | undefined = $state();
  // F6 (mod+F6 in a browser) moves focus between areas; Shift goes back.
  function cycle(event: KeyboardEvent) {
    if (event.key !== "F6") return;
    event.preventDefault();
    const areas = [rail, feed, composer, members].filter(area => area !== undefined);
    const index = areas.findIndex(area => area.contains(document.activeElement));
    areas[(index + (event.shiftKey ? areas.length - 1 : 1)) % areas.length].focus();
  }
  let roomName = $derived(rooms.find(item => item.id === room)?.name ?? "Room");
  let title = $derived(!room ? "Welcome" : session ? store?.sessions.get(session)?.name ?? "Session" : roomName);
</script>

<svelte:window onkeydown={cycle} />
<div class="shell" class:open={room}>
  <aside aria-label="Rooms" tabindex="-1" bind:this={rail}>
    <header><strong>Dyna</strong></header>
    <div class="section-title">Rooms</div>
    {#if rooms.length}
      <nav>{#each rooms as item (item.id)}
        <a href={"/r/" + encodeURIComponent(item.id)} onclick={navigate} aria-current={room === item.id ? "page" : undefined}>{item.name}</a>
      {/each}</nav>
    {:else}<p class="hint">{roomsError || "Your rooms will appear here."}</p>{/if}
  </aside>
  <main>
    <header><span>{title}</span></header>
    <section class="feed" aria-label="Feed" tabindex="-1" bind:this={feed}>
      {#each posts as post, index (post.seq)}
        {#if !sameAuthor(post.from, posts[index - 1]?.from)}<p class="author">{author(post.from)}</p>{/if}
        <p class="post">{post.body.text}</p>
      {/each}
      {#each pending as post}<p class="post pending">{post.text}</p>{/each}
      {#if !posts.length && !pending.length}
        <p class="empty">{unavailable ? "This room is not available." : !room ? "Select a room." : session ? "No messages yet." : "No posts yet."}</p>
      {/if}
    </section>
    {#if room && !unavailable}
      <textarea class="composer" rows="1" aria-label="Message" bind:this={composer} bind:value={drafts[key]} onkeydown={send}
        placeholder={"Message " + title}></textarea>
      {#if failed === key}<p class="hint" role="alert">Your message was not sent. Press Enter to try again.</p>{/if}
    {/if}
  </main>
  {#if room}
    <aside class="members" aria-label="Sessions" tabindex="-1" bind:this={members}>
      <header class="label">Sessions</header>
      {#each [...(store?.sessions.values() ?? [])] as item (item.session)}<div class="member">{item.name}</div>
      {:else}<p class="hint">No sessions yet.</p>{/each}
    </aside>
  {/if}
</div>
