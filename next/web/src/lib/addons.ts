import { commandLine, fromNow } from "./feed.ts";
import type { Chip } from "./settings-list.ts";
import type { Addon, AddonKind, MCPAddRequest, PackageAddRequest, RoomSummary, SkillSaveRequest } from "./contract.gen.ts";

export const addonKinds = [
  { kind: "skill", label: "Skill", icon: "file" },
  { kind: "package", label: "Pi package", icon: "briefcase" },
  { kind: "mcp", label: "MCP server", icon: "plug" },
] as const;
export const addonKind = (kind: AddonKind) => addonKinds.find(item => item.kind === kind) ?? addonKinds[0];

// Settings > Skills & tools (spec 5.13). An item is its scope, its kind and its name: a built-in and a global item may
// share a name, and so may two rooms' items. The scope part is the item's group.
export const addonScope = (item: Addon) => item.builtIn ? "built-in" : item.room ?? "global";
export const addonKey = (item: Addon) => `${addonScope(item)}:${item.kind}:${item.name}`;

export function addonWhere(item: Addon, rooms: readonly RoomSummary[]): string {
  if (item.builtIn) return "Built in";
  if (!item.room) return "All rooms";
  return rooms.find(room => room.id === item.room)?.name ?? "A room no longer listed";
}

// Built-ins first, then what every room shares, then each room by name; each scope keeps the engine's order.
export function addonOrder(items: readonly Addon[], rooms: readonly RoomSummary[]): Addon[] {
  const rank = (item: Addon) => item.builtIn ? 0 : item.room ? 2 : 1;
  return [...items].sort((a, b) => rank(a) - rank(b) || (rank(a) === 2 ? addonWhere(a, rooms).localeCompare(addonWhere(b, rooms)) : 0));
}

export const addonChips: Chip<Addon>[] = [
  { id: "all", label: "All" },
  ...addonKinds.map(({ kind, label }) => ({ id: kind, label: kind === "package" ? "Pi packages" : `${label}s`, test: (item: Addon) => item.kind === kind })),
  { id: "off", label: "Off", test: item => !!item.disabled },
];

// What the item is, on one line.
export function addonDetail(item: Addon): string {
  if (item.kind === "mcp") return item.url || commandLine([item.command ?? "", ...item.args ?? []]);
  return (item.kind === "skill" && item.description) || (item.source ?? "");
}

export const addonAdded = (item: Addon, now: number) => `by ${item.by === "owner" ? "you" : item.by || "someone unknown"}${item.at ? `, ${fromNow(item.at, now)}` : ""}`;

export function addonSummary(items: readonly Addon[]) {
  const inRooms = items.filter(item => !item.builtIn && item.room);
  return { builtIn: items.filter(item => item.builtIn).length, global: items.filter(item => !item.builtIn && !item.room).length, inRooms: inRooms.length,
    rooms: new Set(inRooms.map(item => item.room)).size };
}

// The hosts a skill may run on: host.list's registered hosts, without the engine's own control-plane entry, and any the
// skill still names that host.list no longer has, so turning one off stays possible.
export const runOnChoices = (listed: readonly string[], current: readonly string[] | null | undefined) =>
  [...new Set([...listed.filter(name => name !== "control-plane"), ...current ?? []])];

export type AddonDraft = { kind: AddonKind; where: string; source: string; name: string };
export const emptyDraft = (): AddonDraft => ({ kind: "skill", where: "", source: "", name: "" });

type Where = { room: string } | { global: true };
// The scope the typed Where names: All rooms, or a room by its name in any case; undefined until it names one.
export function draftScope(where: string, rooms: readonly RoomSummary[]): Where | undefined {
  const text = where.trim().toLowerCase();
  if (text === "all rooms") return { global: true };
  const room = rooms.find(each => each.name.toLowerCase() === text);
  return room && { room: room.id };
}

// What an MCP field holds: a link, or one executable and its arguments split on spaces, since no shell reads it.
export function mcpTarget(text: string): Pick<MCPAddRequest, "url" | "command" | "args"> {
  const value = text.trim();
  if (/^https?:\/\//.test(value)) return { url: value };
  const [command = "", ...args] = value.split(/\s+/);
  return { command, args };
}

export const draftReady = (draft: AddonDraft, where: Where | undefined) => !!where && draft.source.trim() !== "" && (draft.kind !== "mcp" || draft.name.trim() !== "");

export const skillRequest = (draft: AddonDraft, where: Where): SkillSaveRequest => ({ ...where, url: draft.source.trim() });
export const packageRequest = (draft: AddonDraft, where: Where): PackageAddRequest => ({ ...where, source: draft.source.trim() });
export const mcpRequest = (draft: AddonDraft, where: Where): MCPAddRequest => ({ ...where, name: draft.name.trim(), ...mcpTarget(draft.source) });
