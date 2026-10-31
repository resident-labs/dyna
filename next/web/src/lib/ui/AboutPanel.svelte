<script lang="ts">
  import { SvelteMap } from "svelte/reactivity";
  import ProfileMenu from "./ProfileMenu.svelte";
  import StatusPill from "./StatusPill.svelte";
  import { boxReason, count, fromNow, fullTime } from "../feed.ts";
  import { failed } from "../jobs.ts";
  import { watchShell, type ShellJob } from "../shell.ts";
  import type { RoomStore } from "../room.svelte.ts";
  import type { BoxState } from "../contract.gen.ts";
  // What a DM's session is, as design A's About tab: its goal, then one label and value per fact the engine reports.
  // A row without a value does not show. Jobs opens the dock's list on this session. It counts the jobs the engine lists, as
  // that list does, because session.list's count can lag; until they load, it reads Jobs.
  let { store, room, session, navigate, onjobs }: {
    store: RoomStore; room: string; session: string; navigate: (event: MouseEvent & { currentTarget: HTMLAnchorElement }) => void; onjobs: (event: MouseEvent) => void;
  } = $props();
  const boxes: Record<BoxState, string> = {
    pending: "Starting", creating: "Starting", starting: "Starting", ready: "Ready", stopped: "Stopped",
    closing: "Closing", deleted: "Deleted", failed: "Failed", lost: "Stopped unexpectedly",
  };
  let open = $derived(store.sessions.get(session));
  let parent = $derived(open?.parent && store.names.get(open.parent));
  let box = $derived(store.boxes.get(session));
  let failure = $derived(store.boxFailures.get(session));
  let hasJobs = $derived(box === "ready" && (store.shellJobs.get(session) ?? 0) > 0);
  const listed = new SvelteMap<string, ShellJob>();
  let visible = $state(document.visibilityState === "visible");
  $effect(() => {
    if (!hasJobs || !visible) return;
    const stop = watchShell(session, listed, closed => { if (closed) listed.clear(); });
    return () => { stop(); listed.clear(); };
  });
  // The two relative times stay roughly true while the panel stays open.
  let now = $state(Date.now());
  $effect(() => { const timer = setInterval(() => { now = Date.now(); }, 30_000); return () => clearInterval(timer); });
  let running = $derived([...listed.values()].filter(job => job.running).length);
  let failures = $derived([...listed.values()].filter(failed).length);
</script>

<svelte:document onvisibilitychange={() => { visible = document.visibilityState === "visible"; }} />
<div class="about">
  {#if open?.goal}<h3>Goal</h3><p class="goal">{open.goal}</p>{/if}
  <dl>
    {#if parent && open?.parent}<dt>Parent</dt><dd><a href={`/r/${encodeURIComponent(room)}/s/${encodeURIComponent(open.parent)}`} onclick={navigate}>{parent}</a></dd>{/if}
    {#if open?.profile}<dt>Profile</dt><dd><ProfileMenu {session} profile={open.profile} /></dd>{/if}
    {#if box}<dt>Box</dt><dd>{#if failure}<StatusPill tone="failed" word label="Failed" /> · {boxReason(failure)}{:else}{boxes[box]}{/if}</dd>{/if}
    {#if hasJobs}<dt>Jobs</dt><dd><button type="button" onclick={onjobs}>{running ? `${count(running, "job")} running` : "Jobs"}</button>{#if failures}{" · "}<StatusPill tone="failed" label="{failures} failed" word />{/if}</dd>{/if}
    {#if open?.startedAt}<dt>Started</dt><dd class="muted"><time datetime={open.startedAt} title={fullTime(open.startedAt)}>{fromNow(open.startedAt, Math.max(now, Date.parse(open.startedAt)))}</time></dd>{/if}
    {#if open?.lastTurnAt}<dt>Last turn</dt><dd class="muted"><time datetime={open.lastTurnAt} title={fullTime(open.lastTurnAt)}>{fromNow(open.lastTurnAt, Math.max(now, Date.parse(open.lastTurnAt)))}</time></dd>{/if}
  </dl>
</div>

<style>
  .about { flex: 1; min-height: 0; overflow-y: auto; padding: 16px; font-size: 14px; }
  h3 { margin: 0 0 4px; color: var(--muted); font-size: 12.5px; font-weight: 400; }
  .goal { margin: 0 0 16px; line-height: 1.45; overflow-wrap: anywhere; }
  dl { display: grid; grid-template-columns: 72px minmax(0, 1fr); gap: 8px 12px; margin: 0; }
  dt { color: var(--muted); font-size: 12.5px; line-height: 20px; }
  dd { margin: 0; line-height: 20px; overflow-wrap: anywhere; }
  .muted { color: var(--muted); }
  a, button { padding: 0; border: 0; border-radius: 4px; background: none; color: var(--focus); font: inherit; text-decoration: none; cursor: pointer; }
  a:hover, button:hover { text-decoration: underline; }
  a:focus-visible, button:focus-visible { outline: 2px solid var(--focus); outline-offset: 2px; }
</style>
