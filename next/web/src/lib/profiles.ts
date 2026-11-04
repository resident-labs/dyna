import type { Account, ModelProvider, Profile, ProfileEntry, ThinkingLevel } from "./contract.gen.ts";
import type { Chip, Column } from "./settings-list.ts";

// A profile as the web lists and edits it: profile.list's stored profile with its read-only in-use count beside it
// flattened in, and the limits an editor may leave unset. profileForSave drops inUse again.
export type ProfileSummary = Omit<Profile, "allowedModels" | "compactTokens" | "maxConcurrent"> & { inUse: number; allowedModels?: string[] | null; compactTokens?: number; maxConcurrent?: number };

export const profileRows = (entries: readonly ProfileEntry[] | null): ProfileSummary[] => (entries ?? []).map(entry => ({ ...entry.profile, inUse: entry.inUse }));

// A person reads a profile, not its slugs, as in v1's profileLabels.ts: the slug's words as a name, with SHOWN_AS
// for those plain capitals get wrong.
const SHOWN_AS: Record<string, string> = { gpt: "GPT", glm: "GLM", mimo: "MiMo", deepseek: "DeepSeek" };
export const thinking: Record<ThinkingLevel, string> = {
  off: "no thinking", minimal: "minimal thinking", low: "low thinking", medium: "medium thinking", high: "high thinking", xhigh: "very high thinking",
};
export const providers: Record<ModelProvider, string> = { anthropic: "Anthropic", openai: "OpenAI-compatible" };

// Settings > Profiles (spec 5.13): its chips, its columns, its provider filter and what a row and the search read.
export const unrated = (profile: ProfileSummary) => !profile.taste && !profile.diagnostics && !profile.intelligence && !profile.cost;
export const profileChips: Chip<ProfileSummary>[] = [
  { id: "all", label: "All" }, { id: "inuse", label: "In use", test: profile => profile.inUse > 0 }, { id: "unrated", label: "Not rated", test: unrated },
];
// The values sort as numbers; an unset one is 0, so it sorts lowest.
const byNumber = (value: (profile: ProfileSummary) => number | undefined) => (a: ProfileSummary, b: ProfileSummary) => (value(a) ?? 0) - (value(b) ?? 0);
export const profileColumns: Column<ProfileSummary>[] = [
  { id: "name", label: "Name", width: "minmax(0, 1fr)", compare: (a, b) => a.name.localeCompare(b.name) },
  { id: "taste", label: "Taste", width: "48px", compare: byNumber(p => p.taste) },
  { id: "diagnostics", label: "Diag.", width: "48px", compare: byNumber(p => p.diagnostics) },
  { id: "intelligence", label: "Intel.", width: "48px", compare: byNumber(p => p.intelligence) },
  { id: "cost", label: "Thrift", width: "48px", compare: byNumber(p => p.cost) },
  { id: "maxConcurrent", label: "At once", width: "60px", compare: byNumber(p => p.maxConcurrent) },
  { id: "inUse", label: "In use", width: "56px", compare: byNumber(p => p.inUse) },
];
export const providerFilter = (provider: ModelProvider | "") => provider ? (profile: ProfileSummary) => profile.provider === provider : undefined;
export const profileText = (profile: ProfileSummary) => `${profile.name} ${profile.model} ${providers[profile.provider]}`;
// A row's second line; a narrow row has no columns, so it names what they would have shown.
export function profileLine(profile: ProfileSummary, compact: boolean): string {
  const line = `${profile.model} · ${thinking[profile.thinking]}`;
  if (!compact) return line;
  return line + (unrated(profile) ? " · not rated" : "") + (profile.inUse ? ` · ${profile.inUse} in use` : "");
}

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
