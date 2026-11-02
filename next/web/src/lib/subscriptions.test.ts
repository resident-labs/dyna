import { test } from "node:test";
import assert from "node:assert/strict";
import type { SubscriptionInfo } from "./contract.gen.ts";
import { hookName, subscriptionAction, subscriptionRows, subscriptionState, subscriptionSummary, subscriptionWhen } from "./subscriptions.ts";

const run = { command: ["bash", "watch.sh"], image: "base", size: "small" as const };
const info = (subscription: Partial<SubscriptionInfo["subscription"]>, rest: Partial<SubscriptionInfo> = {}): SubscriptionInfo =>
  ({ subscription: { id: "sub_a", scope: "room", ...subscription }, ...rest });

test("a subscription says when it fires", () => {
  assert.equal(subscriptionWhen(info({ every: 1_800_000 })), "Every 30 min");
  assert.equal(subscriptionWhen(info({ every: 7_200_000 })), "Every 2 h");
  assert.equal(subscriptionWhen(info({ every: 5_400_000 })), "Every 90 min");
  assert.equal(subscriptionWhen(info({ on: ["notice.posted", "pr.merged"] })), "On notice.posted, pr.merged");
  assert.equal(subscriptionWhen(info({ on: ["ci.finished", "notice.posted", "pr.closed"] })), "On 3 events");
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

test("a hook reads as the script it runs, with the arguments apart", () => {
  assert.deepEqual(hookName(["bash", "author-watch.sh", "levzzz5154", "3"]), { name: "Author watch", args: ["levzzz5154", "3"] });
  assert.deepEqual(hookName(["bash", "upstream-watch.sh"]), { name: "Upstream watch", args: [] });
  assert.deepEqual(hookName(["python3", "-u", "/box/shared/hooks/sample_repo_watch.py", "--fast"]), { name: "Sample repo watch", args: ["--fast"] });
  assert.deepEqual(hookName(["/usr/bin/env", "DEBUG=1", "node", "tools/check-prs.mjs"]), { name: "Check prs", args: [] });
  assert.deepEqual(hookName(["/box/hooks/watch-feed"]), { name: "Watch feed", args: [] });
  assert.deepEqual(hookName(["./run"]), { name: "Run", args: [] });
  assert.deepEqual(hookName(["make", "ci"]), { name: "Make", args: ["ci"] });
});

test("a shell line reads as the script inside it, or its first command", () => {
  assert.deepEqual(hookName(["bash", "-c", "cd /box/shared && bash mention-watch.sh room 2"]), { name: "Mention watch", args: ["room", "2"] });
  assert.deepEqual(hookName(["bash", "-lc", "A=1 python3 'x.py'"]), { name: "X", args: [] });
  assert.equal(hookName(["sh", "-c", "curl -s https://example.com | grep ok"]).name, "Curl");
});

test("a hook with no command still has a name", () => {
  assert.deepEqual(hookName([]), { name: "Hook", args: [] });
  assert.deepEqual(hookName(null), { name: "Hook", args: [] });
  assert.deepEqual(hookName(["bash"]), { name: "Hook", args: [] });
});

const hook = (rest: Partial<SubscriptionInfo> = {}, command = ["bash", "watch.sh"]) => info({ run: { ...run, command }, every: 900_000 }, rest);
const wake = (rest: Partial<SubscriptionInfo> = {}) => info({ wake: { kind: "session", session: "ses_root" }, on: ["notice.posted"] }, rest);
const summary = (list: SubscriptionInfo[]) => { const line = subscriptionSummary(list); return [line.count, line.health].filter(Boolean).join(", "); };

test("the closed line counts the hooks that are on, and says how they stand", () => {
  assert.equal(summary(Array.from({ length: 6 }, () => hook({ last: "passed" }))), "6 hooks, all passing");
  assert.equal(summary([hook({ last: "passed" })]), "1 hook, all passing");
  assert.equal(summary([...Array.from({ length: 5 }, () => hook({ last: "passed" })), hook({ last: "failed", failures: 1 })]), "6 hooks, 1 failing");
  assert.equal(subscriptionSummary([hook({ last: "failed", failures: 3 })]).failing, true);
});

test("a hook that has not run keeps the line from saying all passing", () => {
  assert.equal(summary([hook({ last: "passed" }), hook()]), "2 hooks");
});

test("a running hook is still judged by its last result", () => {
  assert.equal(summary([hook({ running: true, last: "passed" })]), "1 hook, all passing");
  assert.equal(summary([hook({ running: true, failures: 2, last: "failed" })]), "1 hook, 1 failing");
});

test("a disabled hook and a wake stay out of the count", () => {
  assert.equal(summary([hook({ last: "passed" }), hook({ disabled: true, failures: 4, last: "failed" }), wake(), wake({ disabled: true })]), "1 hook, all passing");
});

test("with no hook on, the line says what there is", () => {
  assert.equal(summary([wake()]), "1 wake");
  assert.equal(summary([wake(), wake()]), "2 wakes");
  assert.equal(summary([hook({ disabled: true }), wake({ disabled: true })]), "2 off");
  assert.equal(summary([]), "");
});

test("the list puts failures first, then hooks, then wakes, and disabled rows last", () => {
  const rows = subscriptionRows([
    hook({ disabled: true }, ["bash", "old-watch.sh"]), wake(), hook({ last: "passed" }, ["bash", "plain-watch.sh"]), hook({ failures: 2, last: "failed" }, ["bash", "bad-watch.sh"]),
  ], () => "root");
  assert.deepEqual(rows.map(row => row.name), ["Bad watch", "Plain watch", "Wakes root", "Old watch"]);
  assert.deepEqual(rows.map(row => row.state), ["Failing, 2 in a row", "", "", "Disabled"]);
  assert.deepEqual(rows.map(row => row.failing), [true, false, false, false]);
});

test("a row names its script, keeps the raw line for its tooltip, and tells apart two runs of one script", () => {
  const rows = subscriptionRows([hook({}, ["bash", "author-watch.sh", "levzzz5154", "3"]), hook({}, ["bash", "author-watch.sh", "Sylviromi"]), hook({}, ["bash", "solo.sh", "x"])], () => "root");
  assert.deepEqual(rows.map(row => [row.name, row.detail]), [["Author watch", "levzzz5154"], ["Author watch", "Sylviromi"], ["Solo", ""]]);
  assert.equal(rows[0].title, "Runs bash author-watch.sh levzzz5154 3");
  assert.equal(rows[0].when, "Every 15 min");
});
