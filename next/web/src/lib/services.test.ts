import { test } from "node:test";
import assert from "node:assert/strict";
import type { DiscordRoute, Service, SubscriptionInfo } from "./contract.gen.ts";
import { serviceError, serviceRoutes, serviceRows, serviceSummary } from "./services.ts";

const makeService = (overrides: Partial<Service> = {}): Service => ({
  id: "svc_1",
  room: "room_1",
  spec: { name: "agent-chat", image: "agent-chat", feed: true },
  generation: 1,
  host: "hermeslab",
  want: "run",
  state: "running",
  deployed: 1,
  cpuMilli: 250,
  memBytes: 256 * 1024 * 1024,
  diskBytes: 0,
  restarts: 0,
  feed: { lastMessageAt: "2026-11-22T21:58:00Z", messagesIn: 42, messagesOut: 7 },
  ...overrides,
});

const sub = (subscription: Partial<SubscriptionInfo["subscription"]>, rest: Partial<SubscriptionInfo> = {}): SubscriptionInfo =>
  ({ waiting: 0, subscription: { id: "sub_1", scope: "room", ...subscription }, ...rest });

test("serviceSummary counts active services and surfaces failures first", () => {
  assert.deepEqual(serviceSummary([]), { count: 0, health: "", failing: false });
  assert.deepEqual(serviceSummary([makeService(), makeService({ id: "svc_2", spec: { name: "redis", image: "redis" } })]), { count: 2, health: "all running", failing: false });
  assert.deepEqual(serviceSummary([makeService(), makeService({ id: "svc_2", state: "failed", reason: "crash_loop" })]), { count: 2, health: "1 failing", failing: true });
  assert.deepEqual(serviceSummary([makeService({ state: "pending", reason: "cpu" })]), { count: 1, health: "1 pending", failing: false });
  assert.deepEqual(serviceSummary([makeService({ want: "remove", state: "failed" })]), { count: 0, health: "", failing: false });
});

test("serviceRoutes matches enabled service.message subscriptions for a feed", () => {
  const svc = makeService();
  const names = (id: string) => ({ ses_sunrise: "sunrise", ses_root: "root" }[id] ?? id);
  const subs = [
    sub({ id: "s1", on: ["service.message"], where: ["service=agent-chat"], wake: "ses_sunrise" }),
    sub({ id: "s2", on: ["service.message"], where: ["service=other-feed"], wake: "ses_root" }),
    sub({ id: "s3", on: ["service.message"], workflow: { script: "// handle https://example.com/hook\nserve(async e => e);" } }),
    sub({ id: "s4", on: ["service.failed"], where: ["service=agent-chat"], wake: "ses_root" }),
    sub({ id: "s5", on: ["service.message"], where: ["service=agent-chat"], wake: "ses_root" }, { disabled: true }),
    sub({ id: "s6", on: ["service.message"], where: ["service!=agent-chat"], wake: "ses_root" }),
    sub({ id: "s7", on: ["service.message"], where: ["service=agent-chat", "service=other-feed"], wake: "ses_root" }),
    sub({ id: "s8", on: ["service.message"], where: ["service!=other-feed"], run: { command: ["bash", "triage-feed.sh"], image: "base", size: "small" } }),
  ];
  assert.deepEqual(serviceRoutes(svc, subs, names), ["→ wakes sunrise", "→ runs workflow", "→ runs Triage feed"]);
});

test("serviceRows sorts failures first and formats feed stats and errors", () => {
  const now = Date.parse("2026-11-22T22:00:00Z");
  const rows = serviceRows([
    makeService({ id: "svc_ok", spec: { name: "agent-chat", image: "agent-chat", feed: true }, state: "running" }),
    makeService({ id: "svc_bad", spec: { name: "bridge", image: "bridge", feed: true }, state: "failed", reason: "crash_loop", detail: "exit status 1", feed: { messagesIn: 0, messagesOut: 0 } }),
    makeService({ id: "svc_plain", spec: { name: "postgres", image: "pg", feed: false }, state: "stopped", feed: null }),
  ], [
    sub({ id: "s1", on: ["service.message"], where: ["service=agent-chat"], wake: "ses_sunrise" }),
  ], () => "sunrise", now);

  assert.deepEqual(rows.map(r => r.name), ["bridge", "agent-chat", "postgres"]);
  assert.equal(rows[0].failing, true);
  assert.equal(rows[0].error, "Crash loop: exit status 1");
  assert.equal(rows[0].lastMessage, "No messages yet");
  assert.equal(rows[1].lastMessage, "2 minutes ago");
  assert.equal(rows[1].messagesIn, 42);
  assert.equal(rows[1].messagesOut, 7);
  assert.deepEqual(rows[1].routes, ["→ wakes sunrise"]);
  assert.equal(rows[2].feed, false);
  assert.equal(serviceError(makeService({ reason: "health_failed", detail: "" })), "Health check failed");
});

test("Discord routes appear as built-in feeds in serviceSummary and serviceRows", () => {
  const routes: DiscordRoute[] = [
    { guild: "100", guildName: "Resident Labs", channel: "200", channelName: "support", room: "room_1", session: "ses_front" },
    { guild: "100", guildName: "Resident Labs", room: "room_1", session: "ses_root" },
    { guild: "101", channel: "300", room: "room_1", session: "ses_ops", gatewayError: "gateway disconnected" },
  ];
  const names = (id: string) => ({ ses_front: "front", ses_root: "root", ses_ops: "ops" }[id] ?? id);

  assert.deepEqual(serviceSummary([], routes.slice(0, 2)), { count: 2, health: "all running", failing: false });
  assert.deepEqual(serviceSummary([makeService()], routes), { count: 4, health: "1 failing", failing: true });

  const rows = serviceRows([makeService()], [], names, Date.parse("2026-11-22T22:00:00Z"), routes);
  assert.equal(rows.length, 4);
  // Failing Discord route sorts first.
  assert.equal(rows[0].name, "Discord");
  assert.equal(rows[0].builtIn, true);
  assert.equal(rows[0].failing, true);
  assert.equal(rows[0].host, "channel 300");
  assert.equal(rows[0].detail, "server 101");
  assert.equal(rows[0].error, "gateway disconnected");
  assert.deepEqual(rows[0].routes, ["→ wakes ops"]);

  // Healthy container service and built-in Discord routes follow.
  const support = rows.find(r => r.host === "#support");
  assert.ok(support);
  assert.equal(support.builtIn, true);
  assert.equal(support.state, "Active");
  assert.equal(support.detail, "Resident Labs · channel 200");
  assert.deepEqual(support.routes, ["→ wakes front"]);

  const wholeServer = rows.find(r => r.host === "Resident Labs");
  assert.ok(wholeServer);
  assert.equal(wholeServer.detail, "All channels · server 100");
  assert.deepEqual(wholeServer.routes, ["→ wakes root"]);
});
