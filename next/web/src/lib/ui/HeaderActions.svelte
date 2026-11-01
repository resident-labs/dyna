<script lang="ts">
  import type { RoomStore } from "../room.svelte.ts";
  import type { ShellJob } from "../shell.ts";
  import Avatar from "./Avatar.svelte";
  import Icon from "./Icon.svelte";
  import JobsTool from "./JobsTool.svelte";
  import RoomMenu from "./RoomMenu.svelte";
  import StatusPill from "./StatusPill.svelte";

  type JobSession = { id: string; name: string; root: boolean; running: number };
  let { store, session, sessions, allSessions, failures, phone, paused, panelShown, requestsOpen, operate, admin,
    jobsTool = $bindable(), roomMenu = $bindable(), requestsButton = $bindable(), toggle = $bindable(),
    onopen, onpause, oninstructions, onrequests, onpanel, onterminal, oncomputer }: {
    store: RoomStore; session?: string; sessions: JobSession[]; allSessions: JobSession[]; failures: string[];
    phone: boolean; paused: boolean; panelShown: boolean; requestsOpen: boolean; operate: boolean; admin: boolean;
    jobsTool?: JobsTool; roomMenu?: RoomMenu; requestsButton?: HTMLButtonElement; toggle?: HTMLButtonElement;
    onopen: (session: JobSession, job: ShellJob) => void; onpause: () => void; oninstructions: () => void;
    onrequests: () => void; onpanel: (tapped: boolean) => void; onterminal?: () => void; oncomputer?: () => void;
  } = $props();
  let waiting = $derived([...store.requests.values()].filter(request => (!session || request.by.session === session) && request.state === "pending" && request.class === "owner").length);
  let working = $derived(store.counts.working);
  let busy = $derived([...store.sessions.values()].filter(item => store.status(item.session) === "working"));
  let faces = $derived(!session && !panelShown && working > 0);
</script>

<div class="actions">
  <JobsTool bind:this={jobsTool} {sessions} {allSessions} {failures} allFailures={[...store.failedJobs]} {phone}
    {onopen} onseen={key => store.failedJobs.delete(key)} />
  <button type="button" class="requests" class:waiting={waiting > 0} bind:this={requestsButton} aria-pressed={requestsOpen}
    aria-label={waiting ? `${waiting} requests wait for you` : "Requests"} title="Requests" onclick={onrequests}>
    <Icon name="inbox" />{#if waiting}<StatusPill tone="warning" label="Waiting" count={waiting} />{/if}
  </button>
  <RoomMenu bind:this={roomMenu} {paused} {operate} {admin} {onpause} {oninstructions}
    onrequests={phone ? onrequests : undefined} onjobs={phone ? () => void jobsTool?.show() : undefined}
    onterminal={phone ? onterminal : undefined} oncomputer={phone ? oncomputer : undefined} jobFailed={phone && failures.length > 0} waiting={phone ? waiting : 0} />
  <button type="button" class="panel-toggle" bind:this={toggle} aria-label={"Sessions" + (faces ? `, ${working} working` : "") + (store.counts.failed ? ", a session failed" : "")}
    title={faces ? busy.map(item => item.name).join(", ") : "Sessions"} aria-controls="sessions" aria-expanded={panelShown} aria-pressed={panelShown} onclick={event => onpanel(event.detail > 0)}>
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
  .panel-toggle { display: flex; align-items: center; gap: 8px; margin-left: 0; }
  .faces { display: flex; }
  .faces > :global(.avatar) { --under: var(--bg); box-shadow: 0 0 0 2px var(--bg); letter-spacing: 0; }
  .more, .count { color: var(--muted); font-size: 12.5px; font-variant-numeric: tabular-nums; }
  .phone { display: none; }
  @media (max-width: 640px) {
    .actions :global(.jobs > .open), .requests { display: none; }
    .panel-toggle { gap: 4px; min-width: 40px; height: 40px; padding: 0 8px; }
    .phone { display: inline; }
    .more { display: none; }
  }
</style>
