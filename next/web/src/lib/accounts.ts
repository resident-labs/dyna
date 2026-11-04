import type { Account, AccountProviderInfo, AccountUsage, ConnectFlow, ConnectMethod, RoomSummary, SessionAccountIssue, SessionAccountResponse } from "./contract.gen.ts";
import { APIError } from "./client.ts";
import type { Chip } from "./settings-list.ts";
import { balancesText, causeText, visibleWindows, windowMeter } from "./usage.ts";

// An account as Settings > Accounts lists it (spec 5.13): its provider from the catalog, which an account of a
// provider the catalog no longer has lacks, its name on screen, and its usage from accounts.quota when that has it.
export type AccountItem = { account: Account; provider?: AccountProviderInfo; name: string; usage?: AccountUsage };

// Every account by provider in the catalog's order, an unknown provider last; in a provider an expired sign-in first,
// then low usage, then by label, its numbers as numbers. An account without a label is named by its provider and its place there.
export function accountItems(accounts: readonly Account[], providers: readonly AccountProviderInfo[], usage: readonly AccountUsage[]): AccountItem[] {
  const place = (account: Account) => { const index = providers.findIndex(p => p.id === account.provider); return index < 0 ? providers.length : index; };
  const rank = (account: Account) => account.state === "sign_in_expired" ? 0 : account.state === "quota_low" ? 1 : 2;
  const sorted = [...accounts].sort((a, b) => place(a) - place(b) || a.provider.localeCompare(b.provider) || rank(a) - rank(b) || a.label.localeCompare(b.label, undefined, { numeric: true }));
  const seen = new Map<string, number>();
  return sorted.map(account => {
    const provider = providers.find(p => p.id === account.provider);
    const nth = (seen.get(account.provider) ?? 0) + 1;
    seen.set(account.provider, nth);
    return { account, provider, name: account.label || `${provider?.displayName ?? account.provider} account ${nth}`, usage: usage.find(u => u.accountId === account.id) };
  });
}

export const accountChips: Chip<AccountItem>[] = [
  { id: "all", label: "All" },
  { id: "expired", label: "Needs sign-in", test: item => item.account.state === "sign_in_expired" },
  { id: "limited", label: "Limited to rooms", test: item => !!item.account.rooms?.length },
];

// The typed alerts are the only colored account states (spec 5.12).
export function accountPill(account: Account): { tone: "error" | "warning"; label: string } | undefined {
  if (account.state === "sign_in_expired") return { tone: "error", label: "Sign-in expired" };
  if (account.state === "quota_low") return { tone: "warning", label: "Low usage" };
  return undefined;
}

// A row's second line: the rooms it serves, then one value for a state the row's bars cannot show.
export function accountLine({ account, usage }: AccountItem): string {
  const n = account.rooms?.length ?? 0;
  const rooms = n ? `${n.toLocaleString("en")} ${n === 1 ? "room" : "rooms"}` : "Every room";
  const state = account.state === "unknown" ? "not checked yet" : account.state === "failed" ? "usage unread"
    : usage?.cause === "insufficient_balance" ? "out of credit" : usage?.balances?.length ? balancesText(usage.balances) : "";
  return state ? `${rooms} · ${state}` : rooms;
}

const expired = ({ account, usage }: AccountItem) => account.state === "sign_in_expired" || usage?.cause === "sign_in_expired";

// A row's usage at a glance: a bar for each of the first three windows, warm as Home's tiles are, and the highest
// percent used of all of them. An expired sign-in or any usage cause has no reading to trust, so it gets none.
export function usageHint(item: AccountItem, now: number): { bars: { label: string; percent: number; busy: boolean }[]; peak: number } | undefined {
  if (expired(item) || item.usage?.cause) return undefined;
  const meters = visibleWindows(item.usage?.windows ?? []).map(w => windowMeter(w, now));
  if (!meters.length) return undefined;
  return { bars: meters.slice(0, 3).map(({ label, percent, busy }) => ({ label, percent, busy })), peak: Math.max(...meters.map(m => m.percent)) };
}

// Why an account's detail shows no meter and no balance; none when it shows one.
export function usageNote(item: AccountItem): string | undefined {
  if (expired(item)) return "Limits show once it is signed in again.";
  if (item.account.state === "unknown") return "Not checked yet.";
  if (item.account.state === "failed") return "Usage could not be read.";
  const cause = causeText(item.usage?.cause ?? "");
  if (cause) return cause;
  return visibleWindows(item.usage?.windows ?? []).length || item.usage?.balances?.length ? undefined : "This provider reports no usage limits.";
}

const flows: readonly ConnectFlow[] = ["callback", "device", "key"];

// The providers Add offers: plan sign-ins, then codes, then keys, of which the first ten show until a search or Show
// more, as the key list is long. more is how many keys wait behind Show more.
export function providerChoices(providers: readonly AccountProviderInfo[], query: string, expanded: boolean): { items: AccountProviderInfo[]; more: number } {
  const search = query.trim().toLowerCase();
  const matches = flows.flatMap(flow => providers.filter(p => p.connectFlow === flow && p.displayName.toLowerCase().includes(search)));
  const keys = matches.filter(p => p.connectFlow === "key").length;
  const hidden = !search && !expanded ? Math.max(0, keys - 10) : 0;
  return { items: matches.slice(0, matches.length - hidden), more: hidden };
}

// The rooms a Serves picker draws: those that match the search, in any case, the chosen first, at most cap of them,
// and how many more match.
export function roomChoices(rooms: readonly RoomSummary[], chosen: readonly string[], query: string, cap: number): { items: RoomSummary[]; more: number } {
  const search = query.trim().toLowerCase();
  const found = rooms.filter(room => room.name.toLowerCase().includes(search));
  const ordered = [...found.filter(room => chosen.includes(room.id)), ...found.filter(room => !chosen.includes(room.id))];
  return { items: ordered.slice(0, cap), more: Math.max(0, ordered.length - cap) };
}

// An account's item in the Accounts list page. Home links here, so an expired sign-in is a click from Sign in again.
export const accountPath = (id: string) => "/settings/accounts/" + encodeURIComponent(id);

export const reconnectUnavailable = (error: unknown) => error instanceof APIError && error.detail.code === "reconnect_unavailable";

export const canReconnect = (account: Account, method: ConnectMethod) => account.state === "sign_in_expired" && method === "oauth";

// What Remove's confirm says it deletes.
export const removalPrompt = (method: ConnectMethod) => `Dyna deletes its ${method === "key" ? "key" : "sign-in"} where Dyna runs.`;

export const refused = (error: unknown) => error instanceof APIError && error.detail.code === "conflict";

// The engine alone decides what a label may be, so the page only words its answer.
export const labelRefusal = (error: unknown) => error instanceof APIError && error.detail.code === "invalid" ? "Use 1 to 128 characters, without @." : "The label was not saved. Try again.";

// A session's Account row. A removed account has no label left to show.
export const accountChoiceName = (account: SessionAccountResponse) => !account.account ? "Automatic" : account.label || "Removed account";

const issueNotes: Record<SessionAccountIssue, string> = {
  removed: "Removed. Using Automatic.", signed_out: "Signed out. Using Automatic.",
  spent: "Limit reached. Using Automatic.", unavailable: "Unavailable right now. Using Automatic.",
};

export const accountChoiceNote = (issue?: SessionAccountIssue) => issue ? issueNotes[issue] : "";

// Model IDs as the owner types them, on lines or with commas.
export const parseModelIDs = (text: string) => [...new Set(text.split(/[\s,]+/).filter(Boolean))];

// Why a new provider was refused, in the owner's words.
export function addProviderError(error: unknown): string {
  if (error instanceof APIError && error.detail.code === "models_unavailable") return "This provider does not list its models. Enter the model IDs below.";
  if (error instanceof APIError && error.detail.code === "invalid") return "Check the name and address. The name cannot be one Dyna already has.";
  return "Could not save this provider. Try again.";
}
