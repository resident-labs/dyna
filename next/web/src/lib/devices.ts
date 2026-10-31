import type { Device, Scope } from "./contract.gen.ts";

// Every scope in the order Settings lists it, with v1's words.
export const scopes: readonly { scope: Scope; label: string; about: string }[] = [
  { scope: "read", label: "Read", about: "See rooms, transcripts and sessions." },
  { scope: "operate", label: "Operate", about: "Post, answer, stop work and steer sessions." },
  { scope: "files", label: "Files", about: "Read workspace files and browse paths." },
  { scope: "terminal", label: "Terminal", about: "Open a shell and type into it." },
  { scope: "computers", label: "Computers", about: "Watch computers and control their screens." },
  { scope: "admin", label: "Admin", about: "Change settings and pair or revoke devices." },
];

export const isLoopback = (hostname: string) => hostname === "localhost" || hostname.endsWith(".localhost") || hostname.startsWith("127.") || hostname === "[::1]";

// Spec 7.8: a loopback page is the owner's own machine, so its codes start with every scope.
export const startingScopes = (hostname: string): Scope[] => isLoopback(hostname) ? scopes.map(item => item.scope) : ["read", "operate"];

// Where a phone can reach this engine: this page's own origin, unless the page is on loopback, which only the
// engine's machine can open. Then it is a configured route, and with none no address exists to pair through.
export const pairAddresses = (origin: string, hostname: string, routes: readonly string[]): readonly string[] => isLoopback(hostname) ? routes : [origin];

const codeLength = 10;
// A code in the form the engine compares, from anything typed or pasted, with or without the dash.
export const cleanCode = (text: string) => text.toUpperCase().replace(/[^A-Z0-9]/g, "").slice(0, codeLength);
export const isFullCode = (code: string) => cleanCode(code).length === codeLength;
export const showCode = (code: string) => { const clean = cleanCode(code); return clean.length > 5 ? `${clean.slice(0, 5)}-${clean.slice(5)}` : clean; };
export const pairLink = (address: string, code: string) => `${address}/#pair=${cleanCode(code)}`;
// The code a pairing link carries, or "" when the address holds none that could work.
export function linkCode(hash: string) {
  const code = cleanCode(new URLSearchParams(hash.slice(1)).get("pair") ?? "");
  return isFullCode(code) ? code : "";
}

export const secondsLeft = (expiresAt: string, now: number) => Math.max(0, Math.ceil((Date.parse(expiresAt) - now) / 1000));
export const clock = (seconds: number) => `${Math.floor(seconds / 60)}:${String(seconds % 60).padStart(2, "0")}`;

// This device first, then the newest, so a new device shows right after it.
export const listed = (devices: readonly Device[]) => [...devices].sort((a, b) => Number(b.current) - Number(a.current) || Date.parse(b.pairedAt) - Date.parse(a.pairedAt));

export function access(granted: readonly Scope[]) {
  if (granted.length >= scopes.length) return "All access";
  const words = scopes.filter(item => granted.includes(item.scope)).map(item => item.label.toLowerCase()).join(", ");
  return words ? words[0].toUpperCase() + words.slice(1) : "No access";
}

const minute = 60_000, hour = 60 * minute, day = 24 * hour;
export function seen(device: Device, now: number) {
  if (device.current) return "Active now";
  const gap = now - Date.parse(device.lastSeenAt);
  if (gap < minute) return "Seen just now";
  if (gap < hour) return `Seen ${Math.floor(gap / minute)} min ago`;
  if (gap < day) return `Seen ${Math.floor(gap / hour)} h ago`;
  const days = Math.floor(gap / day);
  return `Seen ${days} ${days === 1 ? "day" : "days"} ago`;
}

// The warning line once 90% of the 30 days have passed, which is at 3 days left.
export function expiring(expiresAt: string, now: number) {
  const days = Math.ceil((Date.parse(expiresAt) - now) / day);
  if (days > 3) return undefined;
  return days <= 1 ? "Expires today" : `Expires in ${days} days`;
}

export const isPhone = (name: string) => /iphone|ipad|android|pixel|phone|mobile|tablet/i.test(name);

// What the browser can tell about itself. The heading names only a device people call by that name; iPads often
// report as a Mac, so the touch points tell them apart.
export function thisBrowser(agent: string, touchPoints: number): { name: string; named: boolean } {
  if (/iPhone/.test(agent)) return { name: "iPhone", named: true };
  if (/iPad/.test(agent) || (/Macintosh/.test(agent) && touchPoints > 1)) return { name: "iPad", named: true };
  if (/Android/.test(agent)) return { name: /Mobile/.test(agent) ? "Android phone" : "Android tablet", named: true };
  const browser = [["Firefox/", "Firefox"], ["Edg/", "Edge"], ["Chrome/", "Chrome"], ["Safari/", "Safari"]].find(([mark]) => agent.includes(mark));
  return { name: browser?.[1] ?? "Browser", named: false };
}
