<script lang="ts">
  import WorkingDot from "./WorkingDot.svelte";
  import { shortPaths } from "../feed.ts";
  import { hue } from "../identity.ts";
  import type { RoomStore } from "../room.svelte.ts";
  import type { Conversation } from "../contract.gen.ts";
  // The live line above the composer, as Discord's typing line: who is working now, and with one session, what it does.
  // A DM names only its own session. A session that waits for the owner is waiting, not working (status.ts).
  let { store, home, navigate }: { store: RoomStore; home: Conversation; navigate: (event: MouseEvent & { currentTarget: HTMLAnchorElement }) => void } = $props();
  let working = $derived([...store.sessions.values()]
    .filter(open => (home.kind === "feed" || open.session === home.session) && store.status(open.session) === "working"));
  let named = $derived(working.length > 3 ? working.slice(0, 2) : working);
  let more = $derived(working.length - named.length);
  let activity = $derived(working.length === 1 ? store.activity.get(working[0].session) : undefined);
</script>

<!-- The line keeps its 20 px when nobody works, so the composer never moves. Screen readers hear who starts or stops,
     not each activity label. -->
<p class="working" aria-live="polite">
  {#if working.length}<WorkingDot />
    <span class="text">{#each named as open, i (open.session)}{#if i}{i === named.length - 1 && !more ? " and " : ", "}{/if}<a href={`/r/${encodeURIComponent(store.room)}/s/${encodeURIComponent(open.session)}`} onclick={navigate} style:--hue={hue(open.name)}>{open.name}</a>{/each}{#if more}{` and ${more} more`}{/if}
      {working.length > 1 ? "are" : "is"} working{#if activity}<span aria-hidden="true">{` · ${shortPaths(activity)}`}</span>{/if}</span>
  {/if}
</p>

<style>
  /* It starts at the composer's text edge. Names carry their identity colors, so the line needs no avatars. */
  .working { flex: none; height: 20px; margin: 0 0 4px; padding: 0 16px 0 30px;
    display: flex; align-items: center; gap: 8px; color: var(--muted); font-size: 12.5px; line-height: 20px; white-space: nowrap; }
  .text { min-width: 0; overflow: hidden; text-overflow: ellipsis; }
  a { font-weight: 600; color: oklch(var(--id-name) var(--hue)); text-decoration: none; }
  a:hover { text-decoration: underline; }
  a:focus-visible { outline: 2px solid var(--focus); outline-offset: 2px; }
</style>
