import { test } from "node:test";
import assert from "node:assert/strict";
import { profileLabel, slugName } from "./profiles.ts";
import type { ProfileSummary } from "./contract.gen.ts";

test("a profile reads as its model, provider and thinking, and twins add their name", () => {
  const opus: ProfileSummary = { name: "deep", provider: "anthropic", model: "claude-opus-5-5", thinking: "xhigh" };
  const gpt: ProfileSummary = { name: "quick", provider: "openai", model: "openrouter/gpt-6.1-sol", thinking: "off" };
  assert.deepEqual(profileLabel(opus, [opus, gpt]), { model: "Claude Opus 5.5", detail: "Anthropic · very high thinking" });
  assert.deepEqual(profileLabel(gpt, [opus, gpt]), { model: "GPT 6.1 Sol", detail: "OpenAI · no thinking" });
  assert.deepEqual(profileLabel(opus, [opus, { ...opus, name: "rewrite" }]), { model: "Claude Opus 5.5", detail: "Anthropic · very high thinking · Deep" });
  assert.equal(slugName("terra"), "Terra");
});
