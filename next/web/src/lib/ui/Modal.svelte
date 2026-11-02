<script lang="ts">
  import { onMount, type Snippet } from "svelte";
  import Icon from "./Icon.svelte";
  import { onBackdrop, returnFocus } from "../modal.ts";
  // The one modal: a native dialog opened with showModal, so the browser traps focus and makes the app behind it inert.
  // Escape, the close button and a press on the backdrop close it, and focus goes back to the opener. Mount it to open it,
  // and unmount it in onclose. It has a title with an optional count, a body that scrolls on its own, and an optional row
  // of actions. Above 760 px it is a centred panel; below, a full-height sheet, or with `fit` a bottom sheet the height
  // of its content. While `busy`, nothing closes it. Keys stay inside: the app's shortcuts, and its Escape that closes a
  // sheet, wait until it has closed.
  let { title, count, onclose, children, actions, busy = false, fit = false, wide = false, opener, initial }: {
    title: string; count?: number | string; onclose: () => void; children: Snippet; actions?: Snippet;
    busy?: boolean; fit?: boolean; wide?: boolean; opener?: HTMLElement | null; initial?: string;
  } = $props();
  let dialog: HTMLDialogElement;
  let down: EventTarget | null = null;
  const id = $props.id();
  onMount(() => {
    // Both are read now: by the time the modal unmounts, the menu item and its caller's props may be gone.
    const before = document.activeElement instanceof HTMLElement ? document.activeElement : null, given = opener;
    dialog.showModal();
    // The browser focuses the first control, the close button; a confirm asks for Cancel instead.
    if (initial) dialog.querySelector<HTMLElement>(initial)?.focus();
    return () => { returnFocus(given, before); };
  });
  // A busy modal disables its buttons, and a disabled button drops focus to the page; the dialog takes it instead, so
  // keys still reach it.
  $effect(() => { if (busy && !dialog.contains(document.activeElement)) dialog.focus(); });
  // With a modal open, a key pressed while focus sits outside every open dialog belongs to no one behind it.
  function guard(event: KeyboardEvent) {
    if (dialog.open && !(event.target instanceof Element && event.target.closest("dialog[open]"))) event.stopPropagation();
  }
  // A second Escape with no click between closes a dialog even when the first was refused; a busy one opens again.
  function closed() {
    if (busy && dialog.isConnected) dialog.showModal();
    else onclose();
  }
</script>

<svelte:window onkeydowncapture={guard} />
<!-- svelte-ignore a11y_click_events_have_key_events a11y_no_noninteractive_element_interactions (Escape closes it natively; the click is the backdrop's) -->
<dialog class="modal" class:fit class:wide bind:this={dialog} tabindex="-1" aria-labelledby="{id}-title" aria-busy={busy || undefined}
  oncancel={event => { if (busy) event.preventDefault(); }} onclose={closed}
  onkeydown={event => event.stopPropagation()}
  onpointerdown={event => { down = event.target; }} onclick={event => { if (!busy && onBackdrop(dialog, down, event.target)) dialog.close(); }}>
  <header>
    <h2 id="{id}-title">{title}{#if count !== undefined}<span class="count">{count}</span>{/if}</h2>
    <button type="button" class="close" aria-label="Close" title="Close" disabled={busy} onclick={() => dialog.close()}><Icon name="close" /></button>
  </header>
  <div class="body">{@render children()}</div>
  {#if actions}<footer>{@render actions()}</footer>{/if}
</dialog>

<style>
  /* The dialog is the panel itself, so a click that lands on the dialog element landed on its backdrop. */
  .modal { box-sizing: border-box; width: min(560px, calc(100vw - 32px)); max-width: none; max-height: min(720px, calc(100dvh - 64px)); margin: auto; padding: 0;
    border: 1px solid var(--line); border-radius: 16px; background: var(--raised); color: var(--text); box-shadow: 0 24px 64px var(--shadow-lg);
    font-size: 14px; line-height: 1.45; overflow: hidden; overscroll-behavior: contain; }
  .modal.wide { width: min(1000px, calc(100vw - 32px)); }
  .modal[open] { display: flex; flex-direction: column; }
  .modal:focus-visible { outline: none; }
  .modal::backdrop { background: var(--scrim); }
  header { flex: none; display: flex; align-items: center; gap: 8px; min-height: 52px; padding: 0 8px 0 16px; border-bottom: 1px solid var(--line); }
  h2 { flex: 1; min-width: 0; margin: 0; font-size: 16px; font-weight: 600; line-height: 1.25; overflow-wrap: anywhere; }
  .count { margin-left: 8px; color: var(--muted); font-size: 13px; font-weight: 400; font-variant-numeric: tabular-nums; }
  .close { display: grid; place-items: center; flex: none; width: 32px; height: 32px; padding: 0; border: 0; border-radius: 6px; background: none; color: var(--muted); cursor: pointer; }
  .close:hover:enabled { background: var(--hover); color: var(--text); }
  .close:disabled { opacity: .45; cursor: default; }
  .close:focus-visible { outline: 2px solid var(--focus); outline-offset: -2px; }
  /* Only the body scrolls, and never sideways: long words and links wrap. */
  .body { flex: 1 1 auto; min-height: 0; padding: 12px 16px 16px; overflow-x: hidden; overflow-y: auto; overscroll-behavior: contain; overflow-wrap: anywhere; }
  footer { flex: none; display: flex; flex-wrap: wrap; justify-content: flex-end; gap: 8px; padding: 12px 16px; border-top: 1px solid var(--line); }
  @media (prefers-reduced-motion: no-preference) {
    .modal[open] { animation: modal-rise 200ms ease-out; }
    .modal[open]::backdrop { animation: modal-fade 200ms ease-out; }
  }
  @keyframes modal-rise { from { opacity: 0; transform: translateY(8px) scale(.99); } }
  @keyframes modal-fade { from { opacity: 0; } }
  /* The phone breakpoint, as the sessions panel's: the whole screen, its edges clear of the notch and the home bar. */
  @media (max-width: 760px) {
    .modal, .modal.wide { width: 100%; height: 100dvh; max-height: none; border: 0; border-radius: 0; box-shadow: none; }
    header { min-height: calc(52px + env(safe-area-inset-top)); padding: env(safe-area-inset-top) max(4px, env(safe-area-inset-right)) 0 max(16px, env(safe-area-inset-left)); }
    .close { width: 40px; height: 40px; }
    .body { padding-left: max(16px, env(safe-area-inset-left)); padding-right: max(16px, env(safe-area-inset-right)); }
    .body:last-child, footer { padding-bottom: max(16px, env(safe-area-inset-bottom)); }
    /* A short one, such as a confirm, docks to the bottom at its own height, within the thumb's reach. */
    .modal.fit { height: fit-content; max-height: calc(100dvh - 48px - env(safe-area-inset-top)); margin: auto 0 0; border-top: 1px solid var(--line); border-radius: 16px 16px 0 0; box-shadow: 0 -8px 32px var(--shadow-lg); }
    .modal.fit header { min-height: 52px; padding-top: 0; }
    @media (prefers-reduced-motion: no-preference) { .modal[open] { animation-name: modal-up; } }
  }
  @keyframes modal-up { from { opacity: 0; transform: translateY(24px); } }
</style>
