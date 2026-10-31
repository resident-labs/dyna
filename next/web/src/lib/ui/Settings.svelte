<script lang="ts">
  import { untrack, type Snippet } from "svelte";
  import { APIError, call, SignedOut } from "../client.ts";
  import Blocked from "./Blocked.svelte";
  import AppearancePane from "./AppearancePane.svelte";
  import SharingPane from "./SharingPane.svelte";
  import AccountsPane from "./AccountsPane.svelte";
  import ProfilesPane from "./ProfilesPane.svelte";
  import Icon, { type IconName } from "./Icon.svelte";
  import type { Device } from "../contract.gen.ts";
  // The Settings page, in the feed's and the sidebar's place. It opens over the app, so the room behind keeps its feed,
  // its position and its draft. A wide page opens the first pane. A phone opens the pane list, and a pane's arrow returns to it.
  let { path, phone, navigate, onmenu, signedout, element = $bindable(), menu = $bindable() }: { path: string; phone: boolean; navigate: (event: MouseEvent & { currentTarget: HTMLAnchorElement }) => void;
    onmenu: (event: MouseEvent) => void; signedout: () => void; element?: HTMLElement; menu?: HTMLButtonElement } = $props();

  // device.list needs admin, so its refusal tells that this device has no Devices pane (spec 7.8).
  let devices: Device[] | undefined = $state.raw();
  let admin = $state(true), failed = $state(false);
  function reload() {
    call("device.list", {}).then(list => { devices = list.devices ?? []; admin = true; failed = false; }, (error: unknown) => {
      if (error instanceof SignedOut) signedout();
      else if (error instanceof APIError && error.detail.code === "forbidden") admin = false;
      // A failed poll keeps the list and the pair card on screen.
      else if (!devices) failed = true;
    });
  }
  untrack(reload);

  // Appearance is on every device; Devices only on one with admin.
  let panes: { id: string; label: string; icon: IconName }[] = $derived([{ id: "appearance", label: "Appearance", icon: "palette" },
    ...admin ? [{ id: "devices", label: "Sharing & access", icon: "devices" }, { id: "accounts", label: "Accounts", icon: "plug" }, { id: "profiles", label: "Profiles", icon: "tool" }] as const : []]);
  let shown = $derived(panes.find(pane => path === `/settings/${pane.id}` || (pane.id === "accounts" && path.startsWith("/settings/accounts/"))) ?? (phone ? undefined : panes[0]));
  // /settings and its trailing slash open the first pane or the list; any other path has no page.
  let lost = $derived(!/^\/settings\/?$/.test(path) && !panes.some(pane => path === `/settings/${pane.id}` || (pane.id === "accounts" && /^\/settings\/accounts\/(?:add(?:\/[^/]+)?|account\/[^/]+)$/.test(path))));
</script>

{#snippet bar(title: string, count?: number, action?: Snippet)}
  <header class="bar">{#if phone}<a class="round" href="/settings" onclick={navigate} aria-label="Back to Settings" title="Back to Settings"><Icon name="backChevron" /></a>{/if}
    <strong>{title}</strong>{#if count !== undefined}<span class="count">{count}</span>{/if}{@render action?.()}</header>
{/snippet}

<section class="settings" aria-label="Settings" tabindex="-1" bind:this={element}>
  {#if !phone}<aside class="nav"><header class="bar"><strong>Settings</strong></header>
    <nav aria-label="Settings panes">{#each panes as pane (pane.id)}
      <a href={`/settings/${pane.id}`} onclick={navigate} aria-current={shown === pane ? "page" : undefined}><Icon name={pane.icon} />{pane.label}</a>{/each}</nav></aside>{/if}
  <div class="pane">
    {#if lost}<Blocked kind="no-page" back={{ href: "/settings", label: "Back to Settings" }} {navigate} />
    {:else if shown?.id === "appearance"}<AppearancePane {bar} />
    {:else if shown?.id === "devices"}<SharingPane {devices} {failed} {reload} {signedout} {bar} />
    {:else if shown?.id === "accounts"}<AccountsPane {bar} {phone} {signedout} {path} {navigate} />
    {:else if shown?.id === "profiles"}<ProfilesPane {bar} {phone} {signedout} />
    {:else}
      <header class="bar"><button type="button" class="round menu" bind:this={menu} aria-label="Rooms and pages" title="Rooms and pages" onclick={onmenu}><Icon name="menu" /></button><strong>Settings</strong></header>
      <nav class="list" aria-label="Settings panes">{#each panes as pane (pane.id)}
        <a href={`/settings/${pane.id}`} onclick={navigate}><Icon name={pane.icon} />{pane.label}<Icon name="forwardChevron" /></a>{/each}</nav>
    {/if}
  </div>
</section>

<style>
  /* Over the room column and the sessions panel, under the phone's drawer; the rail stays. */
  .settings { position: fixed; z-index: 1; top: env(safe-area-inset-top); right: 0; left: calc(64px + env(safe-area-inset-left)); height: calc(var(--app-height, 100dvh) - env(safe-area-inset-top));
    display: grid; grid-template-columns: 220px minmax(0, 1fr); background: var(--bg); }
  .settings:focus-visible { outline: none; }
  header strong, header .count { font-size: 16px; }
  .nav { min-height: 0; }
  .nav nav { display: grid; gap: 2px; padding: 8px; }
  .nav a { display: flex; align-items: center; gap: 10px; height: 36px; padding: 0 10px; border-radius: 6px; color: var(--muted); font-size: 14px; font-weight: 500; text-decoration: none; }
  .nav a:hover { background: var(--hover); color: var(--text); }
  .nav a[aria-current="page"] { background: var(--selected); color: var(--text); }
  .pane { min-width: 0; min-height: 0; display: flex; flex-direction: column; }
  .round { display: grid; place-items: center; flex: none; width: 40px; height: 40px; margin: 0 -8px 0 -10px; padding: 0; border: 0; border-radius: 8px; background: none; color: var(--muted); cursor: pointer; }
  .menu { display: none; }
  .list { padding: 8px; }
  .list a { display: flex; align-items: center; gap: 10px; height: 44px; padding: 0 8px; border-radius: 6px; color: var(--text); font-size: 15px; font-weight: 500; text-decoration: none; }
  .list a :global(svg:last-child) { margin-left: auto; color: var(--muted); }
  @media (max-width: 640px) {
    .settings { left: env(safe-area-inset-left); grid-template-columns: minmax(0, 1fr); }
    .menu { display: grid; }
  }
</style>
