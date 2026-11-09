import { test } from "node:test";
import assert from "node:assert/strict";
import { accountChips, accountChoiceName, accountChoiceNote, accountItems, accountLine, accountPath, accountPill, addProviderError, labelRefusal, parseModelIDs, providerChoices, removalPrompt,
  reconnectUnavailable, roomChoices, usageHint, usageNote, type AccountItem } from "./accounts.ts";
import { APIError } from "./client.ts";
import type { Account, AccountProviderInfo, AccountUsage, ErrorCode, RoomSummary, UsageWindow } from "./contract.gen.ts";

const now = Date.parse("2026-11-03T12:00:00Z");
const claude: AccountProviderInfo = { id: "anthropic", displayName: "Claude", connectMethod: "oauth", connectFlow: "callback" };
const chatgpt: AccountProviderInfo = { id: "openai", displayName: "ChatGPT", connectMethod: "oauth", connectFlow: "callback" };
const deepseek: AccountProviderInfo = { id: "deepseek", displayName: "DeepSeek", connectMethod: "key", connectFlow: "key" };
const acct = (id: string, over: Partial<Account> = {}): Account => ({ id, provider: "anthropic", label: id, rooms: null, state: "connected", ...over });
const win = (usedPercent: number, kind: UsageWindow["kind"] = "five_hour", model?: string): UsageWindow =>
  ({ name: kind + (model ?? ""), kind, model, usedPercent, seconds: kind === "weekly" ? 604800 : 18000, resetsAt: "2026-11-03T15:00:00Z" });
const use = (accountId: string, over: Partial<AccountUsage> = {}): AccountUsage => ({ accountId, provider: "claude", label: accountId, windows: null, active: true, cause: "", ...over });
const item = (account: Account, usage?: AccountUsage): AccountItem => ({ account, provider: claude, name: account.label, usage });

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

test("accounts group by provider in the catalog's order, problems first, then by label in any case", () => {
  const items = accountItems([
    acct("b", { label: "beta" }), acct("o", { provider: "openai", label: "Zed" }), acct("A", { label: "Alpha" }),
    acct("low", { label: "zulu", state: "quota_low" }), acct("exp", { label: "yankee", state: "sign_in_expired" }), acct("m", { provider: "meta", label: "Meta one" }),
  ], [claude, chatgpt], [use("A")]);
  assert.deepEqual(items.map(each => each.account.id), ["exp", "low", "A", "b", "o", "m"]);
  assert.equal(items.find(each => each.account.id === "A")?.usage?.accountId, "A");
  assert.equal(items.find(each => each.account.id === "b")?.usage, undefined);
  // A provider the catalog does not know still lists, last, under its own ID.
  assert.equal(items[5].provider, undefined);
  assert.equal(items[5].name, "Meta one");
});

test("an account without a label is named by its provider and its place there", () => {
  const items = accountItems([acct("x", { label: "" }), acct("y", { label: "" }), acct("z", { provider: "meta", label: "" })], [claude], []);
  assert.deepEqual(items.map(each => each.name), ["Claude account 1", "Claude account 2", "meta account 1"]);
});

test("chips count expired sign-ins and accounts limited to rooms", () => {
  const items = [item(acct("a", { state: "sign_in_expired" })), item(acct("b", { rooms: ["r1"] })), item(acct("c", { rooms: [] })), item(acct("d", { state: "quota_low", rooms: ["r1", "r2"] }))];
  const count = (id: string) => items.filter(accountChips.find(chip => chip.id === id)?.test ?? (() => true)).length;
  assert.deepEqual(accountChips.map(chip => chip.label), ["All", "Needs sign-in", "Limited to rooms"]);
  assert.equal(count("all"), 4);
  assert.equal(count("expired"), 1);
  assert.equal(count("limited"), 2);
});

test("only an expired sign-in and low usage get a pill", () => {
  assert.deepEqual(accountPill(acct("a", { state: "sign_in_expired" })), { tone: "error", label: "Sign-in expired" });
  assert.deepEqual(accountPill(acct("a", { state: "quota_low" })), { tone: "warning", label: "Low usage" });
  for (const state of ["connected", "unknown", "failed"] as const) assert.equal(accountPill(acct("a", { state })), undefined);
});

test("a row's line is its rooms and one value for its state", () => {
  assert.equal(accountLine(item(acct("a"))), "Every room");
  assert.equal(accountLine(item(acct("a", { rooms: ["r1"] }))), "1 room");
  assert.equal(accountLine(item(acct("a", { rooms: ["r1", "r2"], state: "unknown" }))), "2 rooms · not checked yet");
  assert.equal(accountLine(item(acct("a", { state: "failed" }))), "Every room · usage unread");
  assert.equal(accountLine(item(acct("a"), use("a", { cause: "insufficient_balance", balances: [{ currency: "USD", amount: "0.00" }] }))), "Every room · out of credit");
  assert.equal(accountLine(item(acct("a"), use("a", { balances: [{ currency: "CNY", amount: "110.00" }] }))), "Every room · 110.00 CNY");
});

test("a row's usage hint is a bar for each of three windows and the peak of them all", () => {
  const hint = usageHint(item(acct("a"), use("a", { windows: [win(40), win(89.6, "weekly"), win(12, "monthly"), win(95, "other")] })), now);
  assert.deepEqual(hint?.bars.map(bar => [bar.label, bar.percent, bar.busy]), [["5-hour", 40, false], ["Weekly", 90, true], ["Monthly", 12, false]]);
  assert.equal(hint?.peak, 95);
  // The highest window need not be the first or the last.
  assert.equal(usageHint(item(acct("a"), use("a", { windows: [win(10), win(70, "weekly"), win(30, "monthly")] })), now)?.peak, 70);
  assert.equal(usageHint(item(acct("a"), use("a", { windows: [win(89.4)] })), now)?.bars[0].busy, false);
});

test("no usage hint for an expired sign-in, a usage cause, or no windows", () => {
  const windows = [win(40)];
  assert.equal(usageHint(item(acct("a", { state: "sign_in_expired" }), use("a", { windows })), now), undefined);
  assert.equal(usageHint(item(acct("a"), use("a", { windows, cause: "sign_in_expired" })), now), undefined);
  assert.equal(usageHint(item(acct("a"), use("a", { windows, cause: "insufficient_balance" })), now), undefined);
  assert.equal(usageHint(item(acct("a"), use("a", { balances: [{ currency: "USD", amount: "3" }] })), now), undefined);
  assert.equal(usageHint(item(acct("a")), now), undefined);
});

test("the detail says why an account shows no usage", () => {
  assert.equal(usageNote(item(acct("a", { state: "sign_in_expired" }), use("a", { windows: [win(10)] }))), "Limits show once it is signed in again.");
  assert.equal(usageNote(item(acct("a", { state: "unknown" }))), "Not checked yet.");
  assert.equal(usageNote(item(acct("a", { state: "failed" }))), "Usage could not be read.");
  assert.equal(usageNote(item(acct("a"), use("a", { cause: "insufficient_balance" }))), "Out of credit: top up this account");
  assert.equal(usageNote(item(acct("a"))), "This provider reports no usage limits.");
  assert.equal(usageNote(item(acct("a"), use("a", { windows: [win(10)] }))), undefined);
  assert.equal(usageNote(item(acct("a"), use("a", { balances: [{ currency: "USD", amount: "3" }] }))), undefined);
});

test("providers list plan sign-ins, then codes, then ten keys until a search or Show more", () => {
  const keys: AccountProviderInfo[] = Array.from({ length: 12 }, (_, i) => ({ ...deepseek, displayName: `Key ${i}` }));
  const device: AccountProviderInfo = { ...claude, id: "kimi", displayName: "Kimi", connectFlow: "device" };
  const all = [...keys, device, claude];
  const shut = providerChoices(all, "", false);
  assert.deepEqual(shut.items.slice(0, 3).map(p => p.displayName), ["Claude", "Kimi", "Key 0"]);
  assert.equal(shut.items.length, 12);
  assert.equal(shut.more, 2);
  assert.equal(providerChoices(all, "", true).items.length, 14);
  assert.deepEqual(providerChoices(all, "KEY 11", false), { items: [keys[11]], more: 0 });
  assert.deepEqual(providerChoices(all, "  ", false).more, 2);
});

test("the rooms picker shows the chosen first, searches in any case, and counts the rest past the cap", () => {
  const room = (id: string, name: string) => ({ id, name }) as RoomSummary;
  const rooms = [room("1", "alpha"), room("2", "Beta"), room("3", "gamma"), room("4", "beta-two")];
  assert.deepEqual(roomChoices(rooms, ["3"], "", 60).items.map(r => r.id), ["3", "1", "2", "4"]);
  assert.deepEqual(roomChoices(rooms, ["4"], "BETA", 60), { items: [rooms[3], rooms[1]], more: 0 });
  assert.deepEqual(roomChoices(rooms, [], "", 2), { items: [rooms[0], rooms[1]], more: 2 });
  assert.deepEqual(roomChoices(rooms, [], "", 4).more, 0);
  assert.deepEqual(roomChoices(rooms, [], "", 3).more, 1);
});

test("search unfolds matching API key providers", () => {
  const providers: AccountProviderInfo[] = Array.from({ length: 12 }, (_, i) => ({ id: "anthropic", displayName: `Provider ${i}`, connectMethod: "key", connectFlow: "key" }));
  assert.equal(providerChoices(providers, "", false).more, 2);
  assert.equal(providerChoices(providers, "provider 11", false).items[0].displayName, "Provider 11");
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

test("removal says what Dyna deletes for each connection method", () => {
  assert.equal(removalPrompt("key"), "Dyna deletes its key where Dyna runs.");
  assert.equal(removalPrompt("oauth"), "Dyna deletes its sign-in where Dyna runs.");
});

test("an account's link carries its ID as the list page's one item", () => {
  assert.equal(accountPath("a/b"), "/settings/accounts/a%2Fb");
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

test("labels that end in numbers sort by number, as Claude 9 before Claude 10", () => {
  const items = accountItems([acct("a", { label: "Claude 10" }), acct("b", { label: "claude 9" }), acct("c", { label: "Claude 2" })], [claude], []);
  assert.deepEqual(items.map(each => each.account.label), ["Claude 2", "claude 9", "Claude 10"]);
});

test("accounts of providers the catalog does not know stay grouped by provider, after the known ones", () => {
  const items = accountItems([acct("m1", { provider: "meta", label: "a" }), acct("k1", { provider: "kimi", label: "b" }), acct("c", { label: "z" }),
    acct("m2", { provider: "meta", label: "c" }), acct("k2", { provider: "kimi", label: "d" })], [claude], []);
  assert.deepEqual(items.map(each => each.account.id), ["c", "k1", "k2", "m1", "m2"]);
});

test("a search shows every key provider it matches, past the ten", () => {
  const keys: AccountProviderInfo[] = Array.from({ length: 12 }, (_, i) => ({ ...deepseek, displayName: `Key ${i}` }));
  const found = providerChoices([claude, ...keys], "key", false);
  assert.equal(found.items.length, 12);
  assert.equal(found.more, 0);
});
