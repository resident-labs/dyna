<script lang="ts" module>
  // Every image mount gets its own stream URL. Chromium can show a reused multipart URL from its image cache
  // without a new request: a frozen frame under "Live" after a retry, the page showing again, or the view reopening.
  let opened = 0;
</script>

<script lang="ts">
  import { onMount, tick } from "svelte";
  import Icon from "./Icon.svelte";
  import Avatar from "./Avatar.svelte";
  import StatusPill from "./StatusPill.svelte";
  import FloatWindow from "./FloatWindow.svelte";
  import { shortPaths } from "../feed.ts";
  import type { Rect } from "../float.ts";
  import PhoneKeys from "./PhoneKeys.svelte";
  import { LiveStream, type Phase } from "../live.ts";
  import { Finger, InputQueue, MouseHold, PhoneKeys as PhoneState, buttonOf, escTwice, keyboardInput, pictureScale, screenPoint, type ComputerInput, type Modifiers, type Size } from "../computerInput.ts";
  import { softKeyboard } from "../softKeyboard.ts";
  // Floating, the view is a window over the app; docked, it sits above the session list in the sessions panel. The
  // rect belongs to the app, so the window keeps its place and size for the page view.
  // onfit gives each frame's ratio and the height around the screen, so a floating window can show no black bars.
  // At full screen, the steps before the current one read below the screen, newest first.
  // input sends the owner's keys and pointer to the computer. A device that may not control computers passes none,
  // and the view then has no Take control step.
  let { session, name, root, activity, steps, docked, full = false, rect = $bindable(), element = $bindable(), input, onclose, ondock, onfit }: {
    session: string; name: string; root: boolean; activity?: string; steps: string[]; docked: boolean; full?: boolean; rect: Rect; element?: HTMLElement; input?: (events: ComputerInput[], frame: Size) => Promise<void>; onclose: (tapped: boolean) => void; ondock: (event: MouseEvent) => void;
    onfit: (ratio: number, chrome: number) => void;
  } = $props();
  let screen: HTMLElement | undefined = $state();
  let earlier = $derived(full ? steps.slice(-6, -1).reverse() : []);
  // The line under the screen: the current step and, where the owner may control the computer, its control step.
  let foot = $derived(!!input || !!activity || (!docked && !full));
  let visible = $state(document.visibilityState === "visible");
  // A stopped computer closes this view, so a failure here is a broken or silent stream. The owner's retry or the
  // page becoming visible again tries once more; nothing retries on a timer.
  let phase: Phase = $state("connecting");
  const stream = new LiveStream(next => { phase = next; });
  // The screen's shape, from the first frame; 16:9 until then.
  let ratio = $state(16 / 9);
  // Chromium may keep a multipart request open until a removed <img> is garbage-collected, so the
  // image drops its stream before it leaves. The load or error that the clearing fires belongs to a stopped stream,
  // so the view ignores it. A failed image stays, so the browser keeps showing the last frame under the notice, as in v1.
  let picture: HTMLImageElement | undefined;
  function watch(image: HTMLImageElement) {
    picture = image;
    const { frame, fail } = stream.start();
    image.src = `/v1/computers/watch?session=${encodeURIComponent(session)}&n=${++opened}`;
    image.addEventListener("load", () => { frame(); if (image.naturalHeight > 1) {
      ratio = image.naturalWidth / image.naturalHeight;
      if (!docked && !full && element && screen) onfit(ratio, element.offsetHeight - screen.offsetHeight);
    } });
    image.addEventListener("error", fail);
    return () => { stream.stop(); image.src = "data:image/gif;base64,R0lGODlhAQABAAAAACH5BAEKAAEALAAAAAABAAEAAAICTAEAOw=="; };
  }
  // The view takes focus when it opens.
  onMount(() => { element?.focus(); });
  // Escape closes.
  function key(event: KeyboardEvent) {
    if (event.key === "Escape") { event.stopPropagation(); onclose(false); }
  }
  // Try again loads a new stream: the key remounts the image.
  let attempt = $state(0);

  // Control is the owner's, on purpose: nothing leaves the page until Take control, and it ends with Return control,
  // Esc twice (the first Esc still reaches the computer, as in v1), or a stream that is no longer live.
  let control = $state(false);
  let keyboardOpen = $state(false);
  let held: Modifiers = $state({ ctrl: false, alt: false });
  let field: HTMLTextAreaElement | undefined = $state();
  const phoneKeys = new PhoneState(state => { held = state; });
  const twice = escTwice();
  // A batch goes with the size of the picture the owner sees, which its positions are in. A batch the engine refuses
  // shows as a notice under the screen with Retry. It stays until Retry or Return control, because the refused batch may
  // hold a key up or a mouse up, and a later batch that lands does not send it.
  let stuck = $state(false);
  const queue = new InputQueue(async (events, frame) => { await input?.(events, frame); }, ok => { if (!ok && control) stuck = true; });
  // While a refused batch waits, moves and scrolls are dropped, so Retry does not replay a stream of them.
  const send = (events: ComputerInput[]) => {
    if (stuck) events = events.filter(event => event.kind !== "mouse.move" && event.kind !== "wheel");
    if (input && picture && events.length) queue.push(events, { width: picture.naturalWidth, height: picture.naturalHeight });
  };
  async function take() { control = true; await tick(); screen?.focus(); }
  const hold = new MouseHold();
  // Control ends or the view closes: held buttons go up, even behind a refused batch.
  const drop = () => queue.finish(hold.release());
  function release() { drop(); control = false; stuck = false; keyboardOpen = false; field?.blur(); }
  $effect(() => { if (phase !== "live") release(); });
  function toggleKeyboard() {
    keyboardOpen = !keyboardOpen;
    if (keyboardOpen) field?.focus({ preventScroll: true });
    else field?.blur();
  }
  // A hardware key goes to the computer while the owner controls it, Esc and Alt with an arrow key included.
  function press(event: KeyboardEvent, down: boolean) {
    if (!control) return;
    event.stopPropagation();
    event.preventDefault();
    if (down && event.key === "Escape" && twice(performance.now())) { release(); return; }
    void send(keyboardInput(event, down));
  }
  // The pointer: a mouse is sent as it moves, once per frame; a finger taps or scrolls. The picture sits inside the
  // screen at its own shape, so a pointer on a bar lands nowhere.
  function pointing(node: HTMLElement) {
    const finger = new Finger();
    let pending: ComputerInput | undefined;
    let frame = 0;
    const size = () => picture && { width: picture.naturalWidth, height: picture.naturalHeight };
    const at = (event: MouseEvent) => { const shape = size(); return picture && shape && screenPoint(picture.getBoundingClientRect(), shape, event.clientX, event.clientY); };
    const scale = () => { const shape = size(); return picture && shape ? pictureScale(picture.getBoundingClientRect(), shape) : 1; };
    const touching = (event: PointerEvent) => event.pointerType === "touch";
    const down = (event: PointerEvent) => {
      if (!control) return;
      node.focus();
      node.setPointerCapture(event.pointerId);
      const point = at(event);
      if (!point) return;
      if (touching(event)) finger.down({ x: event.clientX, y: event.clientY }, point);
      else {
        const button = buttonOf(event.button);
        hold.down(button, point);
        send([{ kind: "mouse.move", ...point }, { kind: "mouse.down", ...point, button }]);
      }
    };
    // A mouse moves at most once per frame: one batch of the latest position.
    const move = (event: PointerEvent) => {
      if (!control) return;
      const point = at(event);
      if (!point) return;
      if (touching(event)) { void send(finger.move({ x: event.clientX, y: event.clientY }, point, scale())); return; }
      hold.move(point);
      pending = { kind: "mouse.move", ...point };
      frame ||= requestAnimationFrame(() => { frame = 0; if (pending) void send([pending]); pending = undefined; });
    };
    const up = (event: PointerEvent) => {
      if (!control) return;
      if (touching(event)) { void send(finger.up()); return; }
      // A release outside the picture still lets go, where the pointer last was inside it.
      send(hold.release(buttonOf(event.button)));
    };
    const cancel = () => { if (control) send(hold.release()); };
    const wheel = (event: WheelEvent) => {
      if (!control) return;
      event.preventDefault();
      const point = at(event);
      if (point) void send([{ kind: "wheel", ...point, deltaX: event.deltaX / scale(), deltaY: event.deltaY / scale() }]);
    };
    const menu = (event: Event) => { if (control) event.preventDefault(); };
    node.addEventListener("pointerdown", down);
    node.addEventListener("pointermove", move);
    node.addEventListener("pointerup", up);
    node.addEventListener("pointercancel", cancel);
    node.addEventListener("contextmenu", menu);
    // Not passive: the wheel must not also scroll the page.
    node.addEventListener("wheel", wheel, { passive: false });
    return () => {
      drop();
      cancelAnimationFrame(frame);
      node.removeEventListener("pointerdown", down);
      node.removeEventListener("pointermove", move);
      node.removeEventListener("pointerup", up);
      node.removeEventListener("pointercancel", cancel);
      node.removeEventListener("contextmenu", menu);
      node.removeEventListener("wheel", wheel);
    };
  }
</script>

<svelte:document onvisibilitychange={() => { visible = document.visibilityState === "visible"; if (visible && phase === "failed") attempt++; }} />
<FloatWindow label={`${name}'s computer`} kind="computer" {docked} {full} bind:rect bind:element onkeydown={key}>
  {#snippet header()}<Avatar kind="session" {name} {root} size={20} /><span class="title">{name}'s computer</span>
    {#if phase === "live"}<span class="live">Live</span>{:else if phase === "connecting"}<span class="connecting">Connecting</span>{/if}
    {#if !full}<button type="button" class="first" aria-label={docked ? "Undock" : "Dock in the sessions panel"} title={docked ? "Undock" : "Dock"} onclick={ondock}>
      <Icon name={docked ? "undock" : "dock"} /></button>{/if}
    <button type="button" class:first={full} aria-label="Close" title="Close" onclick={event => onclose(event.detail > 0)}><Icon name="close" /></button>{/snippet}
  <!-- The image streams only while the owner can see it: this view open, the page visible. -->
  <!-- While the owner controls it, the screen takes the keyboard and the pointer; its outline says so. -->
  <!-- svelte-ignore a11y_no_noninteractive_tabindex, a11y_no_noninteractive_element_interactions (the screen is a keyboard and pointer target only while the owner controls it) -->
  <div class="screen" class:docked class:full class:control class:foot={foot} style:--ratio={ratio} bind:this={screen} tabindex={control ? 0 : undefined}
    role={control ? "application" : undefined} aria-label={control ? `Controlling ${name}'s computer` : undefined} onkeydown={event => press(event, true)} onkeyup={event => press(event, false)} {@attach pointing}>
    {#if visible}{#key attempt}<img alt={`${name}'s screen, live`} {@attach watch} />{/key}{/if}
    {#if phase === "failed"}<p class="badge" role="status">The live view stopped. <button type="button" class="retry" onclick={() => { attempt++; element?.focus(); }}>Try again</button></p>{/if}
  </div>
  <!-- The current step, one muted line under the screen: "qa-runner · reading the review state". Floating, the line
       stays between steps, so the screen keeps the shape the window was fitted to. Where the owner may control the
       computer, the line carries the Take control step; in control it says how to return. -->
  {#if foot}<div class="activity">
    {#if control && stuck}<span class="line wrap"><StatusPill tone="error" word label="Input not sent" title="Your input did not reach the computer." /></span>
    {:else if control}<span class="line wrap">You are in control.<span class="hint"> Esc twice returns it.</span></span>
    {:else}<span class="line">{activity ? `${name} · ${activity}` : name}</span>{/if}
    {#if input && control}
      <button type="button" class="tool" class:on={keyboardOpen} aria-label="Phone keyboard" aria-pressed={keyboardOpen} title="Phone keyboard" onpointerdown={event => event.preventDefault()} onclick={toggleKeyboard}><Icon name="keyboard" size={18} /></button>
      {#if stuck}<button type="button" class="take" onclick={() => { stuck = false; queue.retry(); }}>Retry</button>{/if}
      <button type="button" class="take" onclick={release}>Return control</button>
    {:else if input}<button type="button" class="take" disabled={phase !== "live"} onclick={take}>Take control</button>{/if}
  </div>{/if}
  {#if input && control}
    <textarea class="field" aria-label="Type on the computer" bind:this={field} {@attach node => softKeyboard(node, phoneKeys, send)} onblur={() => { keyboardOpen = false; }} autocapitalize="off" autocomplete="off" spellcheck="false" rows="1"></textarea>
    {#if keyboardOpen}<PhoneKeys {held} onkey={key => { send(phoneKeys.press(key)); field?.focus({ preventScroll: true }); }} />{/if}
  {/if}
  {#if earlier.length}<ol class="earlier" aria-label="Earlier steps">{#each earlier as step, i (i)}<li>{shortPaths(step)}</li>{/each}</ol>{/if}
</FloatWindow>

<style>
  .title { flex: none; }
  /* A neutral outline label, the window's only uppercase text; it shows only once frames arrive. */
  .live { padding: 0 5px; border: 1px solid var(--line); border-radius: 4px; color: var(--muted); font-size: 11px; font-weight: 600; letter-spacing: 0.06em; text-transform: uppercase; }
  .connecting { color: var(--muted); font-weight: 400; }
  .first { margin-left: auto; }
  /* Full screen, the whole window is one near-black surface, as the screen is, from the notch to the home bar; the
     --screen tokens keep its text, buttons and avatar legible in both themes. */
  :global(.window.computer.full) { --text: var(--screen-text); --muted: var(--screen-muted); --line: var(--screen-line); --hover: var(--screen-hover); --focus: var(--screen-focus); --raised: var(--screen);
    --id-face: var(--screen-id-face); --id-alpha: var(--screen-id-alpha); --id-initials: var(--screen-id-initials); background: var(--screen); }
  /* The screen is near-black in both themes, as a display is. Docked, it takes the panel width at the screen's ratio;
     floating, it fills the window body edge to edge and the dark background fills the letterbox. */
  .screen { position: relative; display: grid; place-items: center; flex: 1; margin: 0; border-radius: 0 0 7px 7px; overflow: hidden; background: var(--screen); }
  .screen.foot { border-radius: 0; }
  /* Full screen, the picture sits under the title bar at its own shape; a short screen shrinks it to keep the steps. */
  .screen.full { flex: 0 1 auto; width: 100%; aspect-ratio: var(--ratio); border-radius: 0; }
  .screen.docked { flex: none; aspect-ratio: var(--ratio); margin: 12px 12px 0; border: 1px solid var(--line); border-radius: 8px; }
  img { position: absolute; inset: 0; width: 100%; height: 100%; object-fit: contain; }
  /* v1's badge: a dark chip over the last picture, readable on any frame. */
  .badge { position: absolute; left: 12px; top: 12px; margin: 0; padding: 3px 11px; border-radius: 12px; background: var(--screen-chip); color: var(--screen-text); font-size: 12px; }
  .badge .retry { margin-left: 4px; color: var(--screen-text); }
  /* The frame is drawn over the picture, which would cover an outline. */
  .screen.control { outline: none; touch-action: none; }
  .screen.control::after { content: ""; position: absolute; inset: 0; border: 2px solid var(--focus); pointer-events: none; }
  .screen:focus { outline-offset: -2px; }
  .activity { display: flex; flex: none; align-items: center; gap: 8px; min-height: 36px; padding: 0 12px; color: var(--muted); font-size: 12.5px; }
  .line { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .line.wrap { white-space: normal; }
  /* The key row has its own Esc, which goes to the computer, so a phone does not read the two-Esc step. */
  @media (hover: none) { .hint { display: none; } }
  /* Take control and Return control are controls, not statuses: an outline button, with the accent only on focus. */
  .take { flex: none; height: 28px; padding: 0 12px; border: 1px solid var(--firm); border-radius: 6px; background: none; color: var(--text); font: 500 13px var(--sans); cursor: pointer; }
  .take:hover:not(:disabled) { background: var(--hover); }
  .take:disabled { opacity: 0.45; cursor: default; }
  .take:focus-visible, .tool:focus-visible { outline: 2px solid var(--focus); outline-offset: 1px; }
  .tool { display: grid; flex: none; place-items: center; width: 28px; height: 28px; padding: 0; border: 0; border-radius: 6px; background: none; color: var(--muted); cursor: pointer; }
  .tool:hover, .tool.on { background: var(--hover); color: var(--text); }
  @media (hover: none) { .activity { min-height: 48px; } .take { height: 40px; padding: 0 16px; } .tool { width: 40px; height: 40px; } }
  /* The field that takes the phone keyboard's text: on the page and focusable, but invisible. */
  .field { position: fixed; left: 0; bottom: 0; width: 1px; height: 1px; padding: 0; border: 0; opacity: 0; font-size: 16px; pointer-events: none; }
  .earlier { flex: none; margin: 0; padding: 0 12px 6px; color: var(--muted); font-size: 12.5px; line-height: 20px; list-style: none; }
  .earlier li { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
</style>
