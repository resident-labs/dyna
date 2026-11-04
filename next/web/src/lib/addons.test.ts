import { test } from "node:test";
import assert from "node:assert/strict";
import { addonAdded, addonChips, addonDetail, addonKey, addonKind, addonOrder, addonSummary, addonWhere, draftReady, draftScope, emptyDraft, mcpRequest, mcpTarget, packageRequest, runOnChoices, runOnHosts, skillRequest } from "./addons.ts";
import { listView } from "./settings-list.ts";
import type { Addon, RoomSummary } from "./contract.gen.ts";

const room = (id: string, name: string) => ({ id, name }) as RoomSummary;
const rooms = [room("a", "web"), room("b", "docs"), room("c", "api")];
const skill: Addon = { kind: "skill", name: "pdf", description: "Read a PDF" };

test("each item has one key, its scope then its kind then its name, so a built-in and a global item of one name differ", () => {
  assert.equal(addonKey({ ...skill, builtIn: true }), "built-in:skill:pdf");
  assert.equal(addonKey(skill), "global:skill:pdf");
  assert.equal(addonKey({ kind: "package", name: "@x/tools", room: "a" }), "a:package:@x/tools");
});

test("an item says where it applies: built in, all rooms, or its room by name", () => {
  assert.equal(addonWhere({ ...skill, builtIn: true }, rooms), "Built in");
  assert.equal(addonWhere(skill, rooms), "All rooms");
  assert.equal(addonWhere({ ...skill, room: "b" }, rooms), "docs");
  assert.equal(addonWhere({ ...skill, room: "gone" }, rooms), "A room no longer listed");
});

test("the list runs built-ins, then all rooms, then each room by name, each keeping the engine's order", () => {
  const items: Addon[] = [
    { ...skill, name: "in-web", room: "a" }, { ...skill, name: "in-api", room: "c" }, { ...skill, name: "shared" }, { ...skill, name: "dyna-extend", builtIn: true },
    { ...skill, name: "in-web-2", room: "a" }, { ...skill, name: "in-docs", room: "b" }, { ...skill, name: "shared-2" }, { ...skill, name: "dyna-images", builtIn: true },
  ];
  assert.deepEqual(addonOrder(items, rooms).map(item => item.name), ["dyna-extend", "dyna-images", "shared", "shared-2", "in-api", "in-docs", "in-web", "in-web-2"]);
  // As groups, a room with nothing added has none.
  const view = listView(addonOrder(items, rooms), { text: item => item.name, key: addonKey, query: "", chip: addonChips[0], folded: new Set(["built-in"]),
    group: item => ({ id: addonKey(item).split(":")[0], title: addonWhere(item, rooms) }) });
  assert.deepEqual(view.rows.filter(row => row.kind === "group").map(row => row.kind === "group" && [row.group.title, row.count, row.folded]),
    [["Built in", 2, true], ["All rooms", 2, false], ["api", 1, false], ["docs", 1, false], ["web", 2, false]]);
});

test("the chips pick a kind, or the items turned off", () => {
  const items: Addon[] = [skill, { ...skill, name: "off", disabled: true }, { kind: "package", name: "tools" }, { kind: "mcp", name: "docs" }, { kind: "mcp", name: "on", disabled: false }];
  const view = listView(items, { text: item => item.name, key: addonKey, query: "", chip: addonChips[0], chips: addonChips, folded: new Set() });
  assert.deepEqual(addonChips.map(chip => chip.label), ["All", "Skills", "Pi packages", "MCP servers", "Off"]);
  assert.deepEqual(view.counts, { all: 5, skill: 2, package: 1, mcp: 2, off: 1 });
});

test("each kind says what it is on one line", () => {
  assert.equal(addonDetail(skill), "Read a PDF");
  assert.equal(addonDetail({ kind: "skill", name: "bare", source: "https://example.test/bare/SKILL.md" }), "https://example.test/bare/SKILL.md");
  assert.equal(addonDetail({ kind: "package", name: "tools", source: "npm:tools@1.2.3" }), "npm:tools@1.2.3");
  assert.equal(addonDetail({ kind: "mcp", name: "docs", url: "https://example.test/mcp" }), "https://example.test/mcp");
  assert.equal(addonDetail({ kind: "mcp", name: "files", command: "npx", args: ["-y", "server files"] }), "npx -y 'server files'");
  assert.equal(addonKind("mcp").label, "MCP server");
});

test("who added an item and when", () => {
  const now = Date.parse("2026-11-01T12:00:00Z");
  assert.equal(addonAdded({ ...skill, by: "owner", at: "2026-11-01T10:00:00Z" }, now), "by you, 2 hours ago");
  assert.equal(addonAdded({ ...skill, by: "lead-web" }, now), "by lead-web");
  assert.equal(addonAdded(skill, now), "by someone unknown");
});

test("the summary counts built-ins, items for all rooms, and items in rooms with how many rooms", () => {
  const items: Addon[] = [{ ...skill, builtIn: true }, skill, { ...skill, name: "x", room: "a" }, { ...skill, name: "y", room: "a" }, { ...skill, room: "b" }];
  assert.deepEqual(addonSummary(items), { builtIn: 1, global: 1, inRooms: 3, rooms: 2 });
  assert.deepEqual(addonSummary([]), { builtIn: 0, global: 0, inRooms: 0, rooms: 0 });
});

test("a skill's run-on choices are the registered hosts, then any it names that are no longer listed, without the control plane", () => {
  assert.deepEqual(runOnChoices(["arch", "control-plane", "omarchy"], ["desktop", "arch"]), ["arch", "omarchy", "desktop"]);
  assert.deepEqual(runOnChoices([], undefined), []);
});

test("a run-on switch adds its host last, or takes it out, and keeps the others' order", () => {
  assert.deepEqual(runOnHosts(["desktop", "arch"], "omarchy", true), ["desktop", "arch", "omarchy"]);
  assert.deepEqual(runOnHosts(["desktop", "arch"], "desktop", false), ["arch"]);
  assert.deepEqual(runOnHosts(["arch"], "arch", true), ["arch"]);
  assert.deepEqual(runOnHosts(undefined, "arch", true), ["arch"]);
  assert.deepEqual(runOnHosts(["arch"], "arch", false), []);
});

test("Where names All rooms or a room, by its name in any case, and nothing until it does", () => {
  assert.deepEqual(draftScope(" all rooms ", rooms), { global: true });
  assert.deepEqual(draftScope("Docs", rooms), { room: "b" });
  assert.deepEqual(draftScope(" docs ", rooms), { room: "b" });
  // Names are unique as stored, so two that differ in case both exist, and the exact one wins.
  const cased = [room("u", "Docs"), room("l", "docs")];
  assert.deepEqual(draftScope("docs", cased), { room: "l" });
  assert.deepEqual(draftScope("Docs", cased), { room: "u" });
  assert.deepEqual(draftScope("DOCS", cased), { room: "u" });
  assert.equal(draftScope("doc", rooms), undefined);
  assert.equal(draftScope("", rooms), undefined);
});

test("an MCP field is a link or an executable with its arguments", () => {
  assert.deepEqual(mcpTarget(" https://example.test/mcp "), { url: "https://example.test/mcp" });
  assert.deepEqual(mcpTarget("npx -y  server"), { command: "npx", args: ["-y", "server"] });
});

test("a draft becomes the request for its kind and scope", () => {
  const draft = { ...emptyDraft(), where: "web", source: " https://example.test/SKILL.md " };
  assert.deepEqual(skillRequest(draft, { room: "a" }), { room: "a", url: "https://example.test/SKILL.md" });
  assert.deepEqual(packageRequest({ ...draft, kind: "package", source: "npm:tools" }, { global: true }), { global: true, source: "npm:tools" });
  assert.deepEqual(mcpRequest({ ...draft, kind: "mcp", name: " docs ", source: "server --flag" }, { room: "a" }), { room: "a", name: "docs", command: "server", args: ["--flag"] });
  assert.equal(draftReady(emptyDraft(), { global: true }), false);
  assert.equal(draftReady(draft, undefined), false);
  assert.equal(draftReady(draft, { room: "a" }), true);
  assert.equal(draftReady({ ...draft, kind: "mcp" }, { room: "a" }), false);
  assert.equal(draftReady({ ...draft, kind: "mcp", name: "docs" }, { room: "a" }), true);
});
