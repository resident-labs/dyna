import { test } from "node:test";
import assert from "node:assert/strict";
import { accountRows, accountState, showLoadError } from "./accounts.ts";
import type { Account } from "./contract.gen.ts";

test("a failed refresh preserves an already loaded pane", () => {
  assert.equal(showLoadError(false), true);
  assert.equal(showLoadError(true), false);
});

test("unknown is muted and expired accounts sort first", () => {
  const unknown: Account = { id: "a", provider: "anthropic", label: "Alpha", rooms: null, state: "unknown" };
  const expired: Account = { ...unknown, id: "z", label: "Zulu", state: "sign_in_expired" };
  assert.deepEqual(accountState(unknown), { line: "Not checked yet" });
  assert.deepEqual(accountRows([unknown, expired]).map(a => a.id), ["z", "a"]);
  assert.equal(accountState(expired).pill?.tone, "error");
});
