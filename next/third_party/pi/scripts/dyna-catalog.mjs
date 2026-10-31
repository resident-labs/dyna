import { writeFileSync } from "node:fs";
import { builtinProviders } from "../packages/ai/dist/providers/all.js";

const providers = builtinProviders().map((provider) => ({
  id: provider.id,
  name: provider.name,
  keyName: provider.auth.apiKey?.name,
  headers: Object.fromEntries(Object.entries(provider.headers ?? {}).filter(([, value]) => typeof value === "string")),
  models: provider.getModels().map(({ id, name, api, baseUrl, reasoning, contextWindow, maxTokens }) => ({
    id, name, api, baseUrl, reasoning, contextWindow, maxTokens,
  })),
}));
writeFileSync(process.argv[2], JSON.stringify({ version: 1, providers }) + "\n");
