import { test } from "node:test";
import assert from "node:assert/strict";
import { profileForSave, profileLabel, profileModels, slugName } from "./profiles.ts";
import type { Account, ProfileSummary } from "./contract.gen.ts";

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
