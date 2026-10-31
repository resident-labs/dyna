import type { Account, ConnectMethod } from "./contract.gen.ts";
import { APIError } from "./client.ts";

export const canReconnect = (account: Account, method: ConnectMethod) => account.state === "sign_in_expired" && method === "oauth";

export const removalPrompt = (method: ConnectMethod) => `Remove this account? Dyna deletes its ${method === "key" ? "key" : "sign-in"}. You can connect it again later.`;

export const refused = (error: unknown) => error instanceof APIError && error.detail.code === "conflict";

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
