export interface WidthFailure {
  fixture: "full" | "empty";
  route: string;
  width: number;
  element: string;
  text: string;
  occurrences?: number;
}

export function matchKnownFailure(baseline: readonly WidthFailure[], issue: WidthFailure): number {
  return baseline.findIndex(item => item.fixture === issue.fixture && item.route === issue.route && item.width === issue.width &&
    item.element === issue.element && item.text === issue.text);
}

export function staleWidthFailures(baseline: readonly WidthFailure[], seen: ReadonlySet<number>): WidthFailure[] {
  return baseline.filter((_, index) => !seen.has(index));
}

// Temporary baseline. Sweep PRs remove each issue when they fix it.
export const knownWidthFailures: readonly WidthFailure[] = [
  { fixture: "empty", route: "/r/:room/s/:session", width: 768, element: "strong.svelte-1n46o8q", text: "root" },
  { fixture: "full", route: "/r/:room/s/:session", width: 768, element: "strong.svelte-1n46o8q", text: "root" },
  { fixture: "full", route: "/r/:room/s/:session", width: 768, element: "strong.svelte-1n46o8q", text: "lead-web" },
  { fixture: "full", route: "/r/:room/s/:session", width: 768, element: "strong.svelte-1n46o8q", text: "builder-feed" },
  { fixture: "full", route: "/r/:room/s/:session", width: 768, element: "strong.svelte-1n46o8q", text: "sync-review" },
  { fixture: "full", route: "/settings/devices", width: 390, element: "span.line.svelte-11q8u0f", text: "Expires in 2 days · Seen 28 days ago · Read, operate" },
  { fixture: "full", route: "/r/:room", width: 768, element: "span.title.svelte-1cx619", text: "asks for host access", occurrences: 2 },
];
