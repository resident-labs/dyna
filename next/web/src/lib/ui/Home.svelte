<script lang="ts">
  import { tick } from "svelte";
  import { SvelteMap, SvelteSet } from "svelte/reactivity";
  import { APIError, call } from "../client.ts";
  import { failedRooms, type RoomLine } from "../active.ts";
  import type { Attention } from "../attention.svelte.ts";
  import { accountPath, reconnectPath } from "../accounts.ts";
  import type { AccountUsage, AccountsListResponse, Card, HostSummary, Principal, Question, SessionListResponse } from "../contract.gen.ts";
  import { clause, fromNow, requestLine } from "../feed.ts";
  import { hostLabel, hostMeters, largeShared, sharedSize, watchHosts } from "../hosts.ts";
  import { questionPlan } from "../questions.ts";
  import { accountAlerts, busiestWindow, holdFor, holding, outOfLimit, tucked, usageRows, type UsageHold, type UsageRow } from "../usage.ts";
  import { distinctInitials, initials as letters } from "../identity.ts";
  import Avatar from "./Avatar.svelte";
  import HostCard from "./HostCard.svelte";
  import QuestionAnswer from "./QuestionAnswer.svelte";
  import Icon from "./Icon.svelte";
  import StatusPill from "./StatusPill.svelte";
  import UsageCard from "./UsageCard.svelte";
  type Navigate = (event: MouseEvent & { currentTarget: HTMLAnchorElement }) => void;
  // onrequests opens the Requests panel once the link has moved to that room. loaded is false until room.list answers.
  // initials are the rooms' distinct initials, as the rail shows them. active is the Active now column's rooms; below
  // its breakpoint they are one row here, which onactive opens as the sheet. covered is true while Settings is open over Home.
  let { attention, initials, loaded, navigate, onrequests, active, onactive, covered }: {
    attention: Attention; initials: Map<string, string>; loaded: boolean; navigate: Navigate; onrequests: () => void;
    active: RoomLine[]; onactive: (event: MouseEvent & { currentTarget: HTMLButtonElement }) => void; covered: boolean;
  } = $props();
  // Each room's sessions load once while Home is open, for its failure and request rows. Requests load per room, and
  // again only when a room gains a request or requester that no answer named yet. A failed load forgets its IDs and
  // offers Try again.
  const requests = new SvelteMap<string, { title: string; body: string; by: Principal; target: Card["target"]; expires: string }>();
  const questions = new SvelteMap<string, Question>();
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
      load(attention.questions.get(room) ?? [], questions, () => call("question.list", { room }), list => {
        for (const question of list.questions ?? []) questions.set(question.id, question);
      });
      // A requester or a failed session that started after the room's list loads it again.
      const by = [...[...attention.waiting.get(room) ?? []].flatMap(id => requests.get(id)?.by.session ?? []), ...[...attention.questions.get(room) ?? []].flatMap(id => questions.get(id)?.session ?? [])];
      if (listed.has(room)) load([...by, ...attention.down(room).map(({ id }) => id)], sessions, () => attention.listSessions(room), keepSessions(room));
    }
  });
  const rows = <T,>(sets: Map<string, Set<string>>, known: SvelteMap<string, T>) => [...attention.rooms.values()].flatMap(room =>
    [...sets.get(room.id) ?? []].flatMap(id => { const value = known.get(id); return value === undefined ? [] : [{ id, room, value }]; }));
  // A request shows once its requester is named, as in the Requests panel. One decided here stays out while its event comes.
  const named = <T,>(row: T, by: Principal) => {
    if (by.kind !== "session" || !by.session) return [{ ...row, who: by.kind === "owner" ? "you" : "Dyna", root: false }];
    const session = sessions.get(by.session);
    if (!session && !answered.has(by.session)) return [];
    return [{ ...row, who: session?.name ?? "A closed session", root: session?.root ?? false }];
  };
  let waiting = $derived(rows(attention.waiting, requests).filter(row => !decided.has(row.id)).flatMap(row => named(row, row.value.by)));
  let asking = $derived(rows(attention.questions, questions).filter(row => !decided.has(row.id)).flatMap(row => named(row, { kind: "session", session: row.value.session })));
  // Each session's initials, distinct within its room as the room's own page shows them.
  let sessionInitials = $derived(new Map([...Map.groupBy(sessions, ([, item]) => item.room).values()].flatMap(list => {
    const labels = distinctInitials(list.map(([, item]) => item.name));
    return list.map(([id, item]) => [id, labels.get(item.name) ?? letters(item.name)]);
  })));
  // More than 4 requests fold into one row per room, which opens that room's Requests.
  let groups = $derived(waiting.length + asking.length <= 4 ? [] : [...attention.rooms.values()].flatMap(room => {
    const count = [...waiting, ...asking].filter(row => row.room.id === room.id).length;
    return count ? [{ room, count }] : [];
  }));
  const count = (n: number, unit: string) => `${n} ${unit}${n === 1 ? "" : "s"}`;
  let failures = $derived(failedRooms(attention, id => sessions.get(id)?.name));
  // Only the owner can end a wait for a host that takes the room, so each such room has one row that opens it.
  let hostless = $derived([...attention.rooms.values()].flatMap(room => {
    const waits = attention.hostless.get(room.id)?.size ?? 0;
    return waits ? [{ room, waits }] : [];
  }));
  // Approve and Deny decide in place. The engine's op.decided event takes the row out of waiting; the owner's own
  // decision stays as one muted line until Home closes, so the row does not just vanish under the pointer.
  const decided = new SvelteMap<string, string>();
  const deciding = new SvelteSet<string>();
  const refused = new SvelteMap<string, string>();
  // Focus that was on the decided row moves to the next request's Approve, or to the Needs you heading when none is
  // left, so a keyboard owner keeps their place. It is read before the buttons disable, which drops focus.
  let needsHeading: HTMLElement | undefined = $state();
  async function decide(id: string, title: string, approve: boolean, row: HTMLElement) {
    const held = row.contains(document.activeElement);
    const sibling = (next: Element | null) => next?.classList.contains("request") ? next : undefined;
    const neighbor = sibling(row.nextElementSibling) ?? sibling(row.previousElementSibling);
    deciding.add(id);
    refused.delete(id);
    const saved = await call("op.decide", { id, approve }).then(() => { decided.set(id, `${approve ? "Approved" : "Denied"} · ${title}`); return true; }, (error: unknown) => {
      refused.set(id, error instanceof APIError && error.detail.code === "conflict" ? "This request is no longer waiting." : "Your decision was not saved. Try again.");
      return false;
    });
    deciding.delete(id);
    if (!held) return;
    await tick();
    if (!saved) { row.querySelector<HTMLElement>(".approve")?.focus(); return; }
    (neighbor?.isConnected ? neighbor.querySelector<HTMLElement>(".approve") : needsHeading)?.focus();
  }
  // A list shows while the store has items, as in v1's Home a failed load is never "all clear";
  // its count is the rows shown.
  const total = (sets: Map<string, { size: number }>) => [...sets.values()].reduce((sum, set) => sum + set.size, 0);
  let asks = $derived(total(attention.waiting) + total(attention.questions));
  let missing = $derived(waiting.length + asking.length < asks
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
  // Model usage loads when Home opens: the engine caches it for 5 min. Reset times count from that answer.
  // accounts.list names each account's provider. Without it the cards still show their labels.
  let accounts: AccountUsage[] | undefined = $state();
  let catalog: AccountsListResponse | undefined = $state();
  let accountsFailed = $state(false);
  let loadedAt = $state(0);
  // Home stays mounted behind Settings, so it loads again when Settings closes: an account may have changed there.
  let opened = false;
  $effect(() => {
    if (covered && opened) return;
    opened = true;
    void Promise.allSettled([call("accounts.quota", {}), call("accounts.list", {})]).then(([quota, list]) => {
      if (list.status === "fulfilled") catalog = list.value;
      if (quota.status === "fulfilled") { accounts = quota.value.accounts ?? []; accountsFailed = false; loadedAt = Date.now(); } else accountsFailed = true;
    });
  });
  const holdsKey = "dyna.usage.holds";
  let usage = $derived(usageRows(accounts ?? [], catalog));
  // Hidden is a closed section under the cards. Only active accounts show by default: an inactive one waits there, next
  // to spent ones, which take no work until they reset or their balance rises. The owner can show an inactive account,
  // or hide any other until the same happens. Those choices stay in this browser.
  let holds = $state<Record<string, UsageHold>>(savedHolds());
  const held = (row: UsageRow) => row.id in holds && !holds[row.id].shown && holding(row, holds[row.id], loadedAt);
  const inHidden = (row: UsageRow) => tucked(row, holds[row.id], loadedAt);
  let shown = $derived(usage.filter(row => !inHidden(row)));
  let hiddenRows = $derived(usage.filter(inHidden));
  function savedHolds(): Record<string, UsageHold> {
    try { return JSON.parse(localStorage.getItem(holdsKey) ?? "{}"); } catch { return {}; }
  }
  function setHolds(next: Record<string, UsageHold>) {
    holds = next;
    try { localStorage.setItem(holdsKey, JSON.stringify(next)); } catch { /* Storage is blocked: the choice lasts until the page closes. */ }
  }
  const release = (id: string) => setHolds(Object.fromEntries(Object.entries(holds).filter(([key]) => key !== id)));
  $effect(() => { for (const row of usage) if (row.id in holds && !holds[row.id].shown && !held(row)) release(row.id); });
  const hide = (row: UsageRow) => row.active ? setHolds({ ...holds, [row.id]: holdFor(row, loadedAt) }) : release(row.id);
  const show = (row: UsageRow) => row.active ? release(row.id) : setHolds({ ...holds, [row.id]: { shown: true } });
  let alerts = $derived(accountAlerts(accounts ?? [], catalog));
  // One sentence says what needs the owner: the same items as the Needs you card. Under it, a muted line sums low disk,
  // a large shared folder, machines and the busiest usage window; a part with nothing to say is left out.
  let needs = $derived(asks + failures.length + hostless.length + alerts.length);
  let unreachable = $derived(hosts?.filter(h => !h.reachable).length ?? 0);
  let low = $derived(hosts?.filter(h => h.diskLow) ?? []);
  let lowDisk = $derived(low.length === 1 ? `${hostLabel(low[0].name)} is low on disk` : `${low.length} machines are low on disk`);
  // A large shared folder never changes the sentence; the muted line names the largest, its room and its host.
  const roomName = (room: string) => attention.rooms.get(room)?.name ?? "a closed room";
  let shared = $derived((hosts ?? []).flatMap(h => largeShared(h).map(folder => ({ ...folder, host: h.name }))).sort((a, b) => b.bytes - a.bytes));
  let summary = $derived([
    low.length && lowDisk,
    shared.length && `Shared folder of ${roomName(shared[0].room)} holds ${sharedSize(shared[0].bytes)} on ${shared[0].host === "control-plane" ? "this computer" : shared[0].host}`
      + (shared.length > 1 ? `, and ${shared.length - 1} more ${shared.length === 2 ? "is" : "are"} large` : ""),
    !active.length && "No room is working",
    hosts?.length && count(hosts.length, "machine") + (unreachable ? `, ${unreachable} unreachable` : ""),
    busiestWindow(shown),
  ].filter(Boolean).join(" · "));
</script>

<div class="home" data-page-ready={loaded && (!!hosts || hostsFailed) && (!!accounts || accountsFailed)}>
  <!-- Until room.list answers, and usage too if nothing else needs the owner, the sentence is a skeleton of the same height: Home never claims nothing waits before it knows. -->
  {#if !loaded || (!needs && !accounts && !accountsFailed)}<p class="hero skeleton" aria-hidden="true"><i></i><i></i></p>
  {:else}<p class="hero">{needs ? `${count(needs, "thing")} need${needs === 1 ? "s" : ""} you.` : "Nothing needs you."}{#if summary}<span>{summary}</span>{/if}</p>{/if}
  <!-- Needs you shows while anything does, as one card with one column of rows. -->
  {#if needs || decided.size}<div class="top"><section class="card"><h2 tabindex="-1" bind:this={needsHeading}>Needs you{#if needs}<span>{needs}</span>{/if}</h2>
    <div class="list">{#each groups as { room, count } (room.id)}<a class="item fold" href={roomHref(room.id)} onclick={event => { navigate(event); if (event.defaultPrevented) onrequests(); }}>
      <Icon name="chevron" /><Avatar kind="room" name={room.name} label={initials.get(room.name)} size={28} />
      <span class="lines"><span class="title">{room.name}</span><span class="line">{count} waiting</span></span></a>
    {:else}{#each waiting as { id, room, value, who, root } (id)}{@const soon = Date.parse(value.expires) - now < 5 * 60_000}{@const line = requestLine(value)}<div class="item request">
      <Avatar kind={value.by.kind} name={who} label={value.by.session ? sessionInitials.get(value.by.session) : undefined} {root} size={28} />
      <!-- The title is the phrase after the asker's name, as on the request card. The body's first line says who gets what for how long, so Approve never hides it; only a line that does not name the host gives way to the host. -->
      <span class="lines"><span class="title"><b class="who">{who}</b> {value.title}</span>
        <span class="line">{#if line.named}{line.text}<code>{line.named}</code>{clause(line.after)}{" · "}{:else if value.target.kind === "host"}on <code>{value.target.name}</code>{" · "}{:else if value.body}{clause(value.body)}{" · "}{:else if value.target.kind !== "room"}on {value.target.kind} {value.target.name}{" · "}{/if}{room.name} ·
          {#if soon}<StatusPill tone="warning" label="Expires {fromNow(value.expires, now)}" />{:else}Expires {fromNow(value.expires, now)}{/if}</span>
        {#if refused.has(id)}<span class="line" role="alert">{refused.get(id)}</span>{/if}</span>
      <span class="acts">{#if attention.can("admin")}<button type="button" class="approve" disabled={deciding.has(id)} onclick={event => void decide(id, `${who} ${value.title}`, true, event.currentTarget.closest(".request") ?? event.currentTarget)}>Approve</button>
        <button type="button" disabled={deciding.has(id)} onclick={event => void decide(id, `${who} ${value.title}`, false, event.currentTarget.closest(".request") ?? event.currentTarget)}>Deny</button>{/if}
        <a class="open" href={roomHref(room.id)} onclick={event => { navigate(event); if (event.defaultPrevented) onrequests(); }}>Open</a></span></div>{/each}
      {#each asking as { id, room, value, who, root } (id)}<div class="item request question">
        <Avatar kind="session" name={who} label={sessionInitials.get(value.session)} {root} size={28} />
        <span class="lines"><span class="title"><b class="who">{who}</b> asks</span><span class="line asked">{value.text}</span>{#if value.meanwhile}<span class="line plan">{questionPlan(value)}</span>{/if}<span class="line">{room.name}</span></span>
        <span class="acts"><a class="open" href={`${roomHref(room.id)}/s/${encodeURIComponent(value.session)}`} onclick={navigate}>Open</a></span>
        {#if attention.can("operate")}<div class="reply"><QuestionAnswer {id} name={who} onanswered={() => { decided.set(id, `Answered · ${who}`); }} /></div>{/if}</div>{/each}{/each}
      {#each failures as { room, title, reason } (room.id)}<div class="item failure"><Avatar kind="room" name={room.name} label={initials.get(room.name)} size={28} />
        <span class="lines"><span class="title">{title}</span><span class="line"><StatusPill tone="failed" label={reason} /> · {room.name}</span></span>
        <span class="acts"><a class="open" href={roomHref(room.id)} onclick={navigate}>Open</a></span></div>{/each}
      {#each hostless as { room, waits } (room.id)}<div class="item hostless"><Avatar kind="room" name={room.name} label={initials.get(room.name)} size={28} />
        <span class="lines"><span class="title">{waits === 1 ? "1 session waits" : `${waits} sessions wait`} for a host that takes {room.name}</span>
          <span class="line"><StatusPill tone="warning" label="No host takes this room" /></span></span>
        <span class="acts"><a class="open" href={roomHref(room.id)} onclick={navigate}>Open</a></span></div>{/each}
      {#each alerts as { id, title, provider, reconnect }, i (i)}{@const signIn = reconnect && attention.can("admin")}<div class="item account"><Avatar kind="room" name={title} size={28} />
        <span class="lines"><span class="title">{title}</span>
          <span class="line"><StatusPill tone="warning" label="Sign-in expired" />{#if provider}{" · "}{provider}{/if}</span></span>
        <span class="acts"><a class="open" class:sign-in={signIn} href={signIn ? reconnectPath(id) : accountPath(id)} onclick={navigate}>{signIn ? "Sign in again" : "Open"}</a></span></div>{/each}
      {#each [...decided] as [id, line] (id)}<p class="decided">{line}</p>{/each}</div>
    </section></div>{/if}
  <!-- Under Needs you, never above it, so Approve stays high. It stands for the column below the column's breakpoint. -->
  {#if active.length}<button type="button" class="strip" aria-haspopup="dialog" onclick={onactive}><b>Active now</b><em>{active.length}</em>
    <span class="stack">{#each active.slice(0, 3) as { room } (room.id)}<Avatar kind="room" name={room.name} label={initials.get(room.name)} size={24} />{/each}</span><Icon name="chevron" /></button>{/if}
  {#if failed && missing}<p class="failed">Some items could not load. <button type="button" class="retry" onclick={() => { failed = false; attempt++; }}>Try again</button></p>{/if}
  {#if hosts?.length || hostsFailed}<h2 id="machines" bind:this={machines}>Machines{#if hosts?.length}<span>{hosts.length}</span>{/if}</h2>{/if}
  {#if !hosts && hostsFailed}<p class="failed">Machines could not load.</p>{/if}
  {#if hosts?.length}<div class="cards">{#each hosts as host (host.name)}<HostCard {host} now={hostsAt} {roomName} {navigate} />{/each}</div>{/if}
  {#if usage.length || accountsFailed}<h2>Usage{#if shown.length}<span>{shown.length}</span>{/if}</h2>{/if}
  {#if accountsFailed}<p class="failed">Model usage could not load.</p>{/if}
  {#if shown.length}<div class="cards">{#each shown as row, i (i)}<UsageCard {row} now={loadedAt} onhide={() => hide(row)} />{/each}</div>{/if}
  {#if hiddenRows.length}<details class="tucked"><summary>Hidden<span>{hiddenRows.length}</span><i><Icon name="chevron" /></i></summary>
    <div class="cards">{#each hiddenRows as row, i (i)}<UsageCard {row} now={loadedAt} onshow={outOfLimit(row, loadedAt) ? undefined : () => show(row)} />{/each}</div></details>{/if}
</div>

<style>
  /* Home is a dashboard, so its card grids use the page width up to 1200 px. A phone gives it nearly all of that width. */
  .home { flex: 1; min-height: 0; overflow-y: auto; display: flex; flex-direction: column; width: min(1200px, 100%); margin: 0 auto; padding: 16px 16px 32px; }
  /* 340 px columns keep three metric tiles' figures on one line. */
  .cards { display: grid; grid-template-columns: repeat(auto-fit, minmax(min(340px, 100%), 1fr)); gap: 12px; margin: 0 8px; }
  .top { display: grid; gap: 12px; margin: 0 8px; }
  .hero { display: grid; gap: 4px; margin: 8px 8px 16px; font-size: 20px; font-weight: 600; line-height: 1.25; }
  .skeleton i { width: min(280px, 70%); height: 25px; border-radius: 6px; background: var(--hover); }
  .skeleton i + i { width: min(440px, 90%); height: 20px; }
  .hero span { color: var(--muted); font-size: 14px; font-weight: 400; line-height: 1.45; font-variant-numeric: tabular-nums; }
  h2 { display: flex; align-items: baseline; gap: 8px; margin: 24px 8px 12px; font-size: 18px; font-weight: 600; line-height: 1.25; }
  .card h2 { margin: 0 0 8px; font-size: 16px; }
  h2 span { color: var(--muted); font-weight: 400; font-variant-numeric: tabular-nums; }
  .tucked summary { display: flex; align-items: center; gap: 8px; width: fit-content; margin: 16px 8px 12px; color: var(--muted); font-size: 13px; cursor: pointer; list-style: none; }
  .tucked summary:focus-visible { outline: 2px solid var(--focus); }
  .tucked summary span { font-variant-numeric: tabular-nums; }
  .tucked summary i { display: inline-flex; transform: rotate(-90deg); }
  .tucked[open] summary i { transform: none; }
  .item { display: flex; align-items: center; gap: 10px; min-width: 0; min-height: 44px; padding: 6px 8px; border-radius: 8px; color: var(--text); text-decoration: none; }
  .item:hover { background: var(--hover); }
  .failed { margin: 12px 8px; color: var(--muted); font-size: 13px; }
  /* A request row has SessionRow's shape: a main line, then one muted line that ends with the room. */
  .lines { flex: 1; min-width: 0; display: grid; line-height: 1.3; }
  .title, .line { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .title { font-weight: 600; font-size: 14px; }
  .line { color: var(--muted); font-size: 12px; }
  /* Requests are one column: each row carries its actions. A decided request is one muted line. */
  .list { display: grid; margin: 0 -8px; }
  .request { flex-wrap: wrap; }
  /* The owner approves from this line, so it wraps rather than cutting off who gets access or for how long. */
  .request .line { white-space: normal; }
  /* A long question opens in full in the session's DM; four lines are enough to answer most from here. */
  .asked, .plan { display: -webkit-box; -webkit-box-orient: vertical; -webkit-line-clamp: 4; line-clamp: 4; overflow-wrap: anywhere; }
  .asked { color: var(--text); font-size: 13px; }
  .reply { flex-basis: 100%; padding-left: 38px; min-width: 0; }
  /* The owner acts on a failure's reason, so it wraps as well. */
  .failure .line, .hostless .line, .account .line { white-space: normal; }
  /* The sentence names the room, so it wraps on a phone instead of cutting the room's name. An account's name wraps too. */
  .hostless .title { white-space: normal; }
  .account .title { white-space: normal; overflow-wrap: anywhere; }
  .failure .line :global(.status), .hostless .line :global(.status), .account .line :global(.status) { display: inline; white-space: normal; }
  .failure .line :global(.status i), .hostless .line :global(.status i), .account .line :global(.status i) { display: inline-block; margin: 0 5px 1px 0; }
  .failure .acts, .hostless .acts, .account .acts { width: auto; margin-left: auto; }
  /* On Home every name is in the text color; the hue stays on the avatar. */
  .who { font-weight: 500; }
  /* Only the host is mono, as it is a name the owner may type. */
  code { font: 12px var(--mono); }
  .line :global(.status) { font-size: 12px; }
  .acts { display: flex; align-items: center; gap: 8px; margin-left: auto; }
  .acts button, .open { display: inline-grid; place-items: center; height: 30px; padding: 0 12px; border: 1px solid var(--line); border-radius: 6px; background: none;
    color: var(--text); font: inherit; font-size: 13px; font-weight: 500; text-decoration: none; cursor: pointer; }
  .acts button:hover:not(:disabled), .open:hover { background: var(--hover); }
  .acts .approve { background: var(--focus); color: var(--bg); border-color: var(--focus); }
  .acts .approve:hover:not(:disabled) { background: var(--focus); filter: brightness(1.08); }
  .open { border-color: transparent; color: var(--muted); }
  .open.sign-in { border-color: var(--line); color: var(--text); white-space: nowrap; }
  /* The ring sits outside a filled button, so it shows on the fill's own color. */
  .acts button:focus-visible, .open:focus-visible { outline-offset: 2px; }
  .acts :disabled { opacity: 0.6; cursor: default; }
  /* A folded room's row starts with a chevron, as every fold line does. */
  .fold :global(svg) { flex: none; color: var(--muted); transform: rotate(-90deg); }
  /* The column's one row: no card, so it reads as a link to the sheet and not as more content. */
  .strip { display: flex; align-items: center; gap: 10px; min-height: 44px; margin: 4px 8px 0; padding: 6px 8px; border: 0; border-radius: 6px; background: none; color: var(--text); font: inherit; font-size: 14px; font-weight: 500; text-align: left; cursor: pointer; }
  .strip em { color: var(--muted); font-style: normal; font-variant-numeric: tabular-nums; }
  .strip :global(svg) { flex: none; color: var(--muted); transform: rotate(-90deg); }
  @media (hover: hover) { .strip:hover { background: var(--hover); } }
  @media (min-width: 761px) { .strip { display: none; } }
  .stack { display: flex; flex: none; align-items: center; gap: 4px; margin-left: auto; }
  .decided { margin: 0; padding: 8px; color: var(--muted); font-size: 13px; }
  @media (max-width: 640px) { .home { padding: 12px 8px 24px; } .hero { margin-bottom: 12px; } }
  /* On a narrow card, Approve and Deny fill the width at 40 px, an easy touch target. */
  @container (max-width: 480px) {
    .acts { width: 100%; margin-left: 38px; }
    .acts button { flex: 1; height: 40px; }
    .question .acts { order: 1; }
  }
</style>
