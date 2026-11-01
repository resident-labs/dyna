import type { Account, AccountProviderInfo, ConnectFlow, ConnectMethod } from "./contract.gen.ts";
import { APIError } from "./client.ts";

// Search always unfolds the key group, so a matching provider stays reachable.
export function providerGroup(providers: readonly AccountProviderInfo[], flow: ConnectFlow, query: string, expanded: boolean): { items: AccountProviderInfo[]; remaining: number; count: number } {
  const search = query.trim().toLowerCase();
  const matches = providers.filter(p => p.connectFlow === flow && p.displayName.toLowerCase().includes(search));
  const items = flow === "key" && !search && !expanded ? matches.slice(0, 10) : matches;
  return { items, remaining: matches.length - items.length, count: matches.length };
}

export const accountPath = (id: string) => "/settings/accounts/account/" + encodeURIComponent(id);

// Opens the sign-in for one account. Home links here, so an expired sign-in is a click from the sign-in button.
export const reconnectPath = (id: string) => "/settings/accounts/reconnect/" + encodeURIComponent(id);

export const reconnectUnavailable = (error: unknown) => error instanceof APIError && error.detail.code === "reconnect_unavailable";

export const canReconnect = (account: Account, method: ConnectMethod) => account.state === "sign_in_expired" && method === "oauth";

export const removalPrompt = (method: ConnectMethod) => `Remove this account? Dyna deletes its ${method === "key" ? "key" : "sign-in"}. You can connect it again later.`;

export const refused = (error: unknown) => error instanceof APIError && error.detail.code === "conflict";

// The engine alone decides what a label may be, so the page only words its answer.
export const labelRefusal = (error: unknown) => error instanceof APIError && error.detail.code === "invalid" ? "Use 1 to 128 characters, without @." : "The label was not saved. Try again.";

// Problems come first; unknown accounts remain in label order with connected accounts.
export function accountRows(accounts: readonly Account[]): Account[] {
  const rank = (account: Account) => account.state === "sign_in_expired" ? 0 : account.state === "quota_low" ? 1 : 2;
  return [...accounts].sort((a, b) => rank(a) - rank(b) || a.label.localeCompare(b.label));
}

export function accountState(account: Account): { line?: string; pill?: { tone: "error" | "warning"; label: string } } {
  switch (account.state) {
    case "unknown": return { line: "Not checked yet" };
    case "sign_in_expired": return { pill: { tone: "error", label: "Sign-in expired" } };
    case "quota_low": return { pill: { tone: "warning", label: "Low usage" } };
    case "failed": return { line: "Usage could not be read." };
    case "connected": return {};
  }
}

// Model IDs as the owner types them, on lines or with commas.
export const parseModelIDs = (text: string) => [...new Set(text.split(/[\s,]+/).filter(Boolean))];

// Why a new provider was refused, in the owner's words.
export function addProviderError(error: unknown): string {
  if (error instanceof APIError && error.detail.code === "models_unavailable") return "This provider does not list its models. Enter the model IDs below.";
  if (error instanceof APIError && error.detail.code === "invalid") return "Check the name and address. The name cannot be one Dyna already has.";
  return "Could not save this provider. Try again.";
}
