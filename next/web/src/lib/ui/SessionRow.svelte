<script lang="ts">
  import Avatar from "./Avatar.svelte";
  import StatusPill from "./StatusPill.svelte";
  import { hue } from "../identity.ts";
  import type { Status } from "../status.ts";
  // One session row: the avatar with the session's one status as a dot on its corner, as Discord marks presence; the
  // name in its identity color; and a muted line that says what the session does. Only the word "Failed" takes a
  // color in that line, and the failure's reason truncates first. mark names the dot when it stands for more than
  // the row, such as a failure in a folded subtree.
  let { name, root, state = "idle", line, mark }: { name: string; root: boolean; state?: Status; line: string; mark?: string } = $props();
  const tones = { waiting: "warning", failed: "failed", working: "working" } as const;
  // The line already says the status, so the dot is only a tooltip unless mark names more than the row.
  const words = { waiting: "Waiting", failed: "Failed", working: "Working" };
  // A name whose hue sits next to its status color would read as status, so it shows in the text color instead.
  const statusHues = { waiting: 70, failed: 25, working: 150 };
  let quiet = $derived(state !== "idle" && Math.abs(((hue(name) - statusHues[state] + 540) % 360) - 180) < 30);
</script>

<span class="face"><Avatar kind="session" {name} {root} size={28} />
  {#if state !== "idle"}<span class="mark" aria-hidden={mark ? undefined : "true"}><StatusPill tone={tones[state]} dot label={mark ?? words[state]} /></span>{/if}</span>
<span class="who"><span class="name" class:quiet style:--hue={hue(name)}>{name}</span>
  <span class="line" class:split={state === "failed"}>{#if state === "failed"}<StatusPill tone="failed" word label="Failed" />{#if line}<span class="reason">{` · ${line}`}</span>{/if}{:else}{line}{/if}</span></span>

<style>
  .face { position: relative; flex: none; display: inline-grid; }
  /* The dot sits on the avatar's lower right corner, ringed in the color behind the row (--ring, the panel's by
     default) so it reads apart from the face. */
  .mark { position: absolute; right: -4px; bottom: -4px; display: grid; }
  .mark > :global(.status) { width: 14px; height: 14px; border-radius: 50%; background: var(--ring, var(--sidebar)); opacity: 1; }
  .mark > :global(.status i) { width: 8px; height: 8px; }
  .who { flex: 1; min-width: 0; display: grid; line-height: 1.3; }
  .name { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-weight: 600; font-size: 14px; color: oklch(var(--id-name) var(--hue)); }
  .name.quiet { color: var(--text); }
  .line { min-width: 0; color: var(--muted); font-size: 12.5px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .split { display: flex; }
  .line > :global(.status) { opacity: 1; font-weight: 400; }
  .reason { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: pre; }
</style>
