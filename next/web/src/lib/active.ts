import type { Attention } from "./attention.svelte.ts";
import type { RoomSummary } from "./contract.gen.ts";
import { boxReason, count, shortPaths, turnReason } from "./feed.ts";

// What Home says a session does. A label is a tool and its argument ("read spec.md", "bash npm run build") or the
// fake harness's phrase, so a tool's name turns into the present tense. Only a command is mono; other words stay as sent.
const present = new Map([["read", "reading"], ["edit", "editing"], ["edited", "editing"], ["write", "writing"], ["wrote", "writing"],
  ["grep", "searching for"], ["find", "finding"], ["ls", "listing"], ["bash", "running"], ["ran", "running"], ["running", "running"]]);
export function doing(label: string): { words: string; command?: string } {
  const [tool, ...rest] = label.split(" ");
  const verb = present.get(tool) ?? tool, target = rest.join(" ");
  return verb === "running" ? { words: verb, command: target } : { words: shortPaths(`${verb} ${target}`.trim()) };
}

export type RoomLine = { room: RoomSummary; failed: number; words: string; command?: string };
const byName = (attention: Attention) => [...attention.rooms.values()].sort((a, b) => a.name.localeCompare(b.name));

// Home's Active now column. Rooms with a failed session come first and the rest keep their order by name, so a row
// never moves when a session starts or ends. One working session says what it does; two or more are a count.
export function activeRooms(attention: Attention): { active: RoomLine[]; quiet: RoomLine[] } {
  const lines = byName(attention).map(room => {
    const working = attention.busy(room.id), failed = attention.failing(room.id), open = attention.sessions.get(room.id)?.size ?? 0;
    const label = working.length === 1 ? attention.activity.get(working[0]) : undefined;
    const words = working.length ? `${working.length} working` : failed ? "" : open ? `${open} idle` : "No open session";
    return { room, failed, working: working.length, ...label ? doing(label) : { words } };
  });
  return { active: lines.filter(line => line.working || line.failed).sort((a, b) => Number(b.failed > 0) - Number(a.failed > 0)),
    quiet: lines.filter(line => !line.working && !line.failed) };
}

// One Needs you row per room with a failed session. The room's first reason speaks for all of them.
export const failedRooms = (attention: Attention, name: (session: string) => string | undefined) => byName(attention).flatMap(room => {
  const down = attention.down(room.id);
  if (!down.length) return [];
  const [reason, ...others] = new Set(down.map(({ box, cause }) => box ? boxReason(box) : turnReason(cause)));
  return [{ room, title: down.length > 1 ? `${count(down.length, "session")} failed` : `${name(down[0].id) ?? "A session"} failed`,
    reason: reason[0].toUpperCase() + reason.slice(1) + (others.length ? " and more" : "") }];
});
