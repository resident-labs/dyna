import { test } from "node:test";
import assert from "node:assert/strict";
import { profileChips, profileColumns, profileForSave, profileLabel, profileLine, profileModels, profileRows, profileText, providerFilter, replacements, sessionCount, slugName, type ProfileSummary } from "./profiles.ts";
import type { Account } from "./contract.gen.ts";

test("saving a profile keeps its hidden limit and allowed models", () => {
  const draft: ProfileSummary = { inUse: 2, name: " quick ", provider: "openai", model: " next-model ", thinking: "high", description: "Notes", taste: 8, diagnostics: 7, intelligence: 9, cost: 6, allowedModels: ["old-model"], maxConcurrent: 3 };
  const saved = profileForSave(draft);
  assert.equal(saved.name, "quick");
  assert.equal("inUse" in saved, false);
  assert.equal(saved.model, "next-model");
  assert.deepEqual(saved.allowedModels, ["old-model", "next-model"]);
  assert.equal(saved.maxConcurrent, 3);
  assert.equal(saved.description, "Notes");
  assert.equal(saved.taste, 8);
});

test("a profile reads as its name and thinking", () => {
  const opus: ProfileSummary = { name: "opus-rewrite", provider: "anthropic", model: "claude-opus-5-5", description: "", taste: 0, diagnostics: 0, intelligence: 0, cost: 0, inUse: 0, thinking: "xhigh" };
  const glm: ProfileSummary = { name: "glm-flash", provider: "openai", model: "openrouter/glm-5.3-flash", description: "", taste: 0, diagnostics: 0, intelligence: 0, cost: 0, inUse: 0, thinking: "off" };
  assert.deepEqual(profileLabel(opus), { name: "Opus Rewrite", detail: "very high thinking" });
  assert.deepEqual(profileLabel(glm), { name: "GLM Flash", detail: "no thinking" });
  assert.equal(slugName("claude-opus-5-5"), "Claude Opus 5.5");
  assert.equal(slugName("terra"), "Terra");
});

test("a removed profile's sessions can move to any other profile, by its shown name", () => {
  const profile = (name: string, thinking: ProfileSummary["thinking"]): ProfileSummary => ({ name, provider: "openai", model: "m", description: "", taste: 0, diagnostics: 0, intelligence: 0, cost: 0, inUse: 0, thinking });
  const list = [profile("opus-rewrite", "xhigh"), profile("glm-flash", "off")];
  assert.deepEqual(replacements(list, "opus-rewrite"), [{ name: "glm-flash", label: "GLM Flash · no thinking" }]);
  assert.deepEqual(replacements(list.slice(0, 1), "opus-rewrite"), []);
  assert.equal(sessionCount(1), "1 session");
  assert.equal(sessionCount(3), "3 sessions");
});

test("a profile can choose any model the proxy routes, custom providers included", () => {
  const account = (provider: string, models?: string[]) => ({ id: provider, provider, label: "", rooms: null, state: "connected", models }) as Account;
  assert.deepEqual(profileModels([
    account("custom-acme", ["custom-acme/local-one"]),
    account("anthropic", ["claude-opus-5-5"]),
    account("zai", ["glm-5.3"]),
    account("opencode-go", ["glm-5.3"]),
    account("meta"),
  ]), [
    { id: "claude-opus-5-5", provider: "anthropic" },
    { id: "custom-acme/local-one", provider: "openai" },
    { id: "glm-5.3", provider: "openai" },
  ]);
});

test("a listed entry becomes a row, and saving it sends back exactly the listed profile", () => {
  const stored = { name: "text", provider: "openai" as const, model: "m", thinking: "low" as const, description: "d", taste: 1, diagnostics: 2, intelligence: 3, cost: 4, images: false, allowedModels: ["m"], compactTokens: 5, maxConcurrent: 6 };
  const [row] = profileRows([{ profile: stored, inUse: 2 }]);
  assert.equal(row.inUse, 2);
  assert.deepEqual(profileForSave(row), stored);
  assert.deepEqual(profileRows(null), []);
});

const sample = (name: string, more: Partial<ProfileSummary> = {}): ProfileSummary => ({ name, provider: "openai", model: "glm-5.3", description: "", taste: 0, diagnostics: 0, intelligence: 0, cost: 0, inUse: 0, thinking: "low", ...more });

test("the chips are All, In use and Not rated, and a profile with any rating is rated", () => {
  assert.deepEqual(profileChips.map(chip => chip.label), ["All", "In use", "Not rated"]);
  const [, inUse, unrated] = profileChips;
  assert.equal(inUse.test!(sample("a", { inUse: 2 })), true);
  assert.equal(inUse.test!(sample("a")), false);
  assert.equal(unrated.test!(sample("a")), true);
  assert.equal(unrated.test!(sample("a", { cost: 3 })), false);
});

test("every column sorts: the name by its text, the values by number, unset lowest", () => {
  assert.deepEqual(profileColumns.map(column => column.label), ["Name", "Taste", "Diag.", "Intel.", "Thrift", "At once", "In use"]);
  for (const column of profileColumns) assert.ok(column.compare, column.id);
  const list = [sample("b", { taste: 7, maxConcurrent: 2, inUse: 1 }), sample("a", { taste: 9 }), sample("c", { taste: 0, maxConcurrent: 4 })];
  const order = (id: string) => [...list].sort(profileColumns.find(column => column.id === id)!.compare!).map(profile => profile.name);
  assert.deepEqual(order("name"), ["a", "b", "c"]);
  assert.deepEqual(order("taste"), ["c", "b", "a"]);
  assert.deepEqual(order("maxConcurrent"), ["a", "b", "c"]);
  assert.deepEqual(order("inUse"), ["a", "c", "b"]);
});

test("the provider filter keeps one provider's profiles, or every one", () => {
  assert.equal(providerFilter(""), undefined);
  assert.equal(providerFilter("anthropic")!(sample("a", { provider: "anthropic" })), true);
  assert.equal(providerFilter("anthropic")!(sample("a")), false);
});

test("a row reads as model and thinking, and a narrow row adds not rated and in use", () => {
  const profile = sample("glm", { model: "openrouter/glm-5.3", thinking: "xhigh", inUse: 3 });
  assert.equal(profileLine(profile, false), "openrouter/glm-5.3 · very high thinking");
  assert.equal(profileLine(profile, true), "openrouter/glm-5.3 · very high thinking · not rated · 3 in use");
  assert.equal(profileLine(sample("x", { taste: 4 }), true), "glm-5.3 · low thinking");
  // The search finds a profile by its name, its model or its provider as shown.
  assert.match(profileText(profile), /glm.*openrouter\/glm-5\.3.*OpenAI-compatible/);
});
