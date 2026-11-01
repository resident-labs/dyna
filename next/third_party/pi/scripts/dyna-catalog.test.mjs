import assert from "node:assert/strict";
import test from "node:test";
import { buildCatalog } from "./dyna-catalog.mjs";
import { defaultModelPerProvider } from "./dyna-defaults.mjs";

function provider(overrides = {}) {
  return { id: "fixture", name: "Fixture", auth: { apiKey: { name: "FIXTURE_KEY" } }, getModels: () => [{ id: "model", name: "Model", api: "openai-completions", baseUrl: "https://example.com/v1" }], ...overrides };
}
test("catalog validates at pack time and emits routable completions", () => {
  const result = buildCatalog([provider({ headers: { "X-Static": "fixed" } })]);
  assert.deepEqual(result.keys[0].headers, { "X-Static": "fixed" });
  assert.equal(result.keys[0].defaultModel, "model");
  assert.equal(result.keys[0].url, "https://example.com/v1");
  const kimi = buildCatalog([provider({ id: "kimi-coding", getModels: () => [{ id: "k3", name: "K3" }, { id: "unknown", name: "Unknown" }] })]);
  assert.deepEqual(kimi.plans[0].models, [{ id: "k3", name: "K3", proxyID: "kimi-k3" }]);
  for (const headers of [{ "X-Test": "$request" }, { Authorization: "secret" }]) assert.equal(buildCatalog([provider({ headers })]).keys.length, 0);
  for (const url of ["http://localhost/v1", "https://user@example.com/v1", "https://example.com:444/v1", "https://example.com/v1?key=x", "https://example.com/v1?", "https://{host}/v1"]) {
    assert.equal(buildCatalog([provider({ getModels: () => [{ id: "model", api: "openai-completions", baseUrl: url }] })]).keys.length, 0);
  }
  assert.throws(() => buildCatalog([provider({ id: "bad/provider" })]), /invalid or duplicate provider ID/);
  assert.throws(() => buildCatalog([provider({ getModels: () => [{ id: "fixture" }] })]), /invalid or duplicate model ID/);
  assert.throws(() => buildCatalog([provider(), provider()]), /duplicate provider ID/);
  assert.throws(() => buildCatalog([provider({ getModels: () => [{ id: "model" }, { id: "model" }] })]), /duplicate model ID/);
  const [id, preferred] = Object.entries(defaultModelPerProvider).find(([id, value]) => /^[a-z0-9][a-z0-9.-]*$/.test(id) && value !== id);
  const models = ["first", preferred].map((id) => ({ id, api: "openai-completions", baseUrl: "https://example.com/v1" }));
  assert.equal(buildCatalog([provider({ id, getModels: () => models })]).keys[0].defaultModel, preferred);
});
