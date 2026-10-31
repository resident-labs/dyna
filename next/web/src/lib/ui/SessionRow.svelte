<script lang="ts">
  import SessionFace from "./SessionFace.svelte";
  import StatusPill from "./StatusPill.svelte";
  import { hue } from "../identity.ts";
  import type { Status } from "../status.ts";
  // One session row: its face with the session's one status; the
  // name in its identity color; and a muted line that says what the session does. Only the word "Failed" takes a
  // color in that line, and the failure's reason truncates first. mark names the dot when it stands for more than
  // the row, such as a failure in a folded subtree: then dot is that subtree's status, while state and the line stay
  // the row's own.
  let { name, root, state = "idle", dot = state, line, mark }: {
    name: string; root: boolean; state?: Status; dot?: Status; line: string; mark?: string;
  } = $props();
  // A name whose hue sits next to its status color would read as status, so it shows in the text color instead.
  const statusHues = { waiting: 70, failed: 25, working: 150 };
  let quiet = $derived([state, dot].some(status => status !== "idle" && Math.abs(((hue(name) - statusHues[status] + 540) % 360) - 180) < 30));
</script>

<SessionFace {name} {root} {dot} {mark} />
<span class="who"><span class="name" class:quiet style:--hue={hue(name)}>{name}</span>
  <span class="line" class:split={state === "failed"}>{#if state === "failed"}<StatusPill tone="failed" word label="Failed" />{#if line}<span class="reason">{` · ${line}`}</span>{/if}{:else}{line}{/if}</span></span>

<style>
  .who { flex: 1; min-width: 0; display: grid; line-height: 1.3; }
  .name { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-weight: 600; font-size: 14px; color: oklch(var(--id-name) var(--hue)); }
  .name.quiet { color: var(--text); }
  .line { min-width: 0; color: var(--muted); font-size: 12.5px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .split { display: flex; }
  .line > :global(.status) { opacity: 1; font-weight: 400; }
  .reason { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: pre; }
</style>
