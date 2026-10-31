<script lang="ts">
  import Avatar from "./Avatar.svelte";
  import type { RoomStore } from "../room.svelte.ts";
  // One direct-message row per open session. The left column stays calm: live work shows only in the sessions panel.
  let { store, href, current, navigate }: {
    store: RoomStore; href: (session: string) => string; current?: string;
    navigate: (event: MouseEvent & { currentTarget: HTMLAnchorElement }) => void;
  } = $props();
</script>

<div class="section-title">Direct messages</div>
<nav aria-label="Direct messages">{#each [...store.sessions.values()] as item (item.session)}
  <a href={href(item.session)} onclick={navigate} aria-current={current === item.session ? "page" : undefined}>
    <Avatar kind="session" name={item.name} root={!item.parent} size={20} /><span class="name">{item.name}</span></a>
{:else}<p class="hint">No sessions yet.</p>{/each}</nav>

<style>
  .name { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
</style>
