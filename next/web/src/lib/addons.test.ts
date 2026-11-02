import { test } from "node:test";
import assert from "node:assert/strict";
import { addonDetail, addonKind, addonOrigin, addonScopes, draftReady, emptyDraft, mcpRequest, mcpTarget, packageRequest, skillRequest } from "./addons.ts";
import type { Addon, RoomSummary } from "./contract.gen.ts";

const room = (id: string, name: string) => ({ id, name }) as RoomSummary;
const skill: Addon = { kind: "skill", name: "pdf", description: "Read a PDF" };

test("the page shows built-ins, then all rooms, then each room, even one with nothing", () => {
  const items: Addon[] = [{ ...skill, name: "dyna-extend", builtIn: true }, skill, { ...skill, name: "own", room: "a" }];
  const scopes = addonScopes(items, [room("a", "web"), room("b", "docs")]);
  assert.deepEqual(scopes.map(scope => [scope.title, scope.items.map(item => item.name)]), [["Built in", ["dyna-extend"]], ["All rooms", ["pdf"]], ["web", ["own"]], ["docs", []]]);
});

test("each kind says what it is on one line", () => {
  assert.equal(addonDetail(skill), "Read a PDF");
  assert.equal(addonDetail({ kind: "package", name: "tools", source: "npm:tools@1.2.3" }), "npm:tools@1.2.3");
  assert.equal(addonDetail({ kind: "mcp", name: "docs", url: "https://example.test/mcp" }), "https://example.test/mcp");
  assert.equal(addonDetail({ kind: "mcp", name: "files", command: "npx", args: ["-y", "server files"] }), "npx -y 'server files'");
  assert.equal(addonKind("mcp").label, "MCP server");
});

test("the origin names who added an item", () => {
  const now = Date.parse("2026-11-01T12:00:00Z");
  assert.equal(addonOrigin({ ...skill, builtIn: true }, now), "Built in");
  assert.equal(addonOrigin({ ...skill, by: "owner", at: "2026-11-01T10:00:00Z" }, now), "Added by you 2 hours ago");
  assert.equal(addonOrigin({ ...skill, by: "lead-web" }, now), "Added by lead-web");
});

test("an MCP field is a link or an executable with its arguments", () => {
  assert.deepEqual(mcpTarget(" https://example.test/mcp "), { url: "https://example.test/mcp" });
  assert.deepEqual(mcpTarget("npx -y  server"), { command: "npx", args: ["-y", "server"] });
});

test("a draft becomes the request for its kind and scope", () => {
  const draft = { ...emptyDraft("a"), source: " https://example.test/SKILL.md " };
  assert.deepEqual(skillRequest(draft), { room: "a", url: "https://example.test/SKILL.md" });
  assert.deepEqual(packageRequest({ ...draft, kind: "package", scope: "global", source: "npm:tools" }), { global: true, source: "npm:tools" });
  assert.deepEqual(mcpRequest({ ...draft, kind: "mcp", name: " docs ", source: "server --flag" }), { room: "a", name: "docs", command: "server", args: ["--flag"] });
  assert.equal(draftReady(emptyDraft("a")), false);
  assert.equal(draftReady({ ...draft, kind: "mcp" }), false);
  assert.equal(draftReady({ ...draft, kind: "mcp", name: "docs" }), true);
});
