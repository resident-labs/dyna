<script lang="ts">
  import { tick } from "svelte";
  import { SvelteMap, SvelteSet } from "svelte/reactivity";
  import { APIError, call } from "../client.ts";
  import type { Attention } from "../attention.svelte.ts";
  import type { AccountUsage, Card, HostSummary, Principal, SessionListResponse } from "../contract.gen.ts";
  import { boxReason, fromNow, requestLine, turnReason } from "../feed.ts";
  import { hostLabel, hostMeters, watchHosts } from "../hosts.ts";
  import { busiestWindow } from "../usage.ts";
  import { distinctInitials, hue, initials as letters } from "../identity.ts";
  import Avatar from "./Avatar.svelte";
  import HostCard from "./HostCard.svelte";
  import Icon from "./Icon.svelte";
  import StatusPill from "./StatusPill.svelte";
  import UsageCard from "./UsageCard.svelte";
  type Navigate = (event: MouseEvent & { currentTarget: HTMLAnchorElement }) => void;
  // onrequests opens the Requests panel once the link has moved to that room. loaded is false until room.list answers.
  // initials are the rooms' distinct initials, as the rail shows them.
  let { attention, initials, loaded, navigate, onrequests }: {
    attention: Attention; initials: Map<string, string>; loaded: boolean; navigate: Navigate; onrequests: () => void;
  } = $props();
  // Each room's sessions load once while Home is open, for Activity's counts and names. Requests load per room, and
  // again only when a room gains a request or requester that no answer named yet. A failed load forgets its IDs and
  // offers Try again.
  const requests = new SvelteMap<string, { title: string; body: string; by: Principal; target: Card["target"]; expires: string }>();
  const sessions = new SvelteMap<string, { name: string; root: boolean; room: string }>();
  // Requester IDs a session.list answered for; one it did not name is a closed session.
  const answered = new SvelteSet<string>();
  // Rooms whose session.list answered.
  const listed = new SvelteSet<string>();
  const asked = new Set<string>();
  let failed = $state(false);
  let attempt = $state(0);
  function load<T>(ids: Iterable<string>, known: { has(id: string): boolean }, list: () => Promise<T>, keep: (answer: T, fresh: string[]) => void) {
    const fresh = [...ids].filter(id => !known.has(id) && !asked.has(id));
    if (!fresh.length) return;
    for (const id of fresh) asked.add(id);
    void list().then(answer => keep(answer, fresh), () => { for (const id of fresh) asked.delete(id); failed = true; });
  }
  const keepSessions = (room: string) => (list: SessionListResponse, fresh: string[]) => {
    for (const item of list.sessions ?? []) sessions.set(item.id, { name: item.name, root: !item.parent, room });
    for (const id of fresh) answered.add(id);
    listed.add(room);
  };
  $effect(() => {
    void attempt;
    for (const room of attention.rooms.keys()) {
      load([room], listed, () => attention.listSessions(room), keepSessions(room));
      load(attention.waiting.get(room) ?? [], requests, () => call("op.list", { room }), list => {
        for (const op of list.operations ?? []) requests.set(op.id, { title: op.card.title, body: op.card.body?.split("\n")[0] ?? "", by: op.by, target: op.card.target, expires: op.expires });
      });
      // A requester or a failed session that started after the room's list loads it again.
      const by = [...attention.waiting.get(room) ?? []].flatMap(id => requests.get(id)?.by.session ?? []);
      const down = [...attention.broken.get(room)?.keys() ?? [], ...attention.unseen(room).map(({ id }) => id)];
      if (listed.has(room)) load([...by, ...down], sessions, () => attention.listSessions(room), keepSessions(room));
    }
  });
  const rows = <T,>(sets: Map<string, Set<string>>, known: SvelteMap<string, T>) => [...attention.rooms.values()].flatMap(room =>
    [...sets.get(room.id) ?? []].flatMap(id => { const value = known.get(id); return value === undefined ? [] : [{ id, room, value }]; }));
  // A request shows once its requester is named, as in the Requests panel. One decided here stays out while its event comes.
  let waiting = $derived(rows(attention.waiting, requests).filter(row => !decided.has(row.id)).flatMap(row => {
    const { by } = row.value;
    if (by.kind !== "session" || !by.session) return [{ ...row, who: by.kind === "owner" ? "You" : "Dyna", root: false }];
    const session = sessions.get(by.session);
    if (!session && !answered.has(by.session)) return [];
    return [{ ...row, who: session?.name ?? "A closed session", root: session?.root ?? false }];
  }));
  // Each session's initials, distinct within its room as the room's own page shows them.
  let sessionInitials = $derived(new Map([...Map.groupBy(sessions, ([, item]) => item.room).values()].flatMap(list => {
    const labels = distinctInitials(list.map(([, item]) => item.name));
    return list.map(([id, item]) => [id, labels.get(item.name) ?? letters(item.name)]);
  })));
  // More than 4 requests fold into one row per room, which opens that room's Requests.
  let groups = $derived(waiting.length <= 4 ? [] : [...attention.rooms.values()].flatMap(room => {
    const count = waiting.filter(row => row.room.id === room.id).length;
    return count ? [{ room, count }] : [];
  }));
  const count = (n: number, unit: string) => `${n} ${unit}${n === 1 ? "" : "s"}`;
  // Activity has one row per room, never one per session: rooms with requests or a failed session first, then working
  // rooms, each by name. A failed session comes first in its row, with its reason, as it needs the owner, so a red rail
  // mark always has its reason here.
  // A working room names its busiest agent, the one with the newest activity label. Quiet rooms fold into one line.
  let activity = $derived([...attention.rooms.values()].map(room => {
    const working = attention.busy(room.id), asks = attention.waiting.get(room.id)?.size ?? 0;
    const down = [...[...attention.broken.get(room.id) ?? []].map(([id, failure]) => ({ id, reason: boxReason(failure) })), ...attention.unseen(room.id).map(({ id, cause }) => ({ id, reason: turnReason(cause) }))];
    const idle = Math.max(0, (attention.sessions.get(room.id)?.size ?? 0) - new Set([...working, ...down.map(({ id }) => id)]).size);
    const failed = down.length > 1 ? `${down.length} sessions failed` : down.map(({ id, reason }) => `${sessions.get(id)?.name ?? "A session"} failed · ${reason}`)[0];
    const [busiest, label] = [...attention.activity].findLast(([id]) => working.includes(id)) ?? [];
    const name = busiest && (sessions.get(busiest)?.name ?? (room.latest?.session === busiest ? room.latest.name : undefined));
    const line = [asks && count(asks, "request"), working.length ? `${working.length} working` : idle ? `${idle} idle` : down.length ? "" : "No open session", name, label];
    return { room, working, failed, rank: asks || down.length ? 0 : working.length ? 1 : 2, line: line.filter(Boolean).join(" · ") };
  }).sort((a, b) => a.rank - b.rank || a.room.name.localeCompare(b.room.name)));
  let active = $derived(activity.filter(row => row.rank < 2));
  let quiet = $derived(activity.filter(row => row.rank === 2));
  let showQuiet = $state(false);
  // Approve and Deny decide in place. The engine's op.decided event takes the row out of waiting; the owner's own
  // decision stays as one muted line until Home closes, so the row does not just vanish under the pointer.
  const decided = new SvelteMap<string, string>();
  const deciding = new SvelteSet<string>();
  const failures = new SvelteMap<string, string>();
  // Focus that was on the decided row moves to the next request's Approve, or to the Requests heading when none is
  // left, so a keyboard owner keeps their place. It is read before the buttons disable, which drops focus.
  let requestsHeading: HTMLElement | undefined = $state();
  async function decide(id: string, title: string, approve: boolean, row: HTMLElement) {
    const held = row.contains(document.activeElement);
    const sibling = (next: Element | null) => next?.classList.contains("request") ? next : undefined;
    const neighbor = sibling(row.nextElementSibling) ?? sibling(row.previousElementSibling);
    deciding.add(id);
    failures.delete(id);
    const saved = await call("op.decide", { id, approve }).then(() => { decided.set(id, `${approve ? "Approved" : "Denied"} · ${title}`); return true; }, (error: unknown) => {
      failures.set(id, error instanceof APIError && error.detail.code === "conflict" ? "This request is no longer waiting." : "Your decision was not saved. Try again.");
      return false;
    });
    deciding.delete(id);
    if (!held) return;
    await tick();
    if (!saved) { row.querySelector<HTMLElement>(".approve")?.focus(); return; }
    (neighbor?.isConnected ? neighbor.querySelector<HTMLElement>(".approve") : requestsHeading)?.focus();
  }
  // A list shows while the store has items, as in v1's Home a failed load is never "all clear";
  // its count is the rows shown.
  const total = (sets: Map<string, { size: number }>) => [...sets.values()].reduce((sum, set) => sum + set.size, 0);
  let missing = $derived(waiting.length < total(attention.waiting)
    || [...attention.rooms.keys()].some(room => !listed.has(room)));
  const roomHref = (room: string) => "/r/" + encodeURIComponent(room);
  // Hosts load when Home opens and stay current while the page is visible (watchHosts). A failed call keeps the last
  // answer, and the next tick tries again. A control plane that reports no load shows no card, never an empty one.
  let hosts: HostSummary[] | undefined = $state();
  let hostsFailed = $state(false);
  let hostsAt = $state(0);
  // Each host answer or failure also moves the clock that request expiry counts from.
  let now = $state(Date.now());
  $effect(() => watchHosts(list => { hosts = list.filter(h => h.name !== "control-plane" || hostMeters(h).length); now = hostsAt = Date.now(); hostsFailed = false; },
    () => { now = Date.now(); hostsFailed = true; }));
  // The sessions panel's host foot links to /#machines, so Machines scrolls into view once it shows.
  let machines: HTMLElement | undefined = $state();
  $effect(() => { if (machines && location.hash === "#machines") machines.scrollIntoView({ block: "start" }); });
  // Model usage loads once when Home opens: the engine caches it for 5 min. Reset times count from that answer.
  let accounts: AccountUsage[] | undefined = $state();
  let accountsFailed = $state(false);
  let loadedAt = $state(0);
  $effect(() => {
    void call("accounts.quota", {}).then(list => { accounts = list.accounts ?? []; loadedAt = Date.now(); }, () => { accountsFailed = true; });
  });
  // One sentence says what needs the owner, counted from the same store as the header's Requests button. Under it, a
  // muted line sums rooms, machines and the busiest usage window; a part with nothing to say is left out.
  let asks = $derived(total(attention.waiting));
  let workingCounts = $derived([...attention.rooms.keys()].map(room => attention.busy(room).length));
  let roomsWorking = $derived(workingCounts.filter(Boolean).length);
  let unreachable = $derived(hosts?.filter(h => !h.reachable).length ?? 0);
  let failing = $derived([...attention.rooms.keys()].reduce((sum, room) => sum + attention.failing(room), 0));
  let low = $derived(hosts?.filter(h => h.diskLow) ?? []);
  let lowDisk = $derived(low.length === 1 ? `${hostLabel(low[0].name)} is low on disk` : `${low.length} machines are low on disk`);
  let summary = $derived([
    asks && failing && `${count(failing, "session")} failed`,
    (asks || failing) && low.length && lowDisk,
    roomsWorking && `${count(roomsWorking, "room")} working`,
    hosts?.length && count(hosts.length, "machine") + (unreachable ? `, ${unreachable} unreachable` : ""),
    accounts && busiestWindow(accounts),
  ].filter(Boolean).join(" · "));
</script>

<div class="home">
  <!-- Until room.list answers, the sentence is a skeleton of the same height: Home never claims nothing waits before it knows. -->
  {#if !loaded}<p class="hero skeleton" aria-hidden="true"><i></i><i></i></p>
  {:else}<p class="hero">{asks ? `${count(asks, "request")} ${asks === 1 ? "waits" : "wait"} for you.` : failing ? `${count(failing, "session")} failed.` : low.length ? `${lowDisk}.` : "Nothing needs you."}{#if summary}<span>{summary}</span>{/if}</p>{/if}
  <!-- Requests while any wait, then Activity, each a full-width card whose rows fill a grid of about 280 px columns. -->
  <div class="top">
    {#if asks || decided.size}<section class="card requests"><h2 tabindex="-1" bind:this={requestsHeading}>Requests{#if waiting.length}<span>{waiting.length}</span>{/if}</h2>
      <div class="list">{#each groups as { room, count } (room.id)}<a class="item fold" href={roomHref(room.id)} onclick={event => { navigate(event); if (event.defaultPrevented) onrequests(); }}>
        <Icon name="chevron" /><Avatar kind="room" name={room.name} label={initials.get(room.name)} size={28} />
        <span class="lines"><span class="title room" style:--hue={hue(room.name)}>{room.name}</span><span class="line">{count} waiting</span></span></a>
      {:else}{#each waiting as { id, room, value, who, root } (id)}{@const soon = Date.parse(value.expires) - now < 5 * 60_000}{@const line = requestLine(value)}<div class="item request">
        <Avatar kind={value.by.kind} name={who} label={value.by.session ? sessionInitials.get(value.by.session) : undefined} {root} size={28} />
        <!-- The title is the phrase after the asker's name, as on the request card. The body's first line says who gets what for how long, so Approve never hides it; only a line that does not name the host gives way to the host. -->
        <span class="lines"><span class="title"><b class="who {value.by.kind}" class:root style:--hue={hue(who)}>{who}</b> {value.title}</span>
          <span class="line">{#if line.named}{line.text}<code>{line.named}</code>{line.after}{" · "}{:else if value.target.kind === "host"}on <code>{value.target.name}</code>{" · "}{:else if value.body}{value.body}{" · "}{:else if value.target.kind !== "room"}on {value.target.kind} {value.target.name}{" · "}{/if}{room.name} ·
            {#if soon}<StatusPill tone="warning" label="Expires {fromNow(value.expires, now)}" />{:else}Expires {fromNow(value.expires, now)}{/if}</span>
          {#if failures.has(id)}<span class="line" role="alert">{failures.get(id)}</span>{/if}</span>
        <span class="acts"><button type="button" class="approve" disabled={deciding.has(id)} onclick={event => void decide(id, `${who} ${value.title}`, true, event.currentTarget.closest(".request") ?? event.currentTarget)}>Approve</button>
          <button type="button" disabled={deciding.has(id)} onclick={event => void decide(id, `${who} ${value.title}`, false, event.currentTarget.closest(".request") ?? event.currentTarget)}>Deny</button>
          <a class="open" href={roomHref(room.id)} onclick={event => { navigate(event); if (event.defaultPrevented) onrequests(); }}>Open</a></span></div>{/each}{/each}
        {#each [...decided] as [id, line] (id)}<p class="decided">{line}</p>{/each}</div>
      </section>{/if}
    <section class="card"><h2>Activity{#if roomsWorking}<span>{workingCounts.reduce((sum, n) => sum + n)} working</span>{/if}</h2>
      {#snippet row({ room, working, failed, line }: (typeof activity)[number])}<a class="item" href={roomHref(room.id)} onclick={navigate}>
        <Avatar kind="room" name={room.name} label={initials.get(room.name)} size={28} />
        <span class="lines"><span class="title room" style:--hue={hue(room.name)}>{room.name}</span><span class="line">{#if failed}<StatusPill tone="failed" word label={failed} />{line && " · "}{/if}{line}</span></span>
        <!-- Who works, from two sessions on (one is named in the line, unless a failure cuts it): three small avatars at most, then a count. -->
        {#if working.length > 1 || failed && working.length}<span class="stack">{#each working.slice(0, 3) as id (id)}<Avatar kind="session" name={sessions.get(id)?.name ?? ""} label={sessionInitials.get(id)} root={sessions.get(id)?.root ?? false} size={20} />{/each}
          {#if working.length > 3}<span class="more-count">+{working.length - 3}</span>{/if}</span>{/if}</a>{/snippet}
      <div class="list">{#each active as item (item.room.id)}{@render row(item)}{/each}
        {#if quiet.length}<button type="button" class="item fold quiet" aria-expanded={showQuiet} onclick={() => { showQuiet = !showQuiet; }}>
          <Icon name="chevron" /><span class="line">{count(quiet.length, "quiet room")}</span></button>
          {#if showQuiet}{#each quiet as item (item.room.id)}{@render row(item)}{/each}{/if}{/if}</div>
      {#if !activity.length}{#if loaded}<p class="none">No room yet.</p>
        {:else}<div class="item skeleton" aria-hidden="true"><i class="avatar"></i><span class="lines"><i></i><i></i></span></div>{/if}{/if}</section>
  </div>
  {#if failed && missing}<p class="failed">Some items could not load. <button type="button" class="retry" onclick={() => { failed = false; attempt++; }}>Try again</button></p>{/if}
  {#if hosts?.length || hostsFailed}<h2 id="machines" bind:this={machines}>Machines{#if hosts?.length}<span>{hosts.length}</span>{/if}</h2>{/if}
  {#if !hosts && hostsFailed}<p class="failed">Machines could not load.</p>{/if}
  {#if hosts?.length}<div class="cards">{#each hosts as host (host.name)}<HostCard {host} now={hostsAt} />{/each}</div>{/if}
  {#if accounts?.length || accountsFailed}<h2>Usage{#if accounts?.length}<span>{accounts.length}</span>{/if}</h2>{/if}
  {#if accountsFailed}<p class="failed">Model usage could not load.</p>{/if}
  {#if accounts?.length}<div class="cards">{#each accounts as account, i (i)}<UsageCard {account} now={loadedAt} />{/each}</div>{/if}
</div>

<style>
  /* Home is a dashboard, so its card grids use the page width up to 1200 px. */
  .home { flex: 1; min-height: 0; overflow-y: auto; display: flex; flex-direction: column; width: min(1200px, 100%); margin: 0 auto; padding: 16px 16px 32px; }
  /* 340 px columns keep three metric tiles' figures on one line. */
  .cards { display: grid; grid-template-columns: repeat(auto-fit, minmax(min(340px, 100%), 1fr)); gap: 12px; margin: 0 8px; }
  .top { display: grid; gap: 12px; margin: 8px 8px 0; }
  .hero { display: grid; gap: 4px; margin: 8px 8px 16px; font-size: 20px; font-weight: 600; line-height: 1.25; }
  .skeleton i { width: min(280px, 70%); height: 25px; border-radius: 6px; background: var(--hover); }
  .skeleton i + i { width: min(440px, 90%); height: 20px; }
  .item.skeleton i { width: 120px; height: 12px; }
  .item.skeleton i + i { width: 80px; height: 10px; margin-top: 4px; }
  .item.skeleton .avatar { flex: none; width: 28px; height: 28px; border-radius: 8px; margin: 0; }
  .hero span { color: var(--muted); font-size: 14px; font-weight: 400; line-height: 1.45; font-variant-numeric: tabular-nums; }
  .rows { display: grid; grid-template-columns: repeat(auto-fit, minmax(min(280px, 100%), 1fr)); column-gap: 12px; margin: 0 -8px; }
  h2 { display: flex; align-items: baseline; gap: 8px; margin: 24px 8px 12px; font-size: 18px; font-weight: 600; line-height: 1.25; }
  .card h2 { margin: 0 0 8px; font-size: 16px; }
  h2 span { color: var(--muted); font-weight: 400; font-variant-numeric: tabular-nums; }
  .item { display: flex; align-items: center; gap: 10px; min-width: 0; min-height: 44px; padding: 6px 8px; border-radius: 8px; color: var(--text); text-decoration: none; }
  .item:hover { background: var(--hover); }
  .none { margin: 12px 0 4px; color: var(--muted); font-size: 13px; }
  .failed { margin: 12px 8px; color: var(--muted); font-size: 13px; }
  /* A request row has SessionRow's shape: a main line, then one muted line that ends with the room. */
  .lines { flex: 1; min-width: 0; display: grid; line-height: 1.3; }
  .title, .line { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .title { font-weight: 600; font-size: 14px; }
  .line { color: var(--muted); font-size: 12px; }
  .room { color: oklch(var(--id-name) var(--hue)); }
  /* Requests are one column: each row carries its actions. A decided request is one muted line. */
  .list { display: grid; margin: 0 -8px; }
  .request { flex-wrap: wrap; }
  /* The owner approves from this line, so it wraps rather than cutting off who gets access or for how long. */
  .request .line { white-space: normal; }
  .who { font-weight: 500; color: oklch(var(--id-name) var(--hue)); }
  .who.owner { color: var(--focus); }
  .who.engine, .who.root { color: var(--text); }
  /* Only the host is mono, as it is a name the owner may type. */
  code { font: 12px var(--mono); color: var(--text); }
  .line :global(.status) { font-size: 12px; }
  .acts { display: flex; align-items: center; gap: 8px; margin-left: auto; }
  .acts button, .open { display: inline-grid; place-items: center; height: 30px; padding: 0 12px; border: 1px solid var(--line); border-radius: 6px; background: none;
    color: var(--text); font: inherit; font-size: 13px; font-weight: 500; text-decoration: none; cursor: pointer; }
  .acts button:hover:not(:disabled), .open:hover { background: var(--hover); }
  .acts .approve { background: var(--focus); color: var(--bg); border-color: var(--focus); }
  .acts .approve:hover:not(:disabled) { background: var(--focus); filter: brightness(1.08); }
  .open { border-color: transparent; color: var(--muted); }
  /* The ring sits outside a filled button, so it shows on the fill's own color. */
  .acts button:focus-visible, .open:focus-visible { outline-offset: 2px; }
  .acts :disabled { opacity: 0.6; cursor: default; }
  /* A folded room's row starts with a chevron, as every fold line does. */
  .fold :global(svg) { flex: none; color: var(--muted); transform: rotate(-90deg); }
  .fold[aria-expanded="true"] :global(svg) { transform: none; }
  .quiet { width: 100%; border: 0; background: none; font: inherit; text-align: left; cursor: pointer; min-height: 36px; }
  .stack { display: flex; flex: none; align-items: center; }
  .stack > :global(*) + :global(*) { margin-left: -4px; }
  .more-count { margin-left: 6px; color: var(--muted); font-size: 12px; font-variant-numeric: tabular-nums; }
  .decided { margin: 0; padding: 8px; color: var(--muted); font-size: 13px; }
  /* On a narrow card, Approve and Deny fill the width at 40 px, an easy touch target. */
  @container (max-width: 480px) {
    .acts { width: 100%; margin-left: 38px; }
    .acts button { flex: 1; height: 40px; }
  }
</style>
