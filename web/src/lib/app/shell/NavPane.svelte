<script lang="ts">
  import RoomRail from '../../rooms/RoomRail.svelte'
  import Sidebar from '../../rooms/Sidebar.svelte'
  import { dialog } from '../../ui/dialog.ts'
  import type { ComponentProps } from 'svelte'
  import type { Room } from '../../api/room.ts'
  // The room rail and the sidebar beside it. On a wide screen this wrapper adds
  // no box; on a narrow one it is the drawer that slides in from the left.
  // App keeps the open room and the drawer state, and builds the sidebar's own
  // props once, so no value is held twice.
  interface Props {
    /** The phone drawer is open. */
    open: boolean
    narrow: boolean
    /** Another phone sheet owns focus. */
    suspended?: boolean
    /** The sidebar shows beside the rail; a phone always shows it in the drawer. */
    showSide: boolean
    rooms: Room[]
    /** The open room's ID, or '' when the rail's machines tile is open. */
    selected: string
    onselect: (id: string) => void
    onclose: () => void
    onmenu: ((room: Room, x: number, y: number, anchor?: HTMLElement) => void) | null
    /** An update is ready; the settings button shows a dot. */
    update: boolean
    onsettings: () => void
    onlogout: () => void
    sidebar: ComponentProps<typeof Sidebar>
  }
  let { open, narrow, suspended = false, showSide, rooms, selected, onselect, onclose, onmenu, update, onsettings, onlogout, sidebar }: Props = $props()
  let modal = $derived(narrow && open && !suspended)
</script>

<!-- The wrapper is a dialog only while the phone drawer owns focus. -->
<!-- svelte-ignore a11y_no_noninteractive_tabindex -->
<div class="nav" class:open={open && !suspended} inert={narrow && !modal} use:dialog={{ active: modal, close: onclose }} role={modal ? 'dialog' : undefined} aria-modal={modal ? 'true' : undefined} aria-label={modal ? 'Rooms and views' : undefined} tabindex={modal ? -1 : undefined}>
  <RoomRail {narrow} {rooms} {selected} {onselect} {onmenu} {update} {onsettings} {onlogout} />
  {#if showSide || narrow}
    <Sidebar {...sidebar} />
  {/if}
</div>

<style>
  .nav { display: contents; }

  /* A phone: full room rows and the selected room's views share one drawer. */
  @media (max-width: 760px) {
    .nav {
      display: flex;
      flex-direction: column;
      position: fixed;
      z-index: 50;
      top: 0;
      bottom: 0;
      left: 0;
      width: min(320px, calc(100vw - 48px));
      max-width: calc(100vw - 48px);
      overflow: hidden;
      padding-top: env(safe-area-inset-top);
      padding-bottom: env(safe-area-inset-bottom);
      background: var(--side);
      transform: translateX(-105%);
      transition: transform var(--motion) ease-out;
      box-shadow: 4px 0 24px rgba(0, 0, 0, 0.45);
    }
    .nav.open { transform: none; }
    .nav > :global(.side) { width: 100%; flex: 1 1 55%; border-top: 1px solid var(--line-soft); }
  }
</style>
