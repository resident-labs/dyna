import { test } from "node:test";
import assert from "node:assert/strict";
import { engineRestartParts, engineRestartText } from "./engine-restarted.ts";
import { decodeEvent } from "./event.ts";
import { describe, feedRows, subject } from "./feed.ts";
import { timelineInput } from "./timeline.ts";
import type { EngineRestarted } from "./contract.gen.ts";

const build = "123456789abcdef123456789abcdef123456789a";
const previousBuild = "abcdef123456789abcdef123456789abcdef1234";
const cases: { body: EngineRestarted; text: string; builds: string[] }[] = [
  { body: { build, previousBuild, clean: true, downMillis: 7500 }, text: "Dyna restarted on build 123456789abc (was abcdef123456); it was down 7 s.", builds: [build, previousBuild] },
  { body: { build, previousBuild: build, clean: true, downMillis: 2000 }, text: "Dyna restarted on build 123456789abc; it was down 2 s.", builds: [build] },
  { body: { build, previousBuild, clean: false, downMillis: 0 }, text: "Dyna restarted on build 123456789abc after an unclean stop (was abcdef123456).", builds: [build, previousBuild] },
];

test("restart notices use the engine's three texts and link each short build to its full commit", () => {
  for (const { body, text, builds } of cases) {
    assert.equal(engineRestartText(body), text);
    assert.deepEqual(engineRestartParts(body).filter(part => part.href), builds.map(sha => ({ text: sha.slice(0, 12), href: `https://github.com/resident-labs/dyna/commit/${sha}` })));
  }
  assert.deepEqual(engineRestartParts({ build: "dev", previousBuild: "dev", clean: true, downMillis: 0 }).filter(part => part.href), []);
});

test("a decoded engine restart has one plain notice in the room feed, not a post or work row", () => {
  for (const { body, text } of cases) {
    const event = decodeEvent({ seq: 1, id: "restart", at: "2026-11-06T12:00:00Z", room: "r", from: { kind: "engine" }, type: "engine.restarted", body });
    assert.ok(event);
    assert.equal(subject(event), undefined);
    const rows = feedRows([event], { kind: "feed" }).filter(row => row.kind !== "day");
    assert.equal(rows.length, 1);
    assert.equal(rows[0].kind, "notice");
    assert.equal(describe(event, id => id), text);
    assert.deepEqual(feedRows([event], { kind: "dm", session: "worker" }), []);
    assert.deepEqual(timelineInput({ id: event.id, seq: event.seq, at: event.at, room: "r", from: event.from, type: event.type, text: JSON.stringify(body) }), { kind: "line", text, restart: body });
  }
});

test("restart event bodies are validated once at the web decode boundary", () => {
  const raw = { seq: 1, id: "restart", at: "", room: "r", from: { kind: "engine" }, type: "engine.restarted" };
  assert.equal(decodeEvent({ ...raw, body: { ...cases[0].body, clean: "yes" } }), undefined);
  assert.equal(decodeEvent({ ...raw, body: { build, previousBuild, clean: true } }), undefined);
});
