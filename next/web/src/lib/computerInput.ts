// The owner's input to a computer, as the DevTools protocol takes it. v1 sent X11 keysyms over VNC (a key down and up
// for each modifier); a DevTools key event carries its modifiers as a bit mask, so a press here is one stroke.
// The text-to-key rules are v1's phoneKeyboard.ts: Enter and Tab are keys, the first modifier press applies to one
// key only, and text a keyboard composes goes in as text.

// Input.dispatchKeyEvent's modifier bits.
export const Alt = 1;
export const Ctrl = 2;
export const Shift = 8;

export type Modifiers = { ctrl: boolean; alt: boolean };

// One key press. vk is the Windows virtual key code DevTools wants (0 when the key has none); text is what the
// press types, and is absent for a shortcut or a key that types nothing.
export type Stroke = { type: "key"; key: string; code: string; vk: number; modifiers: number; text?: string };
// Text a keyboard composed (CJK, emoji, a paste): it goes in whole, as Input.insertText does.
export type Insert = { type: "text"; text: string };
export type KeyInput = Stroke | Insert;

type Named = { key: string; code: string; vk: number; text?: string };
const named: Record<string, Named> = {
  Escape: { key: "Escape", code: "Escape", vk: 27 },
  Tab: { key: "Tab", code: "Tab", vk: 9 },
  Enter: { key: "Enter", code: "Enter", vk: 13, text: "\r" },
  Backspace: { key: "Backspace", code: "Backspace", vk: 8 },
  Delete: { key: "Delete", code: "Delete", vk: 46 },
  Home: { key: "Home", code: "Home", vk: 36 },
  End: { key: "End", code: "End", vk: 35 },
  PageUp: { key: "PageUp", code: "PageUp", vk: 33 },
  PageDown: { key: "PageDown", code: "PageDown", vk: 34 },
  ArrowLeft: { key: "ArrowLeft", code: "ArrowLeft", vk: 37 },
  ArrowUp: { key: "ArrowUp", code: "ArrowUp", vk: 38 },
  ArrowRight: { key: "ArrowRight", code: "ArrowRight", vk: 39 },
  ArrowDown: { key: "ArrowDown", code: "ArrowDown", vk: 40 },
};

// The on-screen keyboard's named keys.
export type PhoneKey = "ctrl" | "alt" | "esc" | "tab" | "left" | "up" | "down" | "right";
const phoneNamed: Record<Exclude<PhoneKey, "ctrl" | "alt">, string> = { esc: "Escape", tab: "Tab", left: "ArrowLeft", up: "ArrowUp", down: "ArrowDown", right: "ArrowRight" };

const maskOf = ({ ctrl, alt }: Modifiers) => (ctrl ? Ctrl : 0) | (alt ? Alt : 0);

// A character the keyboard types on its own. Letters, digits and space carry the code and virtual key a shortcut
// needs (Ctrl+L); other characters type text and have no code.
function character(char: string, modifiers: number): Stroke {
  const upper = char.toUpperCase();
  const letter = /^[a-z]$/i.test(char), digit = /^[0-9]$/.test(char), space = char === " ";
  const code = letter ? `Key${upper}` : digit ? `Digit${char}` : space ? "Space" : "";
  const vk = letter ? upper.charCodeAt(0) : digit ? char.charCodeAt(0) : space ? 32 : 0;
  const stroke: Stroke = { type: "key", key: char, code, vk, modifiers };
  // Ctrl or Alt turns a character into a shortcut: it types nothing.
  if (!(modifiers & (Ctrl | Alt))) stroke.text = char;
  return stroke;
}

// What typing `value` sends. Characters above U+00FF go in as one text insert, as v1 sent them as Unicode keysyms:
// a DevTools key event cannot type them. A run of them stays one insert, so an IME's word arrives whole.
export function textInputs(value: string, modifiers: Modifiers): KeyInput[] {
  const held = maskOf(modifiers);
  const out: KeyInput[] = [];
  for (const char of value.replace(/\r\n?/g, "\n")) {
    const point = char.codePointAt(0) ?? 0;
    if (char === "\n") out.push({ type: "key", ...named.Enter, modifiers: held });
    else if (char === "\t") out.push({ type: "key", ...named.Tab, modifiers: held });
    else if (point > 0xff) {
      const last = out.at(-1);
      if (last?.type === "text") last.text += char;
      else out.push({ type: "text", text: char });
    } else out.push(character(char, held));
  }
  return out;
}

// The stroke of a named key, with the modifiers held.
export function namedInput(name: string, modifiers: Modifiers): Stroke | undefined {
  const key = named[name];
  return key && { type: "key", ...key, modifiers: maskOf(modifiers) };
}

// The stroke of a hardware key press. A key that types has its text; with Ctrl, Alt or Meta it is a shortcut.
// Meta (the owner's own system key) is not sent, so a shortcut of this machine never reaches the computer.
export function keyboardInput(event: Pick<KeyboardEvent, "key" | "code" | "keyCode" | "ctrlKey" | "altKey" | "shiftKey" | "metaKey" | "isComposing">): Stroke | undefined {
  if (event.metaKey || event.isComposing || event.keyCode === 229) return undefined;
  const modifiers = maskOf({ ctrl: event.ctrlKey, alt: event.altKey }) | (event.shiftKey ? Shift : 0);
  const key = named[event.key];
  if (key) return { type: "key", ...key, modifiers };
  if (/^F([1-9]|1[0-2])$/.test(event.key)) return { type: "key", key: event.key, code: event.code, vk: 111 + Number(event.key.slice(1)), modifiers };
  // A modifier alone or a dead key has nothing to send.
  if ([...event.key].length !== 1) return undefined;
  const stroke: Stroke = { type: "key", key: event.key, code: event.code, vk: event.keyCode, modifiers };
  if (!(modifiers & (Ctrl | Alt))) stroke.text = event.key;
  return stroke;
}

// The phone keyboard's state: Ctrl and Alt apply to the next key only, then release. press returns what to send.
export class PhoneKeys {
  private held: Modifiers = { ctrl: false, alt: false };
  private readonly changed: (state: Modifiers) => void;
  constructor(changed: (state: Modifiers) => void) {
    this.changed = changed;
    changed({ ...this.held });
  }
  // A modifier key toggles; a named key sends with the held modifiers and releases them.
  press(key: PhoneKey): KeyInput[] {
    if (key === "ctrl" || key === "alt") {
      this.held = { ...this.held, [key]: !this.held[key] };
      this.changed({ ...this.held });
      return [];
    }
    return this.named(phoneNamed[key]);
  }
  // A key by its KeyboardEvent name, with the held modifiers; a name with no key sends nothing.
  named(name: string): KeyInput[] {
    const stroke = namedInput(name, this.held);
    if (stroke) this.release();
    return stroke ? [stroke] : [];
  }
  // Typed or pasted text. Modifiers apply to its first character only, as v1 released them after the first key.
  text(value: string): KeyInput[] {
    const first = [...value][0];
    if (first === undefined) return [];
    const out = textInputs(first, this.held);
    this.release();
    return out.concat(textInputs(value.slice(first.length), this.held));
  }
  // The delete key of the soft keyboard.
  erase(forward: boolean): KeyInput[] {
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

// Where a pointer lands on the computer. The picture sits inside its box at its own shape (object-fit: contain), so
// the bars around it are not the screen: a pointer there lands nowhere.
export function screenPoint(box: Box, picture: { width: number; height: number }, clientX: number, clientY: number): Point | undefined {
  if (!picture.width || !picture.height || !box.width || !box.height) return undefined;
  const scale = Math.min(box.width / picture.width, box.height / picture.height);
  const width = picture.width * scale, height = picture.height * scale;
  const x = (clientX - box.left - (box.width - width) / 2) / scale;
  const y = (clientY - box.top - (box.height - height) / 2) / scale;
  return x < 0 || y < 0 || x > picture.width || y > picture.height ? undefined : { x: Math.round(x), y: Math.round(y) };
}

export type Button = "left" | "middle" | "right";
export type PointerInput =
  | { type: "move"; x: number; y: number }
  | { type: "down" | "up"; x: number; y: number; button: Button }
  | { type: "wheel"; x: number; y: number; dx: number; dy: number };

// The button a pointer event names. Touch and pen contact are the left button.
export const buttonOf = (button: number): Button => (button === 1 ? "middle" : button === 2 ? "right" : "left");

// A finger on the screen: a short touch is a click where it lands, and a drag past the slop scrolls, as v1's
// two-finger scroll did (one finger here, because the view is not zoomed while in control).
// It reads the finger in page pixels (for the slop) and on the screen (for where the input lands).
const slop = 8;
export class Finger {
  private touch: { page: Point; screen: Point; last: Point } | undefined;
  private scrolling = false;
  down(page: Point, screen: Point): void {
    this.touch = { page, screen, last: page };
    this.scrolling = false;
  }
  move(page: Point, screen: Point): PointerInput[] {
    const touch = this.touch;
    if (!touch) return [];
    if (!this.scrolling && Math.hypot(page.x - touch.page.x, page.y - touch.page.y) < slop) return [];
    this.scrolling = true;
    // The page moves with the finger, so the content scrolls the other way.
    const out: PointerInput[] = [{ type: "wheel", ...screen, dx: touch.last.x - page.x, dy: touch.last.y - page.y }];
    touch.last = page;
    return out;
  }
  up(): PointerInput[] {
    const touch = this.touch;
    this.touch = undefined;
    if (!touch || this.scrolling) return [];
    const at = touch.screen;
    return [{ type: "move", ...at }, { type: "down", ...at, button: "left" }, { type: "up", ...at, button: "left" }];
  }
}
