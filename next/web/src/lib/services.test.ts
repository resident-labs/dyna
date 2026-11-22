import { test } from "node:test";
import assert from "node:assert/strict";
import type { Service, SubscriptionInfo } from "./contract.gen.ts";
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
    sub({ id: "s3", on: ["service.message"], workflow: { script: "/box/shared/triage-feed.js" } }),
    sub({ id: "s4", on: ["service.failed"], where: ["service=agent-chat"], wake: "ses_root" }),
    sub({ id: "s5", on: ["service.message"], where: ["service=agent-chat"], wake: "ses_root" }, { disabled: true }),
  ];
  assert.deepEqual(serviceRoutes(svc, subs, names), ["→ wakes sunrise", "→ runs triage feed"]);
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
