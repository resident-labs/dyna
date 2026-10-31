import { test } from "node:test";
import assert from "node:assert/strict";
import { accountRows, accountState, removalPrompt, canReconnect } from "./accounts.ts";
import type { Account } from "./contract.gen.ts";

test("only expired sign-ins offer reconnect", () => {
  const account: Account = { id: "a", provider: "anthropic", label: "Work", rooms: null, state: "sign_in_expired" };
  assert.equal(canReconnect(account, "oauth"), true);
  assert.equal(canReconnect({ ...account, state: "connected" }, "oauth"), false);
  assert.equal(canReconnect(account, "key"), false);
});

test("removal explains what Dyna deletes for each connection method", () => {
  assert.equal(removalPrompt("key"), "Remove this account? Dyna deletes its key. You can connect it again later.");
  assert.equal(removalPrompt("oauth"), "Remove this account? Dyna deletes its sign-in. You can connect it again later.");
});

test("unknown is muted and expired accounts sort first", () => {
  const unknown: Account = { id: "a", provider: "anthropic", label: "Alpha", rooms: null, state: "unknown" };
  const expired: Account = { ...unknown, id: "z", label: "Zulu", state: "sign_in_expired" };
  assert.deepEqual(accountState(unknown), { line: "Not checked yet" });
  assert.deepEqual(accountRows([unknown, expired]).map(a => a.id), ["z", "a"]);
  assert.equal(accountState(expired).pill?.tone, "error");
});
