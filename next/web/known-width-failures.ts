export function staleWidthFailures(baseline: Readonly<Record<string, readonly number[]>>, seen: ReadonlySet<string>): string[] {
  return Object.entries(baseline).flatMap(([route, widths]) => widths.map(width => `${route} ${width}`).filter(key => !seen.has(key)));
}

// Temporary baseline. Sweep PRs remove routes and widths as they fix them.
export const knownWidthFailures: Readonly<Record<string, readonly number[]>> = {
  "/r/:room": [768],
  "/settings/devices": [390],
  "/settings/profiles": [768, 920],
};
