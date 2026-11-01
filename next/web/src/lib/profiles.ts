import type { Account, ModelProvider, ProfileSummary, ThinkingLevel } from "./contract.gen.ts";

// A person reads a profile, not its slugs, as in v1's profileLabels.ts: the slug's words as a name, with SHOWN_AS
// for those plain capitals get wrong.
const SHOWN_AS: Record<string, string> = { gpt: "GPT", glm: "GLM", mimo: "MiMo", deepseek: "DeepSeek" };
const thinking: Record<ThinkingLevel, string> = {
  off: "no thinking", minimal: "minimal thinking", low: "low thinking", medium: "medium thinking", high: "high thinking", xhigh: "very high thinking",
};

// Preserve the profile’s other allowed models and hidden limits when the owner edits its model.
export function profileForSave(draft: ProfileSummary) {
  const { inUse: _inUse, ...profile } = draft;
  const model = profile.model.trim();
  return { ...profile, name: draft.name.trim(), model, allowedModels: Array.from(new Set([...(draft.allowedModels ?? []), model])),
    compactTokens: draft.compactTokens ?? 0, maxConcurrent: draft.maxConcurrent ?? 0 };
}

// Where a profile's sessions can go when it is removed, named as the session picker names them.
export function replacements(profiles: readonly ProfileSummary[], name: string): { name: string; label: string }[] {
  return profiles.filter(profile => profile.name !== name).map(profile => {
    const shown = profileLabel(profile);
    return { name: profile.name, label: `${shown.name} · ${shown.detail}` };
  });
}

export const sessionCount = (count: number) => `${count} ${count === 1 ? "session" : "sessions"}`;

export type ProfileModel = { id: string; provider: ModelProvider };

// The models the proxy routes for connected accounts. Only Claude speaks Anthropic's API; everything else goes through
// the OpenAI-compatible route, including a provider the owner added.
export function profileModels(accounts: readonly Account[]): ProfileModel[] {
  const found = new Map<string, ProfileModel>();
  for (const account of accounts) {
    for (const id of account.models ?? []) found.set(id, { id, provider: account.provider === "anthropic" ? "anthropic" : "openai" });
  }
  return [...found.values()].sort((a, b) => a.id.localeCompare(b.id));
}

// "gpt-6.1-sol" is "GPT 6.1 Sol". Neighboring numbers are one version: "claude-opus-5-5" is "Claude Opus 5.5".
export function slugName(slug: string): string {
  const word = (part: string) => SHOWN_AS[part.toLowerCase()] ?? (/^v?\d/.test(part) ? part : part[0].toUpperCase() + part.slice(1).toLowerCase());
  const parts = (slug.split("/").pop() ?? "").split(/[-_\s]+/).filter(Boolean);
  return parts.map(word).join(" ").replace(/(?<=\d) (?=\d)/g, ".");
}

// One profile for the owner: its name, such as "Opus Rewrite", over "high thinking".
export function profileLabel(profile: ProfileSummary): { name: string; detail: string } {
  return { name: slugName(profile.name), detail: thinking[profile.thinking] };
}
