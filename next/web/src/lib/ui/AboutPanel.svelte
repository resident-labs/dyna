<script lang="ts">
  import StatusPill from "./StatusPill.svelte";
  import { count } from "../feed.ts";
  import type { RoomStore } from "../room.svelte.ts";
  import type { BoxState } from "../contract.gen.ts";
  // What a DM's session is, as design A's About tab: its goal, then one label and value per fact the engine reports.
  // A row without a value does not show. Jobs opens the session's job list.
  let { store, room, session, line, navigate, onjobs }: {
    store: RoomStore; room: string; session: string; line: string; navigate: (event: MouseEvent & { currentTarget: HTMLAnchorElement }) => void; onjobs: () => void;
  } = $props();
  const boxes: Record<BoxState, string> = {
    pending: "Starting", creating: "Starting", starting: "Starting", ready: "Ready", stopped: "Stopped",
    closing: "Closing", deleted: "Deleted", failed: "Could not start", lost: "Stopped unexpectedly",
  };
  let open = $derived(store.sessions.get(session));
  let parent = $derived(open?.parent && store.names.get(open.parent));
  let box = $derived(store.boxes.get(session));
  let jobs = $derived(box === "ready" ? store.shellJobs.get(session) ?? 0 : 0);
</script>

<div class="about">
  {#if open?.goal}<h3>Goal</h3><p class="goal">{open.goal}</p>{/if}
  <dl>
    <dt>Status</dt><dd>{line}</dd>
    {#if parent && open?.parent}<dt>Parent</dt><dd><a href={`/r/${encodeURIComponent(room)}/s/${encodeURIComponent(open.parent)}`} onclick={navigate}>{parent}</a></dd>{/if}
    {#if open?.profile}<dt>Profile</dt><dd>{open.profile}</dd>{/if}
    {#if box}<dt>Box</dt><dd>{#if box === "failed" || box === "lost"}<StatusPill tone="failed" label={boxes[box]} />{:else}{boxes[box]}{/if}</dd>{/if}
    {#if jobs}<dt>Jobs</dt><dd><button type="button" onclick={onjobs}>{count(jobs, "job")} running</button></dd>{/if}
  </dl>
</div>

<style>
  .about { flex: 1; min-height: 0; overflow-y: auto; padding: 16px; font-size: 14px; }
  h3 { margin: 0 0 4px; color: var(--muted); font-size: 12.5px; font-weight: 400; }
  .goal { margin: 0 0 16px; line-height: 1.45; overflow-wrap: anywhere; }
  dl { display: grid; grid-template-columns: 72px minmax(0, 1fr); gap: 8px 12px; margin: 0; }
  dt { color: var(--muted); font-size: 12.5px; line-height: 20px; }
  dd { margin: 0; line-height: 20px; overflow-wrap: anywhere; }
  a, button { padding: 0; border: 0; border-radius: 4px; background: none; color: var(--focus); font: inherit; text-decoration: none; cursor: pointer; }
  a:hover, button:hover { text-decoration: underline; }
  a:focus-visible, button:focus-visible { outline: 2px solid var(--focus); outline-offset: 2px; }
</style>
