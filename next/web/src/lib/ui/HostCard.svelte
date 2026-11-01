<script lang="ts">
  import { MediaQuery } from "svelte/reactivity";
  import type { HostSummary } from "../contract.gen.ts";
  import { boxWords, count } from "../feed.ts";
  import { backupProblem, hang, hostLabel, hostMeters, largeShared, lastSeen, roomSessions, sharedSize } from "../hosts.ts";
  import Avatar from "./Avatar.svelte";
  import MeterTile from "./MeterTile.svelte";
  import StatusPill from "./StatusPill.svelte";
  // One machine on Home: the name and the workspace count, then CPU, memory and disk tiles. A healthy host shows no
  // status. A host low on disk, as the engine judges it, shows the warning pill. An unreachable one shows the warning pill and its last figures dimmed with their age, or no figures if
  // it never answered. The control plane has no workspaces. A backup problem shows its pill; a healthy backup shows nothing.
  // A room's shared folder past a quarter of the disk is a muted line under the tiles: it needs cleaning, not a decision.
  // The workspace count opens the machine's sessions by room, as Jobs does: a popover, a sheet on a phone. Workspaces
  // count running boxes and the list also holds asleep ones, so its rows can outnumber the count. With none it is plain text.
  // The list sits in the top layer: a card is a size container, and Home's scroll area would clip a popover in its first column.
  // That layer gives Escape, the click away and focus's return. Its backdrop lets taps through, so a phone's scrim is the popover itself.
  let { host, now, roomName, navigate }: { host: HostSummary; now: number; roomName: (room: string) => string; navigate: (event: MouseEvent & { currentTarget: HTMLAnchorElement }) => void } = $props();
  let seen = $derived(host.reachable ? undefined : lastSeen(host, now));
  let backup = $derived(backupProblem(host, now));
  let groups = $derived(roomSessions(host, roomName));
  let listed = $derived(host.reachable && host.boxes > 0 && groups.length > 0);
  const phone = new MediaQuery("max-width: 640px");
  let button: HTMLButtonElement | undefined = $state();
  let open = $state(false);
  let place = $state("");
  function placed() { if (button) place = hang(button.getBoundingClientRect(), document.documentElement.clientWidth, document.documentElement.clientHeight); }
  function toggled(event: ToggleEvent & { currentTarget: HTMLElement }) {
    open = event.newState === "open";
    if (open) event.currentTarget.querySelector("a")?.focus();
  }
</script>

<section class="card" aria-label={hostLabel(host.name)}>
  <h3>{hostLabel(host.name)}
    <span class="aside state">{#if backup}<StatusPill tone={backup.tone} label={backup.label} title={backup.title} />{/if}
      {#if host.diskLow}<StatusPill tone="warning" label="Disk low" />{/if}
      {#if !host.reachable}<StatusPill tone="error" label="Unreachable" />
      {:else if listed}<button type="button" class="count" bind:this={button} popovertarget={`sessions-${host.name}`} aria-haspopup="dialog" aria-expanded={open}>{count(host.boxes, "workspace")}</button>
      {:else if host.name !== "control-plane"}{count(host.boxes, "workspace")}{/if}</span></h3>
  {#if host.reachable || seen}<div class="tiles" class:dimmed={!host.reachable}>{#each hostMeters(host) as meter (meter.label)}<MeterTile {meter} />{/each}</div>{/if}
  {#if seen}<p class="aside seen">{seen}</p>{/if}
  {#each largeShared(host) as folder (folder.room)}<p class="aside shared">Shared folder of {roomName(folder.room)}: {sharedSize(folder.bytes)}</p>{/each}
  {#if listed}
    <!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions (Escape closes it; a tap on the scrim has no key) -->
    <div class="veil" class:phone={phone.current} id={`sessions-${host.name}`} popover="auto" onbeforetoggle={placed} ontoggle={toggled}
      onclick={event => { if (event.target === event.currentTarget) event.currentTarget.hidePopover(); }}>
      <div class="pop" class:sheet={phone.current} style={phone.current ? undefined : place} role="dialog" aria-label={`Sessions on ${hostLabel(host.name)}`}>
        {#if phone.current}<span class="grab"></span>{/if}
        <strong class="title">{hostLabel(host.name)}</strong>
        <div class="list">
          {#each groups as group (group.room)}
            <p class="room"><Avatar kind="room" name={group.name} size={16} />{group.name}</p>
            {#each group.sessions as session (session.id)}
              <a class="row" class:quiet={session.boxState === "stopped"} href={`/r/${encodeURIComponent(group.room)}/s/${encodeURIComponent(session.id)}`} onclick={navigate}>
                <span class="name">{session.name}</span>{#if session.boxState !== "ready"}<span class="word">{boxWords[session.boxState]}</span>{/if}</a>
            {/each}
          {/each}
        </div>
      </div>
    </div>
  {/if}
</section>

<style>
  .state { display: flex; flex-wrap: wrap; justify-content: flex-end; align-items: center; gap: 2px 8px; }
  .dimmed { opacity: 0.4; }
  .seen, .shared { margin: 10px 0 0; }
  /* The wash reaches past the words, and the margin takes it back, so the count stays where plain text sits. */
  .count { all: unset; position: relative; margin: -2px -6px; padding: 2px 6px; border-radius: 6px; cursor: pointer; }
  .count[aria-expanded="true"] { background: var(--hover); color: var(--text); }
  .count:focus-visible { outline: 2px solid var(--focus); }
  @media (hover: hover) { .count:hover { background: var(--hover); color: var(--text); } }
  @media (hover: none) { .count::after { content: ""; position: absolute; inset: -10px 0; } }
  /* A desktop's popover hangs from the button, so this holds nothing. A phone's fills the screen as the sheet's scrim. */
  .veil { inset: auto; width: 0; height: 0; margin: 0; padding: 0; border: 0; background: none; color: var(--text); overflow: visible; }
  .veil.phone { inset: 0; width: auto; height: auto; max-width: none; max-height: none; background: var(--scrim); }
  .pop:not(.sheet) { position: fixed; width: min(360px, calc(100vw - 24px)); }
  .title { flex: none; padding: 12px 14px 6px; font-size: 16px; }
  .sheet .title { padding: 6px 16px 8px; font-size: 18px; }
  .list { min-height: 0; overflow-y: auto; padding: 0 6px 6px; }
  .room { display: flex; align-items: center; gap: 6px; margin: 10px 10px 2px; color: var(--muted); font-size: 12.5px; font-weight: 600; }
  .room:first-child { margin-top: 4px; }
  .row { display: flex; align-items: center; gap: 10px; min-height: 32px; padding: 4px 10px; border-radius: 6px; color: var(--text); font-size: 13px; font-weight: 500; text-decoration: none; }
  .row:hover, .row:focus-visible { background: var(--selected); }
  .row:focus-visible { outline: 2px solid var(--focus); outline-offset: -2px; }
  .quiet { color: var(--muted); font-weight: 400; }
  .name { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .word { flex: none; color: var(--muted); font-size: 12.5px; }
  @media (hover: none) { .row { min-height: 44px; } }
</style>
