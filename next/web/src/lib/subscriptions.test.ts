import { test } from "node:test";
import assert from "node:assert/strict";
import type { SubscriptionInfo } from "./contract.gen.ts";
import { subscriptionAction, subscriptionState, subscriptionWhen } from "./subscriptions.ts";

const run = { command: ["bash", "watch.sh"], image: "base", size: "small" as const };
const info = (subscription: Partial<SubscriptionInfo["subscription"]>, rest: Partial<SubscriptionInfo> = {}): SubscriptionInfo =>
  ({ subscription: { id: "sub_a", scope: "room", ...subscription }, ...rest });

test("a subscription says when it fires", () => {
  assert.equal(subscriptionWhen(info({ every: 1_800_000 })), "Every 30 min");
  assert.equal(subscriptionWhen(info({ every: 7_200_000 })), "Every 2 h");
  assert.equal(subscriptionWhen(info({ every: 5_400_000 })), "Every 90 min");
  assert.equal(subscriptionWhen(info({ on: ["notice.posted", "pr.merged"] })), "On notice.posted, pr.merged");
});

test("a subscription says what it does", () => {
  const name = (session: string) => session === "ses_root" ? "root" : "?";
  assert.equal(subscriptionAction(info({ run }), name), "Runs bash watch.sh");
  assert.equal(subscriptionAction(info({ wake: { kind: "session", session: "ses_root" } }), name), "Wakes root");
});

test("a subscription's state puts the worst news first", () => {
  assert.equal(subscriptionState(info({}, { disabled: true, failures: 2 })), "Disabled");
  assert.equal(subscriptionState(info({}, { running: true, failures: 2 })), "Running");
  assert.equal(subscriptionState(info({}, { failures: 2, last: "failed" })), "Failing, 2 in a row");
  assert.equal(subscriptionState(info({}, { last: "passed" })), "Passing");
  assert.equal(subscriptionState(info({})), "");
});
