import assert from "node:assert/strict";
import test from "node:test";
import { buildCatalog } from "./dyna-catalog.mjs";

function provider(overrides = {}) {
  return { id: "fixture", name: "Fixture", auth: { apiKey: { name: "FIXTURE_KEY" } }, getModels: () => [{ id: "model", name: "Model", api: "openai-completions", baseUrl: "https://example.com/v1" }], ...overrides };
}
test("catalog validates at pack time and emits routable completions", () => {
  const result = buildCatalog([provider({ headers: { "X-Static": "fixed", Authorization: "secret" } })]);
  assert.deepEqual(result.keys[0].headers, { "X-Static": "fixed" });
  assert.equal(result.keys[0].defaultModel, "model");
  assert.equal(result.keys[0].url, "https://example.com/v1");
  assert.throws(() => buildCatalog([provider({ headers: { "X-Test": "$request" } })]), /a header value cannot start with \$/);
  for (const url of ["http://localhost/v1", "https://example.com:444/v1", "https://example.com/v1?key=x"]) {
    assert.throws(() => buildCatalog([provider({ getModels: () => [{ id: "model", api: "openai-completions", baseUrl: url }] })]), /invalid URL/);
  }
  assert.throws(() => buildCatalog([provider({ id: "bad/provider" })]), /invalid provider ID/);
  assert.throws(() => buildCatalog([provider({ getModels: () => [{ id: "fixture" }] })]), /invalid model ID/);
});
