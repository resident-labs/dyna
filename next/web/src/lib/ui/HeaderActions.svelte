<script lang="ts">
  import { requestsFor } from "../request-groups.ts";
  import type { Attention } from "../attention.svelte.ts";
  import type { RoomStore } from "../room.svelte.ts";
  import type { ShellJob } from "../shell.ts";
  import Avatar from "./Avatar.svelte";
  import Icon from "./Icon.svelte";
  import JobsTool from "./JobsTool.svelte";
  import RoomMenu from "./RoomMenu.svelte";
  import StatusPill from "./StatusPill.svelte";

  type JobSession = { id: string; name: string; root: boolean; running: number };
  let { store, session, attention, closed, phone, paused, panelShown, requestsOpen,
    jobsTool = $bindable(), roomMenu = $bindable(), requestsButton = $bindable(), toggle = $bindable(),
    onopen, onpause, oninstructions, onnewsession, onrequests, onpanel, onterminal, oncomputer }: {
    store: RoomStore; session?: string; attention: Attention; closed: boolean;
    phone: boolean; paused: boolean; panelShown: boolean; requestsOpen: boolean;
    jobsTool?: JobsTool; roomMenu?: RoomMenu; requestsButton?: HTMLButtonElement; toggle?: HTMLButtonElement;
    onopen: (session: JobSession, job: ShellJob) => void; onpause: () => void; oninstructions: () => void; onnewsession: () => void;
    onrequests: () => void; onpanel: (tapped: boolean) => void; onterminal?: () => void; oncomputer?: () => void;
  } = $props();
  let allSessions = $derived([...store.sessions.values()].filter(item => store.hadJobs.has(item.session) && store.boxes.get(item.session) === "ready")
    .map(item => ({ id: item.session, name: item.name, root: !item.parent, running: store.shellJobs.get(item.session) ?? 0 })));
  let sessions = $derived(session ? allSessions.filter(item => item.id === session) : allSessions);
  let failures = $derived([...store.failedJobs].filter(key => sessions.some(item => key.startsWith(`${item.id}/`))));
  let waiting = $derived(session ? requestsFor(store.requests.values(), session).filter(request => request.state === "pending" && request.class === "owner").length : store.waiting);
  let working = $derived(store.counts.working);
  let busy = $derived([...store.sessions.values()].filter(item => store.status(item.session) === "working"));
  let faces = $derived(!session && !panelShown && working > 0);
</script>

<div class="actions" class:session={!!session}>
  <JobsTool bind:this={jobsTool} {sessions} {allSessions} {failures} allFailures={[...store.failedJobs]} {phone}
    {onopen} onseen={key => store.failedJobs.delete(key)} />
  <button type="button" class="requests" class:waiting={waiting > 0} bind:this={requestsButton} aria-pressed={requestsOpen}
    aria-label={waiting ? `${waiting} requests wait for you` : "Requests"} title="Requests" onclick={onrequests}>
    <Icon name="inbox" />{#if waiting}<StatusPill tone="warning" label="Waiting" count={waiting} />{/if}
  </button>
  <RoomMenu bind:this={roomMenu} {paused} operate={attention.can("operate")} admin={attention.can("admin")} {onpause} {oninstructions} {onnewsession}
    onrequests={phone ? onrequests : undefined} onjobs={phone ? () => void jobsTool?.show() : undefined}
    onterminal={phone && session && !closed && attention.can("terminal") && store.boxes.get(session) === "ready" ? onterminal : undefined} oncomputer={phone && session && attention.can("computers") && store.computers.has(session) ? oncomputer : undefined} jobFailed={phone && failures.length > 0} waiting={phone ? waiting : 0} />
  <button type="button" class="panel-toggle" bind:this={toggle} aria-label={"Sessions" + (faces ? `, ${working} working` : "") + (store.counts.failed ? ", a session failed" : "")}
    title={faces ? busy.map(item => item.name).join(", ") : "Sessions"} aria-controls="sessions" aria-pressed={panelShown} onclick={event => onpanel(event.detail > 0)}>
    {#if faces}<span class="faces">{#each busy.slice(0, 3) as item (item.session)}<Avatar kind="session" name={item.name} root={!item.parent} size={24} />{/each}</span>
      {#if busy.length > 3}<span class="more">+{busy.length - 3}</span>{/if}<span class="count">{working} working</span>
    {:else}<Icon name="people" />{#if store.ready && !session}<span class="count phone">{store.sessions.size}</span>{/if}{/if}
    {#if store.counts.failed && !panelShown && !session}<span class="attention"></span>{/if}
  </button>
</div>

<style>
  .actions { margin-left: auto; display: flex; align-items: center; gap: 8px; flex: none; }
  .requests { display: flex; align-items: center; justify-content: center; gap: 6px; min-width: 28px; height: 28px; padding: 0; border: 0; border-radius: 6px; background: none; color: var(--muted); cursor: pointer; }
  .requests:hover, .requests[aria-pressed="true"] { background: var(--hover); color: var(--text); }
  @media (min-width: 761px) { .requests.waiting { margin-right: 4px; } }
  .panel-toggle { display: flex; align-items: center; gap: 8px; }
  .panel-toggle[aria-pressed="true"] { background: var(--hover); color: var(--text); }
  .faces { display: flex; }
  .faces > :global(.avatar) { --under: var(--bg); box-shadow: 0 0 0 2px var(--bg); letter-spacing: 0; }
  .more, .count { color: var(--muted); font-size: 12.5px; font-variant-numeric: tabular-nums; }
  .phone { display: none; }
  @media (max-width: 640px) {
    .session :global(.jobs > .open), .session .requests { display: none; }
    .requests { min-width: 44px; min-height: 44px; }
    .panel-toggle { gap: 4px; min-width: 44px; min-height: 44px; padding: 0 8px; }
    .phone { display: inline; }
    .more { display: none; }
  }
</style>
