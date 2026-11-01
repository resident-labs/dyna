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
  { fixture: "full", route: "/r/:room", width: 768, element: "span.title.svelte-1cx619", text: "asks for host access", occurrences: 2 },
];
