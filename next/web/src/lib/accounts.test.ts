import { test } from "node:test";
import assert from "node:assert/strict";
import { accountRows, accountState, providerGroup, removalPrompt, canReconnect, reconnectUnavailable } from "./accounts.ts";
import { APIError } from "./client.ts";
import type { Account, AccountProviderInfo } from "./contract.gen.ts";

test("groups each provider by its first flow and searches names and IDs", () => {
  const providers: AccountProviderInfo[] = [
    { id: "anthropic", displayName: "Claude", connectMethod: "oauth", connectFlow: "callback", flows: ["callback", "key"], models: [] },
    ...Array.from({ length: 12 }, (_, i) => ({ id: `key-${i}`, displayName: `Provider ${i}`, connectMethod: "key" as const, connectFlow: "key" as const, flows: ["key" as const], models: [] })),
  ];
  assert.deepEqual(providerGroup(providers, "callback", "anthropic", false).items.map(p => p.id), ["anthropic"]);
  assert.equal(providerGroup(providers, "key", "", false).remaining, 2);
  assert.deepEqual(providerGroup(providers, "key", "key-11", false).items.map(p => p.id), ["key-11"]);
  assert.equal(providerGroup(providers, "key", "", true).count, 12);
});

test("only the typed unavailable error offers remove and add again", () => {
  assert.equal(reconnectUnavailable(new APIError({ code: "reconnect_unavailable", message: "unavailable" })), true);
  assert.equal(reconnectUnavailable(new APIError({ code: "unavailable", message: "offline" })), false);
  assert.equal(reconnectUnavailable(new Error("reconnect_unavailable")), false);
});

test("only expired sign-ins offer reconnect", () => {
  const account: Account = { id: "a", provider: "anthropic", flow: "callback", label: "Work", rooms: null, state: "sign_in_expired" };
  assert.equal(canReconnect(account, "oauth"), true);
  assert.equal(canReconnect({ ...account, state: "connected" }, "oauth"), false);
  assert.equal(canReconnect(account, "key"), false);
});

test("removal explains what Dyna deletes for each connection method", () => {
  assert.equal(removalPrompt("key"), "Remove this account? Dyna deletes its key. You can connect it again later.");
  assert.equal(removalPrompt("oauth"), "Remove this account? Dyna deletes its sign-in. You can connect it again later.");
});

test("unknown is muted and expired accounts sort first", () => {
  const unknown: Account = { id: "a", provider: "anthropic", flow: "callback", label: "Alpha", rooms: null, state: "unknown" };
  const expired: Account = { ...unknown, id: "z", label: "Zulu", state: "sign_in_expired" };
  assert.deepEqual(accountState(unknown), { line: "Not checked yet" });
  assert.deepEqual(accountRows([unknown, expired]).map(a => a.id), ["z", "a"]);
  assert.equal(accountState(expired).pill?.tone, "error");
});
