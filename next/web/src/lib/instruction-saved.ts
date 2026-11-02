import type { InstructionsSaver } from "./contract.gen.ts";

type Saver = InstructionsSaver | null | undefined;

// Who saved an instructions file, as words after "Saved": the owner reading the page is "you", and a room's root is its
// name and room. A file saved before the record was kept names nobody.
export function savedBy(saver: Saver) {
  if (!saver) return "";
  if (saver.kind === "owner") return "by you";
  if (!saver.name) return "";
  return saver.roomName ? `by ${saver.name} (${saver.roomName})` : `by ${saver.name}`;
}
