<script lang="ts">
  import { untrack, type Snippet } from "svelte";
  import { APIError, call, SignedOut } from "../client.ts";
  import Blocked from "./Blocked.svelte";
  import AppearancePane from "./AppearancePane.svelte";
  import SharingPane from "./SharingPane.svelte";
  import AccountsPane from "./AccountsPane.svelte";
  import ProfilesPane from "./ProfilesPane.svelte";
  import AddonsPane from "./AddonsPane.svelte";
  import ApprovalsPane from "./ApprovalsPane.svelte";
  import HostsPane from "./HostsPane.svelte";
  import PoolsPane from "./PoolsPane.svelte";
  import ImagesPane from "./ImagesPane.svelte";
  import Icon from "./Icon.svelte";
  import { pathItem, settingsPanes } from "../routes.ts";
  import type { Device } from "../contract.gen.ts";
  // The Settings page, in the feed's and the sidebar's place. It opens over the app, so the room behind keeps its feed,
  // its position and its draft. A wide page opens the first pane. A phone opens the pane list, and a pane's arrow returns to it.
  // moved tells the app that Settings changed the path itself, as a list page does when it opens an item.
  // counts are the item counts the nav shows beside list panes, from lists the app already holds.
  let { path, phone, counts, navigate, moved, onmenu, signedout, thishost, element = $bindable(), menu = $bindable() }: { path: string; phone: boolean; counts: Partial<Record<string, number>>; navigate: (event: MouseEvent & { currentTarget: HTMLAnchorElement }) => void;
    moved: () => void; onmenu: (event: MouseEvent) => void; signedout: () => void; thishost: (host?: string) => void; element?: HTMLElement; menu?: HTMLButtonElement } = $props();

  // device.list needs admin, so its refusal tells that this device has no Sharing & access pane (spec 7.8). reload
  // resolves once the list is read or refused.
  let devices: Device[] | undefined = $state.raw();
  let admin = $state(true), failed = $state(false);
  function reload() {
    return call("device.list", {}).then(list => { devices = list.devices ?? []; thishost(devices.find(device => device.current)?.host); admin = true; failed = false; }, (error: unknown) => {
      if (error instanceof SignedOut) signedout();
      else if (error instanceof APIError && error.detail.code === "forbidden") admin = false;
      // A failed poll keeps the list and the pair card on screen.
      else if (!devices) failed = true;
    });
  }
  untrack(reload);

  // Appearance is on every device; Devices only on one with admin.
  let panes = $derived(settingsPanes.filter(pane => !pane.admin || admin));
  let shown = $derived(panes.find(pane => path === `/settings/${pane.id}` || (pane.list && path.startsWith(`/settings/${pane.id}/`))) ?? (phone ? undefined : panes[0]));
  // /settings and its trailing slash open the first pane or the list; any other path has no page.
  // A part that does not decode has no page.
  let lost = $derived(!/^\/settings\/?$/.test(path) && !panes.some(pane => path === `/settings/${pane.id}` || (pane.list && pathItem(path, `/settings/${pane.id}/`) !== undefined)));

  // A list page's open item is the path's last part (spec 5.13): a wide page moves it as the list's keys move, in place,
  // and a narrow one opens it as a screen of its own, so Back returns to the list.
  let item = $derived(shown?.list && !lost ? pathItem(path, `/settings/${shown.id}/`) : undefined);
  function open(id: string, screen: boolean) {
    const href = `/settings/${shown!.id}/${encodeURIComponent(id)}`;
    if (href === path) return;
    if (screen) history.pushState({ fromList: true }, "", href);
    else history.replaceState(history.state, "", href);
    moved();
  }
  function close() {
    if (history.state?.fromList) { history.back(); return; }
    history.replaceState(null, "", `/settings/${shown!.id}`);
    moved();
  }
  // The tab names the open pane, or Settings on the pane list. Settings owns the title while it is open.
  $effect(() => { document.title = `${lost ? "Not found" : shown?.label ?? "Settings"} · Dyna`; });
</script>

{#snippet bar(title: string, count?: number, action?: Snippet)}
  <header class="bar">{#if phone}<a class="round" href="/settings" onclick={navigate} aria-label="Back to Settings" title="Back to Settings"><Icon name="backChevron" /></a>{/if}
    <strong>{title}</strong>{#if count !== undefined}<span class="count">{count.toLocaleString("en")}</span>{/if}{@render action?.()}</header>
{/snippet}

<section class="settings" aria-label="Settings" tabindex="-1" bind:this={element}>
  <div class="settings-layout">
  {#if !phone}<aside class="nav"><header class="bar"><strong>Settings</strong></header>
    <nav aria-label="Settings panes">{#each panes as pane (pane.id)}
      <a href={`/settings/${pane.id}`} onclick={navigate} class:sep={pane.sep} aria-current={shown === pane ? "page" : undefined}><Icon name={pane.icon} /><span class="l">{pane.label}</span>{#if counts[pane.id] !== undefined}<span class="n">{counts[pane.id]!.toLocaleString("en")}</span>{/if}</a>{/each}</nav></aside>{/if}
  <div class="pane">
    {#if lost}<Blocked kind="no-page" back={{ href: "/settings", label: "Back to Settings" }} {navigate} />
    {:else if shown?.id === "appearance"}<AppearancePane {bar} />
    {:else if shown?.id === "devices"}<SharingPane {bar} {phone} {devices} {failed} {reload} {signedout} selected={item} onselect={open} onclose={close} />
    {:else if shown?.id === "accounts"}<AccountsPane {bar} {phone} {signedout} selected={item} onselect={open} onclose={close} />
    {:else if shown?.id === "profiles"}<ProfilesPane {bar} {phone} {signedout} selected={item} onselect={open} onclose={close} />
    {:else if shown?.id === "addons"}<AddonsPane {bar} {admin} {signedout} selected={item} onselect={open} onclose={close} />
    {:else if shown?.id === "approvals"}<ApprovalsPane {bar} {signedout} selected={item} onselect={open} onclose={close} />
    {:else if shown?.id === "hosts"}<HostsPane {bar} {phone} {navigate} {signedout} selected={item} onselect={open} onclose={close} />
    {:else if shown?.id === "pools"}<PoolsPane {bar} {phone} {navigate} {signedout} selected={item} onselect={open} onclose={close} />
    {:else if shown?.id === "images"}<ImagesPane {bar} {signedout} selected={item} onselect={open} onclose={close} />
    {:else}
      <header class="bar"><button type="button" class="round menu" bind:this={menu} aria-label="Rooms and pages" title="Rooms and pages" onclick={onmenu}><Icon name="menu" /></button><strong>Settings</strong></header>
      <nav class="list" aria-label="Settings panes">{#each panes as pane (pane.id)}
        <a href={`/settings/${pane.id}`} onclick={navigate} class:sep={pane.sep}><Icon name={pane.icon} />{pane.label}{#if counts[pane.id] !== undefined}<span class="n">{counts[pane.id]!.toLocaleString("en")}</span>{/if}<Icon name="forwardChevron" /></a>{/each}</nav>
    {/if}
  </div>
  </div>
</section>

<style>
  /* Over the room column and the sessions panel, under the phone's drawer; the rail stays. */
  .settings { position: fixed; z-index: 1; top: env(safe-area-inset-top); right: 0; left: calc(64px + env(safe-area-inset-left)); height: calc(var(--app-height, 100dvh) - env(safe-area-inset-top));
    container: settings / inline-size; background: var(--bg); }
  .settings-layout { display: grid; grid-template-columns: 220px minmax(0, 1fr); height: 100%; }
  .settings:focus-visible { outline: none; }
  header strong, header .count { font-size: 16px; }
  .nav { min-height: 0; }
  .nav nav { display: grid; gap: 2px; padding: 8px; }
  .nav a { display: flex; align-items: center; gap: 10px; height: 36px; padding: 0 10px; border-radius: 6px; color: var(--muted); font-size: 14px; font-weight: 500; text-decoration: none; }
  .nav a:hover { background: var(--hover); color: var(--text); }
  .nav a[aria-current="page"] { background: var(--selected); color: var(--text); }
  /* The line sits in the gap above the pane, so the panes keep their places. */
  .nav a.sep, .list a.sep { position: relative; margin-top: 13px; }
  .nav a.sep::before, .list a.sep::before { content: ""; position: absolute; top: -8px; right: 10px; left: 10px; border-top: 1px solid var(--line); }
  .nav a .l { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .nav a .n { margin-left: auto; color: var(--muted); font-size: 12.5px; font-variant-numeric: tabular-nums; }
  .pane { min-width: 0; min-height: 0; display: flex; flex-direction: column; }
  .round { display: grid; place-items: center; flex: none; width: 40px; height: 40px; margin: 0 -8px 0 -10px; padding: 0; border: 0; border-radius: 8px; background: none; color: var(--muted); cursor: pointer; }
  .menu { display: none; }
  .list { padding: 8px; }
  .list a { display: flex; align-items: center; gap: 10px; height: 44px; padding: 0 8px; border-radius: 6px; color: var(--text); font-size: 15px; font-weight: 500; text-decoration: none; }
  .list a :global(svg:last-child) { margin-left: auto; color: var(--muted); }
  .list a .n { margin-left: auto; color: var(--muted); font-size: 13px; font-variant-numeric: tabular-nums; }
  .list a .n + :global(svg) { margin-left: 0; }
  @container settings (max-width: 720px) {
    .settings-layout { grid-template-columns: minmax(0, 1fr); grid-template-rows: auto minmax(0, 1fr); }
    .nav nav { display: flex; overflow-x: auto; }
    .nav a { flex: none; }
    .nav a.sep { margin-top: 0; }
    .nav a.sep::before { display: none; }
  }
  @media (max-width: 640px) {
    .settings { left: env(safe-area-inset-left); }
    .settings-layout { grid-template-columns: minmax(0, 1fr); }
    .menu { display: grid; }
  }
</style>
