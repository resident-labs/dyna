import { commandLine, fromNow } from "./feed.ts";
import type { Addon, AddonKind, MCPAddRequest, PackageAddRequest, RoomSummary, SkillSaveRequest } from "./contract.gen.ts";

export const addonKinds = [
  { kind: "skill", label: "Skill", icon: "file" },
  { kind: "package", label: "Pi package", icon: "briefcase" },
  { kind: "mcp", label: "MCP server", icon: "plug" },
] as const;
export const addonKind = (kind: AddonKind) => addonKinds.find(item => item.kind === kind) ?? addonKinds[0];

export type AddonScope = { id: string; title: string; room?: string; note: string; items: readonly Addon[] };

// The page's sections in reach order: what every box has, what every room shares, then each room, a room with nothing included.
export function addonScopes(items: readonly Addon[], rooms: readonly RoomSummary[]): AddonScope[] {
  const held = (room: string) => items.filter(item => !item.builtIn && (item.room ?? "") === room);
  return [
    { id: "built-in", title: "Built in", note: "Ships with Dyna. Every box has these.", items: items.filter(item => item.builtIn) },
    { id: "global", title: "All rooms", note: "Added for every room. A room's own item wins a name clash.", items: held("") },
    ...rooms.map(room => ({ id: room.id, title: room.name, room: room.id, note: "Added for this room only.", items: held(room.id) })),
  ];
}

// What the item is, on one line.
export function addonDetail(item: Addon): string {
  if (item.kind === "mcp") return item.url || commandLine([item.command ?? "", ...item.args ?? []]);
  return item.kind === "skill" ? item.description ?? "" : item.source ?? "";
}

export function addonOrigin(item: Addon, now: number): string {
  if (item.builtIn) return "Built in";
  const by = item.by === "owner" ? "you" : item.by || "unknown";
  return `Added by ${by}${item.at ? ` ${fromNow(item.at, now)}` : ""}`;
}

export type AddonDraft = { kind: AddonKind; scope: string; source: string; name: string };
export const emptyDraft = (scope: string): AddonDraft => ({ kind: "skill", scope, source: "", name: "" });

// What an MCP field holds: a link, or one executable and its arguments split on spaces, since no shell reads it.
export function mcpTarget(text: string): Pick<MCPAddRequest, "url" | "command" | "args"> {
  const value = text.trim();
  if (/^https?:\/\//.test(value)) return { url: value };
  const [command = "", ...args] = value.split(/\s+/);
  return { command, args };
}

type Where = { room: string } | { global: true };
export const draftScope = (draft: AddonDraft): Where => draft.scope === "global" ? { global: true } : { room: draft.scope };

export const draftReady = (draft: AddonDraft) => draft.source.trim() !== "" && (draft.kind !== "mcp" || draft.name.trim() !== "");

export const skillRequest = (draft: AddonDraft): SkillSaveRequest => ({ ...draftScope(draft), url: draft.source.trim() });
export const packageRequest = (draft: AddonDraft): PackageAddRequest => ({ ...draftScope(draft), source: draft.source.trim() });
export const mcpRequest = (draft: AddonDraft): MCPAddRequest => ({ ...draftScope(draft), name: draft.name.trim(), ...mcpTarget(draft.source) });
