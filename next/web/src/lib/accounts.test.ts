import { test } from "node:test";
import assert from "node:assert/strict";
import { accountChoiceName, accountChoiceNote, accountPath, accountRows, accountState, addProviderError, labelRefusal, parseModelIDs, providerGroup, reconnectPath, removalPrompt, canReconnect, reconnectUnavailable } from "./accounts.ts";
import { APIError } from "./client.ts";
import type { Account, AccountProviderInfo, ErrorCode } from "./contract.gen.ts";

test("a session's Account row says what is chosen and when it fell back", () => {
  assert.equal(accountChoiceName({ choices: [] }), "Automatic");
  assert.equal(accountChoiceName({ account: "a", label: "pipsqueak.tasks", choices: [] }), "pipsqueak.tasks");
  assert.equal(accountChoiceName({ account: "gone", issue: "removed", choices: [] }), "Removed account");
  assert.equal(accountChoiceNote(), "");
  assert.equal(accountChoiceNote("spent"), "Limit reached. Using Automatic.");
  assert.equal(accountChoiceNote("signed_out"), "Signed out. Using Automatic.");
  assert.equal(accountChoiceNote("removed"), "Removed. Using Automatic.");
  assert.equal(accountChoiceNote("unavailable"), "Unavailable right now. Using Automatic.");
});

test("search unfolds matching API key providers", () => {
  const providers: AccountProviderInfo[] = Array.from({ length: 12 }, (_, i) => ({ id: "anthropic", displayName: `Provider ${i}`, connectMethod: "key", connectFlow: "key" }));
  assert.equal(providerGroup(providers, "key", "", false).remaining, 2);
  assert.equal(providerGroup(providers, "key", "provider 11", false).items[0].displayName, "Provider 11");
  assert.equal(providerGroup(providers, "key", "", true).items.length, 12);
});

test("only the typed unavailable error offers remove and add again", () => {
  assert.equal(reconnectUnavailable(new APIError({ code: "reconnect_unavailable", message: "unavailable" })), true);
  assert.equal(reconnectUnavailable(new APIError({ code: "unavailable", message: "offline" })), false);
  assert.equal(reconnectUnavailable(new Error("reconnect_unavailable")), false);
});

test("only the engine's refusal asks for a different label", () => {
  assert.equal(labelRefusal(new APIError({ code: "invalid", message: "invalid account label" })), "Use 1 to 128 characters, without @.");
  assert.equal(labelRefusal(new APIError({ code: "unavailable", message: "offline" })), "The label was not saved. Try again.");
  assert.equal(labelRefusal(new Error("invalid")), "The label was not saved. Try again.");
});

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

test("an account's links carry its ID as one path segment", () => {
  assert.equal(accountPath("a/b"), "/settings/accounts/account/a%2Fb");
  assert.equal(reconnectPath("a/b"), "/settings/accounts/reconnect/a%2Fb");
});

test("reads model IDs from lines or commas, once each", () => {
  assert.deepEqual(parseModelIDs("one, two\n three,one\n"), ["one", "two", "three"]);
  assert.deepEqual(parseModelIDs("  "), []);
});

test("says what to fix when a new provider is refused", () => {
  const refusal = (code: ErrorCode) => new APIError({ code, message: "" });
  assert.match(addProviderError(refusal("models_unavailable")), /model IDs/);
  assert.match(addProviderError(refusal("invalid")), /name and address/);
  assert.match(addProviderError(new Error("network")), /Try again/);
});
