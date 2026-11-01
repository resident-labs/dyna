import { readFileSync, writeFileSync } from "node:fs";
import { pathToFileURL } from "node:url";
import { builtinProviders } from "../packages/ai/dist/providers/all.js";
import { defaultModelPerProvider } from "./dyna-defaults.mjs";

export function buildCatalog(providers, compiled = JSON.parse(readFileSync(new URL("../../cliproxyapi/internal/registry/models/models.json", import.meta.url), "utf8"))) {
  if (providers.length === 0 || providers.length > 200) throw new Error("invalid provider count");
  const plans = [], keys = [], providerIDs = new Set();
  const planIDs = { anthropic: ["anthropic", "claude"], "openai-codex": ["openai", "codex-free", "codex-team", "codex-plus", "codex-pro"], "kimi-coding": ["kimi-coding", "kimi"], xai: ["xai", "xai"] };
  const aliases = { "kimi-coding": { k3: "kimi-k3", k2p6: "kimi-k2.6", "kimi-for-coding": "kimi-k2.8-code" } };
  for (const [id, mapped] of Object.entries(aliases)) {
    const available = new Set(planIDs[id].slice(1).flatMap((tier) => compiled[tier].map((model) => model.id)));
    for (const [piID, proxyID] of Object.entries(mapped)) if (!available.has(proxyID)) throw new Error(`${id}/${piID}: mapped proxy model ${proxyID} is missing`);
  }
  for (const provider of providers) {
    const id = provider.id, models = provider.getModels(), modelIDs = new Set();
    if (!/^[a-z0-9][a-z0-9.-]{0,63}$/.test(id) || providerIDs.has(id) || models.length > 500) throw new Error(`${id}: invalid or duplicate provider ID or model count`);
    providerIDs.add(id);
    for (const model of models) {
      if (!/^[!-~]{1,200}$/.test(model.id) || model.id === id || modelIDs.has(model.id)) throw new Error(`${id}: invalid or duplicate model ID`);
      modelIDs.add(model.id);
    }
    let reason = "";
    const headers = {};
    for (const [name, value] of Object.entries(provider.headers ?? {})) {
      if (typeof value !== "string") continue;
      if (value.startsWith("$")) { reason = "a header value cannot start with $"; break; }
      if (/auth|key|token|cookie/i.test(name) || name.toLowerCase() === "host") { reason = "a header names a secret"; break; }
      if (value.length > 256 || !/^[ -~]*$/.test(value)) { reason = "invalid header value"; break; }
      headers[name] = value;
    }
    if (Object.keys(headers).length > 8) reason = "too many headers";
    if (reason) { console.warn(`${id}: ${reason}`); continue; }
    if (Object.hasOwn(planIDs, id)) {
      const [planID, ...tiers] = planIDs[id];
      const available = new Set(tiers.flatMap((tier) => compiled[tier].map((model) => model.id)));
      const planModels = [];
      for (const model of models) {
        const proxyID = aliases[id]?.[model.id] ?? model.id;
        if (!available.has(proxyID)) { console.warn(`${id}/${model.id}: no proxy model`); continue; }
        planModels.push({ id: model.id, name: model.name, proxyID });
      }
      plans.push({ id: planID, defaultModel: defaultModelPerProvider[id], models: planModels });
    }
    if (!provider.auth.apiKey?.name || id === "github-copilot") { console.warn(`${id}: no supported key flow`); continue; }
    const completions = models.filter((model) => model.api === "openai-completions");
    if (!completions.length) { console.warn(`${id}: no completions models`); continue; }
    let base = "";
    for (const model of completions) {
      let url;
      try { url = new URL(model.baseUrl); } catch { reason = "invalid URL"; break; }
      if (/[{}$#?]/.test(model.baseUrl) || url.protocol !== "https:" || !url.hostname || url.username || url.password || url.search || (url.port && url.port !== "443") || (base && base !== model.baseUrl)) { reason = "invalid or multiple URLs"; break; }
      base = model.baseUrl;
    }
    if (reason) { console.warn(`${id}: ${reason}`); continue; }
    const defaultModel = completions.some((model) => model.id === defaultModelPerProvider[id]) ? defaultModelPerProvider[id] : completions[0].id;
    keys.push({ provider: id, displayName: provider.name, keyName: provider.auth.apiKey.name, defaultModel, url: base, headers, models: completions.map(({ id, name }) => ({ id, name })) });
  }
  return { version: 1, plans, keys };
}

if (import.meta.url === pathToFileURL(process.argv[1]).href) writeFileSync(process.argv[2], JSON.stringify(buildCatalog(builtinProviders())) + "\n");
