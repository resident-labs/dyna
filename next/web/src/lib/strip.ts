// The dock strip shows whole tabs only. The active tab always shows; the others fill the room in order and stop at the
// first that does not fit, so the ones behind "N more" are always the later ones. Returns the shown tabs' indexes.
export function wholeTabs(widths: number[], room: number, active: number, gap = 2): number[] {
  const order = widths.map((_, index) => index).sort((a, b) => Number(b === active) - Number(a === active) || a - b);
  const shown: number[] = [];
  let used = -gap;
  for (const index of order) {
    used += widths[index] + gap;
    if (used > room && shown.length) break;
    shown.push(index);
  }
  return shown.sort((a, b) => a - b);
}

// What the phone key row sends. A held Ctrl turns one letter into its control character; arrows follow the shell's cursor mode.
export function keyBytes(name: "esc" | "tab" | "up" | "down" | "right" | "left", applicationCursor: boolean): string {
  const arrow = { up: "A", down: "B", right: "C", left: "D" }[name as "up"];
  return arrow ? `\x1b${applicationCursor ? "O" : "["}${arrow}` : name === "esc" ? "\x1b" : "\t";
}
export function withCtrl(text: string): string {
  const code = text.toUpperCase().charCodeAt(0);
  return text.length === 1 && code >= 64 && code <= 95 ? String.fromCharCode(code - 64) : text;
}
