import type { ModelProvider, ProfileSummary, ThinkingLevel } from "./contract.gen.ts";

// A person reads a profile, not its slugs, as in v1's profileLabels.ts: the slug's words as a name, with SHOWN_AS
// for those plain capitals get wrong. v1 named the harness; v2 has one, so the label names the provider and thinking.
const SHOWN_AS: Record<string, string> = { gpt: "GPT", glm: "GLM", mimo: "MiMo", deepseek: "DeepSeek" };
const providers: Record<ModelProvider, string> = { anthropic: "Anthropic", openai: "OpenAI" };
const thinking: Record<ThinkingLevel, string> = {
  off: "no thinking", minimal: "minimal thinking", low: "low thinking", medium: "medium thinking", high: "high thinking", xhigh: "very high thinking",
};

// Preserve the profile’s other allowed models and hidden limits when the owner edits its model.
export function profileForSave(draft: ProfileSummary) {
  const { inUse: _inUse, ...profile } = draft;
  const model = profile.model.trim();
  return { ...profile, name: draft.name.trim(), model, allowedModels: Array.from(new Set([...(draft.allowedModels ?? []), model])),
    compactTokens: draft.compactTokens ?? 0, maxConcurrent: draft.maxConcurrent ?? 0, maxCallsPerRun: draft.maxCallsPerRun ?? 0 };
}

// "gpt-6.1-sol" is "GPT 6.1 Sol". Neighboring numbers are one version: "claude-opus-5-5" is "Claude Opus 5.5".
export function slugName(slug: string): string {
  const word = (part: string) => SHOWN_AS[part.toLowerCase()] ?? (/^v?\d/.test(part) ? part : part[0].toUpperCase() + part.slice(1).toLowerCase());
  const parts = (slug.split("/").pop() ?? "").split(/[-_\s]+/).filter(Boolean);
  return parts.map(word).join(" ").replace(/(?<=\d) (?=\d)/g, ".");
}

// One profile for the owner: the model, such as "Claude Opus 5.5", over "Anthropic · high thinking". Profiles that would
// read the same add their own name, so they still read apart.
export function profileLabel(profile: ProfileSummary, all: ProfileSummary[]): { model: string; detail: string } {
  const detail = (p: ProfileSummary) => `${providers[p.provider] ?? p.provider} · ${thinking[p.thinking]}`;
  const twin = all.some(other => other.name !== profile.name && other.model === profile.model && detail(other) === detail(profile));
  return { model: slugName(profile.model), detail: twin ? `${detail(profile)} · ${slugName(profile.name)}` : detail(profile) };
}
