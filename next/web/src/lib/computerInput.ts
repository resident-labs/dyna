// The phone keyboard's input to a computer, as X11 keysyms for noVNC's sendKey. A mouse, a finger and a hardware keyboard
// go to the desktop through noVNC itself; a phone keyboard types into a hidden field, so its text and the key row's keys
// come here. The text-to-key rules are v1's phoneKeyboard.ts: Enter and Tab are keys, Ctrl and Alt apply to one key only,
// and every other character is its own keysym, which Xvnc types whatever its keyboard layout.

// One key going down or up.
export type KeyEvent = { keysym: number; down: boolean };

export type Modifiers = { ctrl: boolean; alt: boolean };

const Control = 0xffe3, Alt = 0xffe9;
const named: Record<string, number> = {
  Escape: 0xff1b, Tab: 0xff09, Enter: 0xff0d, Backspace: 0xff08, Delete: 0xffff, Home: 0xff50, End: 0xff57,
  PageUp: 0xff55, PageDown: 0xff56, ArrowLeft: 0xff51, ArrowUp: 0xff52, ArrowRight: 0xff53, ArrowDown: 0xff54,
};

// A character's keysym: Latin-1 keysyms are the code point, and any other character is a Unicode keysym.
export const keysymOf = (char: string): number => {
  const point = char.codePointAt(0) ?? 0;
  return (point >= 0x20 && point <= 0x7e) || (point >= 0xa0 && point <= 0xff) ? point : 0x01000000 + point;
};

// A press is the held modifiers down, the key down and up, then the modifiers up.
function press(keysym: number, { ctrl, alt }: Modifiers): KeyEvent[] {
  const held = [...ctrl ? [Control] : [], ...alt ? [Alt] : []];
  return [...held.map(keysym => ({ keysym, down: true })), { keysym, down: true }, { keysym, down: false }, ...held.reverse().map(keysym => ({ keysym, down: false }))];
}

// What typing `value` sends, with the modifiers held for each key.
export function textInputs(value: string, modifiers: Modifiers): KeyEvent[] {
  return [...value.replace(/\r\n?/g, "\n")].flatMap(char => press(char === "\n" ? named.Enter : char === "\t" ? named.Tab : keysymOf(char), modifiers));
}

// A named key by its KeyboardEvent name, with the modifiers held; a name with no key sends nothing.
export const namedInput = (name: string, modifiers: Modifiers): KeyEvent[] => name in named ? press(named[name], modifiers) : [];

// The on-screen keyboard's named keys.
export type PhoneKey = "ctrl" | "alt" | "esc" | "tab" | "left" | "up" | "down" | "right";
const phoneNamed: Record<Exclude<PhoneKey, "ctrl" | "alt">, string> = { esc: "Escape", tab: "Tab", left: "ArrowLeft", up: "ArrowUp", down: "ArrowDown", right: "ArrowRight" };

// The phone keyboard's state: Ctrl and Alt apply to the next key only, then release.
export class PhoneKeys {
  private held: Modifiers = { ctrl: false, alt: false };
  private readonly changed: (state: Modifiers) => void;
  constructor(changed: (state: Modifiers) => void) {
    this.changed = changed;
    changed({ ...this.held });
  }
  // A modifier key toggles; a named key sends with the held modifiers and releases them.
  press(key: PhoneKey): KeyEvent[] {
    if (key === "ctrl" || key === "alt") {
      this.held = { ...this.held, [key]: !this.held[key] };
      this.changed({ ...this.held });
      return [];
    }
    return this.named(phoneNamed[key]);
  }
  // A key by its KeyboardEvent name, with the held modifiers; a name with no key sends nothing.
  named(name: string): KeyEvent[] {
    const out = namedInput(name, this.held);
    if (out.length) this.release();
    return out;
  }
  // Typed or pasted text. Modifiers apply to its first character only, as v1 released them after the first key.
  text(value: string): KeyEvent[] {
    const first = [...value][0];
    if (first === undefined) return [];
    const out = textInputs(first, this.held);
    this.release();
    return out.concat(textInputs(value.slice(first.length), this.held));
  }
  // The delete key of the soft keyboard.
  erase(forward: boolean): KeyEvent[] {
    return this.named(forward ? "Delete" : "Backspace");
  }
  private release() {
    if (!this.held.ctrl && !this.held.alt) return;
    this.held = { ctrl: false, alt: false };
    this.changed({ ...this.held });
  }
}

// Esc twice within a gap leaves full screen. The first Esc still reaches the computer. Feed it each Esc press time.
export function escTwice(gap = 600): (at: number) => boolean {
  let last = -Infinity;
  return at => {
    const hit = at - last <= gap;
    last = hit ? -Infinity : at;
    return hit;
  };
}

// A second tap or click within 350 ms and 24 px of the first: the view's way to full screen. Feed it each press.
export function doubleTap(gap = 350, slop = 24): (at: number, x: number, y: number) => boolean {
  let last = { at: -Infinity, x: 0, y: 0 };
  return (at, x, y) => {
    const hit = at - last.at <= gap && Math.hypot(x - last.x, y - last.y) <= slop;
    last = hit ? { at: -Infinity, x: 0, y: 0 } : { at, x, y };
    return hit;
  };
}
