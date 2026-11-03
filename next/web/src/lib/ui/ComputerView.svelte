<script lang="ts">
  import { onMount, tick, type Snippet } from "svelte";
  import Icon from "./Icon.svelte";
  import Avatar from "./Avatar.svelte";
  import FloatWindow from "./FloatWindow.svelte";
  import PhoneKeys from "./PhoneKeys.svelte";
  import type { Rect } from "../float.ts";
  import { LiveStream, type Phase } from "../live.ts";
  import { PhoneKeys as PhoneState, doubleTap, escTwice, type KeyEvent, type Modifiers } from "../computerInput.ts";
  import { openStream, type DesktopStream } from "../computerStream.ts";
  import { softKeyboard } from "../softKeyboard.ts";
  // The session's desktop, live (spec 5.14). It floats as a window over the app at the desktop's shape, scaled to fit, on
  // every display; the desktop never resizes for it. A double tap or double click makes it full screen and gives the owner
  // control, as v1's Take control did; leaving full screen gives control back. Full screen has v1's chat bar along the
  // bottom, which is the app's own composer for the session's DM (chat), so the owner can message the agent while watching.
  // The rect belongs to the app, so the window keeps its place and size for the page view. onfit gives the desktop's ratio
  // and the height around the screen once it shows, so a new window can show no black bars. start starts the desktop; the
  // view runs it before each connection, so Try again also starts a desktop that stopped.
  let { session, name, root, activity, full = $bindable(false), rect = $bindable(), element = $bindable(), start, chat, onclose, onfit }: {
    session: string; name: string; root: boolean; activity?: string; full?: boolean; rect: Rect; element?: HTMLElement;
    start: () => Promise<void>; chat?: Snippet; onclose: (tapped: boolean) => void; onfit: (ratio: number, chrome: number) => void;
  } = $props();
  let screen: HTMLElement | undefined = $state();
  let visible = $state(document.visibilityState === "visible");
  // A stopped computer closes this view, so a failure here is a desktop that did not start or a stream that broke. Try
  // again or the page becoming visible again tries once more; nothing retries on a timer.
  let phase: Phase = $state("connecting");
  let failure = $state("");
  const live = new LiveStream(next => { phase = next; });
  let attempt = $state(0);
  let desktop: DesktopStream | undefined;
  const url = () => new URL(`/v1/computers/stream?session=${encodeURIComponent(session)}`, location.href.replace(/^http/, "ws")).href;
  // noVNC fills node with the desktop. The stream lives only while the owner can see it: this view open, the page visible.
  function connect(node: HTMLElement) {
    const { frame, fail } = live.start();
    let gone = false;
    let stream: DesktopStream | undefined;
    void start().then(() => {
      if (gone) return;
      stream = desktop = openStream(node, url(), {
        live: (width, height) => {
          frame();
          stream?.control(full);
          if (!full && element && screen && width && height) onfit(width / height, element.offsetHeight - screen.offsetHeight);
        },
        ended: () => { failure = "The live view stopped."; fail(); },
      });
    }, () => { if (!gone) { failure = "The desktop did not start."; fail(); } });
    return () => { gone = true; live.stop(); stream?.close(); if (desktop === stream) desktop = undefined; };
  }
  onMount(() => { element?.focus(); });

  // Full screen is control: noVNC takes the keyboard and pointer, and owner input never pauses the agent.
  let keyboardOpen = $state(false);
  let held: Modifiers = $state({ ctrl: false, alt: false });
  let field: HTMLTextAreaElement | undefined = $state();
  const phoneKeys = new PhoneState(state => { held = state; });
  const send = (keys: KeyEvent[]) => { for (const key of keys) desktop?.key(key.keysym, key.down); };
  async function fullScreen(on: boolean) {
    full = on;
    keyboardOpen = false;
    field?.blur();
    desktop?.control(on);
    await tick();
    if (!on) element?.focus();
  }
  // A double tap or click on the floating screen goes full screen. Full screen, every tap and click is the desktop's.
  const tapped = doubleTap();
  function press(event: PointerEvent) {
    if (!full && event.isPrimary && tapped(event.timeStamp, event.clientX, event.clientY)) {
      event.preventDefault();
      void fullScreen(true);
    }
  }
  // Escape closes the floating view and leaves full screen. On the desktop itself, Esc goes to the computer, and Esc twice
  // leaves full screen, as v1's Esc twice returned control.
  function key(event: KeyboardEvent) {
    if (event.key !== "Escape") return;
    event.stopPropagation();
    if (full) void fullScreen(false); else onclose(false);
  }
  const twice = escTwice();
  function desktopKey(event: KeyboardEvent) {
    if (full && event.key === "Escape" && !event.repeat && screen?.contains(event.target as Node) && twice(event.timeStamp)) {
      event.stopPropagation();
      event.preventDefault();
      void fullScreen(false);
    }
  }
  function toggleKeyboard() {
    keyboardOpen = !keyboardOpen;
    if (keyboardOpen) field?.focus({ preventScroll: true });
    else field?.blur();
  }
</script>

<svelte:document onvisibilitychange={() => { visible = document.visibilityState === "visible"; }} />
<FloatWindow label={`${name}'s computer`} kind="computer" docked={false} {full} bind:rect bind:element onkeydown={key}>
  {#snippet header()}<Avatar kind="session" {name} {root} size={20} /><span class="title">{name}'s computer</span>
    {#if phase === "live"}<span class="live">Live</span>{:else if phase === "connecting"}<span class="connecting">Connecting</span>{/if}
    <button type="button" class="first" aria-label={full ? "Leave full screen" : "Full screen and control"} title={full ? "Leave full screen" : "Full screen and control"}
      onclick={() => void fullScreen(!full)}><Icon name={full ? "restore" : "maximize"} /></button>
    <button type="button" aria-label="Close" title="Close" onclick={event => onclose(event.detail > 0)}><Icon name="close" /></button>{/snippet}
  <!-- While full screen, the desktop takes the keyboard and the pointer; its outline says so. -->
  <!-- svelte-ignore a11y_no_static_element_interactions (a double tap is the pointer's way to full screen; the title bar's button is the keyboard's) -->
  <div class="screen" class:full bind:this={screen} onpointerdowncapture={press} onkeydowncapture={desktopKey}>
    {#if visible}{#key attempt}<div class="desktop" role="img" aria-label={full ? `${name}'s screen, in your control` : `${name}'s screen, live`} {@attach connect}></div>{/key}{/if}
    {#if phase === "failed"}<p class="badge" role="status">{failure} <button type="button" class="retry" onclick={() => { attempt++; }}>Try again</button></p>{/if}
  </div>
  {#if full}
    <div class="chat">
      <div class="line"><span class="step">{activity ? `${name} · ${activity}` : name}</span><span class="hint"> Esc twice leaves full screen.</span>
        <button type="button" class="tool" class:on={keyboardOpen} aria-label="Type on the computer" aria-pressed={keyboardOpen} title="Type on the computer"
          onpointerdown={event => event.preventDefault()} onclick={toggleKeyboard}><Icon name="keyboard" size={18} /></button></div>
      {#if keyboardOpen}<PhoneKeys {held} onkey={key => { send(phoneKeys.press(key)); field?.focus({ preventScroll: true }); }} />{/if}
      {@render chat?.()}
    </div>
    <textarea class="field" aria-label="Type on the computer" bind:this={field} {@attach node => softKeyboard(node, phoneKeys, send)} onblur={() => { keyboardOpen = false; }} autocapitalize="off" autocomplete="off" spellcheck="false" rows="1"></textarea>
  {:else}
    <!-- The current step, one muted line under the screen: "qa-runner · reading the review state". -->
    <div class="activity"><span class="step">{activity ? `${name} · ${activity}` : name}</span><span class="hint">Double-click to take control</span></div>
  {/if}
</FloatWindow>

<style>
  .title { flex: none; }
  /* A neutral outline label, the window's only uppercase text; it shows only once the desktop shows. */
  .live { padding: 0 5px; border: 1px solid var(--line); border-radius: 4px; color: var(--muted); font-size: 11px; font-weight: 600; letter-spacing: 0.06em; text-transform: uppercase; }
  .connecting { color: var(--muted); font-weight: 400; }
  .first { margin-left: auto; }
  /* Full screen, the whole window is one near-black surface, as the screen is, from the notch to the home bar; the
     --screen tokens keep its text, buttons, avatar and composer legible in both themes. */
  :global(.window.computer.full) { --text: var(--screen-text); --muted: var(--screen-muted); --line: var(--screen-line); --hover: var(--screen-hover); --focus: var(--screen-focus); --raised: var(--screen);
    --id-face: var(--screen-id-face); --id-alpha: var(--screen-id-alpha); --id-initials: var(--screen-id-initials); background: var(--screen); }
  /* The screen is near-black in both themes, as a display is. It fills the window body, and noVNC scales the desktop to
     fit inside it at its own shape. */
  .screen { position: relative; display: flex; flex: 1; min-height: 0; overflow: hidden; background: var(--screen); }
  .desktop { flex: 1; min-width: 0; min-height: 0; }
  /* The frame is drawn over the picture, which would cover an outline. */
  .screen.full::after { content: ""; position: absolute; inset: 0; border: 2px solid var(--focus); pointer-events: none; }
  /* v1's badge: a dark chip over the last picture, readable on any frame. */
  .badge { position: absolute; left: 12px; top: 12px; margin: 0; padding: 3px 11px; border-radius: 12px; background: var(--screen-chip); color: var(--screen-text); font-size: 12px; }
  .badge .retry { margin-left: 4px; color: var(--screen-text); }
  .activity, .line { display: flex; flex: none; align-items: center; gap: 8px; min-height: 36px; padding: 0 12px; color: var(--muted); font-size: 12.5px; }
  .step { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .hint { flex: none; }
  .line .hint { margin-left: auto; }
  .line .step { flex: 0 1 auto; }
  /* The phone shows the key row's Esc; a short window has no room for the hint. */
  @media (hover: none) { .hint { display: none; } .line .tool { margin-left: auto; } }
  /* v1's chat bar along the bottom: the step above the session's composer. */
  .chat { display: flex; flex: none; flex-direction: column; border-top: 1px solid var(--line); }
  .tool { display: grid; flex: none; place-items: center; width: 28px; height: 28px; padding: 0; border: 0; border-radius: 6px; background: none; color: var(--muted); cursor: pointer; }
  .tool:hover, .tool.on { background: var(--hover); color: var(--text); }
  .tool:focus-visible { outline: 2px solid var(--focus); outline-offset: 1px; }
  @media (hover: none) { .activity, .line { min-height: 44px; } .tool { width: 40px; height: 40px; } }
  /* The field that takes the phone keyboard's text: on the page and focusable, but invisible. */
  .field { position: fixed; left: 0; bottom: 0; width: 1px; height: 1px; padding: 0; border: 0; opacity: 0; font-size: 16px; pointer-events: none; }
</style>
