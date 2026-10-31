import type { ComputerInput, MouseButton } from "./contract.gen.ts";

// The owner's input to a computer, as POST /v1/computer.input takes it: DOM key names, DevTools modifier bits, and
// positions in the pixels of the picture the owner sees. v1 sent X11 keysyms over VNC, with a key down and up for each
// modifier; here a press carries its modifiers as a bit mask, and the box turns a key into a DevTools key event.
// The text-to-key rules are v1's phoneKeyboard.ts: Enter and Tab are keys, Ctrl and Alt apply to one key only, and text
// a keyboard composes goes in as text.

// Alt 1, Ctrl 2, Meta 4, Shift 8.
export const Alt = 1;
export const Ctrl = 2;
export const Shift = 8;

export type Modifiers = { ctrl: boolean; alt: boolean };

export type { ComputerInput };

// A key press is a down and an up, in that order.
const press = (key: string, code: string, modifiers: number): ComputerInput[] => [
  { kind: "key.down", key, code, modifiers, x: 0, y: 0 },
  { kind: "key.up", key, code, modifiers, x: 0, y: 0 },
];

const namedKeys = new Set(["Escape", "Tab", "Enter", "Backspace", "Delete", "Home", "End", "PageUp", "PageDown", "ArrowLeft", "ArrowUp", "ArrowRight", "ArrowDown"]);

// The on-screen keyboard's named keys.
export type PhoneKey = "ctrl" | "alt" | "esc" | "tab" | "left" | "up" | "down" | "right";
const phoneNamed: Record<Exclude<PhoneKey, "ctrl" | "alt">, string> = { esc: "Escape", tab: "Tab", left: "ArrowLeft", up: "ArrowUp", down: "ArrowDown", right: "ArrowRight" };

const maskOf = ({ ctrl, alt }: Modifiers) => (ctrl ? Ctrl : 0) | (alt ? Alt : 0);

// The code of a character a shortcut may name (Ctrl+L); other characters have none.
const codeOf = (char: string) => /^[a-z]$/i.test(char) ? `Key${char.toUpperCase()}` : /^[0-9]$/.test(char) ? `Digit${char}` : char === " " ? "Space" : "";

// What typing `value` sends. Characters above U+00FF go in as one text insert, as v1 sent them as Unicode keysyms:
// a key event cannot type them. A run of them stays one insert, so an input method's word arrives whole.
export function textInputs(value: string, modifiers: Modifiers): ComputerInput[] {
  const held = maskOf(modifiers);
  const out: ComputerInput[] = [];
  for (const char of value.replace(/\r\n?/g, "\n")) {
    const last = out.at(-1);
    if (char === "\n") out.push(...press("Enter", "Enter", held));
    else if (char === "\t") out.push(...press("Tab", "Tab", held));
    else if ((char.codePointAt(0) ?? 0) > 0xff) {
      if (last?.kind === "char") last.text += char;
      else out.push({ kind: "char", text: char, x: 0, y: 0 });
    } else out.push(...press(char, codeOf(char), held));
  }
  return out;
}

// A named key by its KeyboardEvent name, with the modifiers held.
export const namedInput = (name: string, modifiers: Modifiers): ComputerInput[] => namedKeys.has(name) ? press(name, name, maskOf(modifiers)) : [];

type Hardware = Pick<KeyboardEvent, "key" | "code" | "ctrlKey" | "altKey" | "shiftKey" | "metaKey" | "isComposing" | "keyCode">;
// The key down of a hardware key, or nothing for a key that sends nothing. Meta is the owner's own system key, so a
// shortcut of this machine never reaches the computer. Key up goes with release below.
export function keyboardInput(event: Hardware, down: boolean): ComputerInput[] {
  if (event.metaKey || event.isComposing || event.keyCode === 229) return [];
  // A modifier alone or a dead key is a name longer than one character that is not a key the page can use.
  if (!namedKeys.has(event.key) && !/^F([1-9]|1[0-2])$/.test(event.key) && [...event.key].length !== 1) return [];
  const modifiers = maskOf({ ctrl: event.ctrlKey, alt: event.altKey }) | (event.shiftKey ? Shift : 0);
  return [{ kind: down ? "key.down" : "key.up", key: event.key, code: event.code, modifiers, x: 0, y: 0 }];
}

// The phone keyboard's state: Ctrl and Alt apply to the next key only, then release.
export class PhoneKeys {
  private held: Modifiers = { ctrl: false, alt: false };
  private readonly changed: (state: Modifiers) => void;
  constructor(changed: (state: Modifiers) => void) {
    this.changed = changed;
    changed({ ...this.held });
  }
  // A modifier key toggles; a named key sends with the held modifiers and releases them.
  press(key: PhoneKey): ComputerInput[] {
    if (key === "ctrl" || key === "alt") {
      this.held = { ...this.held, [key]: !this.held[key] };
      this.changed({ ...this.held });
      return [];
    }
    return this.named(phoneNamed[key]);
  }
  // A key by its KeyboardEvent name, with the held modifiers; a name with no key sends nothing.
  named(name: string): ComputerInput[] {
    const out = namedInput(name, this.held);
    if (out.length) this.release();
    return out;
  }
  // Typed or pasted text. Modifiers apply to its first character only, as v1 released them after the first key.
  text(value: string): ComputerInput[] {
    const first = [...value][0];
    if (first === undefined) return [];
    const out = textInputs(first, this.held);
    this.release();
    return out.concat(textInputs(value.slice(first.length), this.held));
  }
  // The delete key of the soft keyboard.
  erase(forward: boolean): ComputerInput[] {
    return this.named(forward ? "Delete" : "Backspace");
  }
  private release() {
    if (!this.held.ctrl && !this.held.alt) return;
    this.held = { ctrl: false, alt: false };
    this.changed({ ...this.held });
  }
}

// Esc twice within a gap returns control. The first Esc still reaches the computer. Feed it each Esc press time.
export function escTwice(gap = 600): (at: number) => boolean {
  let last = -Infinity;
  return at => {
    const hit = at - last <= gap;
    last = hit ? -Infinity : at;
    return hit;
  };
}

// A point on the screen, in the picture's own pixels.
export type Point = { x: number; y: number };
export type Box = { left: number; top: number; width: number; height: number };
export type Size = { width: number; height: number };

// How many page pixels one picture pixel covers: the picture sits inside its box at its own shape (object-fit:
// contain), so one axis fills the box and the other has bars.
export const pictureScale = (box: Box, picture: Size) => Math.min(box.width / picture.width, box.height / picture.height);

// Where a pointer lands on the computer. A pointer on the bars around the picture lands nowhere.
export function screenPoint(box: Box, picture: Size, clientX: number, clientY: number): Point | undefined {
  if (!picture.width || !picture.height || !box.width || !box.height) return undefined;
  const scale = pictureScale(box, picture);
  const x = (clientX - box.left - (box.width - picture.width * scale) / 2) / scale;
  const y = (clientY - box.top - (box.height - picture.height * scale) / 2) / scale;
  return x < 0 || y < 0 || x > picture.width || y > picture.height ? undefined : { x: Math.round(x), y: Math.round(y) };
}

// The button a pointer event names. Touch and pen contact are the left button.
export const buttonOf = (button: number): MouseButton => (button === 1 ? "middle" : button === 2 ? "right" : "left");

// A finger on the screen: a short touch is a click where it lands, and a drag past the slop scrolls, as v1's
// two-finger scroll did (one finger here, because the view is not zoomed while in control).
// It reads the finger in page pixels (for the slop and the scroll) and on the screen (for where the input lands).
const slop = 8;
export class Finger {
  private touch: { page: Point; screen: Point; last: Point } | undefined;
  private scrolling = false;
  down(page: Point, screen: Point): void {
    this.touch = { page, screen, last: page };
    this.scrolling = false;
  }
  // scale is page pixels per picture pixel (pictureScale), so a scroll is in picture pixels as the route takes it.
  move(page: Point, screen: Point, scale: number): ComputerInput[] {
    const touch = this.touch;
    if (!touch) return [];
    if (!this.scrolling && Math.hypot(page.x - touch.page.x, page.y - touch.page.y) < slop) return [];
    this.scrolling = true;
    // The page moves with the finger, so the content scrolls the other way.
    const out: ComputerInput[] = [{ kind: "wheel", ...screen, deltaX: (touch.last.x - page.x) / scale, deltaY: (touch.last.y - page.y) / scale }];
    touch.last = page;
    return out;
  }
  up(): ComputerInput[] {
    const touch = this.touch;
    this.touch = undefined;
    if (!touch || this.scrolling) return [];
    const at = touch.screen;
    return [{ kind: "mouse.move", ...at }, { kind: "mouse.down", ...at, button: "left" }, { kind: "mouse.up", ...at, button: "left" }];
  }
}

const isRelease = (event: ComputerInput) => event.kind === "mouse.up" || event.kind === "key.up";

// One request in flight: the engine keeps order only inside a batch, so a later request could pass an earlier one. Events
// that come while one is in flight wait and go together as the next batch (at most 256, the route's limit). A refused
// batch pauses the queue and stays first, with everything after it in order, until retry: a key up never goes before its down.
export class InputQueue {
  private waiting: ComputerInput[] = [];
  private frame: Size = { width: 0, height: 0 };
  private busy = false;
  private paused = false;
  // After finish, a refused batch is tried again this many times, keeping only its releases, and is then given up.
  private tries = 0;
  private readonly deliver: (events: ComputerInput[], frame: Size) => Promise<void>;
  private readonly settled: (ok: boolean) => void;
  constructor(deliver: (events: ComputerInput[], frame: Size) => Promise<void>, settled: (ok: boolean) => void) {
    this.deliver = deliver;
    this.settled = settled;
  }
  push(events: ComputerInput[], frame: Size): void {
    this.waiting.push(...events);
    this.frame = frame;
    if (!this.busy && !this.paused) void this.run();
  }
  private async run(): Promise<void> {
    this.busy = true;
    while (this.waiting.length && !this.paused) {
      const batch = this.waiting.splice(0, 256);
      const ok = await this.deliver(batch, this.frame).then(() => true, () => false);
      if (!ok && this.tries > 0) {
        this.waiting.unshift(...batch.filter(isRelease));
        if (--this.tries === 0) this.waiting = [];
      } else if (!ok) {
        this.waiting.unshift(...batch);
        this.paused = true;
      }
      this.settled(ok);
    }
    this.tries = 0;
    this.busy = false;
  }
  // Sends the refused batch and what came after it, in order.
  retry(): void {
    this.paused = false;
    if (!this.busy) void this.run();
  }
  // Control ends or the view closes: with a refused batch paused, only the releases that wait go on, then `events`. The
  // queue sends them even when a request now in flight is refused, so the computer is not left with a key or button down.
  finish(events: ComputerInput[]): void {
    if (this.paused) this.waiting = this.waiting.filter(isRelease);
    this.waiting.push(...events);
    this.paused = false;
    this.tries = 3;
    if (!this.busy) void this.run();
  }
}

// The mouse buttons the owner holds on the computer. Control or the pointer may end outside the picture, so a release
// goes where the pointer last was inside it, and control never ends with a button held there.
export class MouseHold {
  private held = new Map<MouseButton, Point>();
  down(button: MouseButton, at: Point): void { this.held.set(button, at); }
  move(at: Point): void { for (const button of this.held.keys()) this.held.set(button, at); }
  // One button, or every held button when none is named.
  release(button?: MouseButton): ComputerInput[] {
    const out: ComputerInput[] = [];
    for (const [held, at] of this.held) {
      if (button !== undefined && held !== button) continue;
      out.push({ kind: "mouse.up", ...at, button: held });
      this.held.delete(held);
    }
    return out;
  }
}
