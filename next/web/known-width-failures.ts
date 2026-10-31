// Temporary baseline. Sweep PRs remove routes and widths as they fix them.
export const knownWidthFailures: Readonly<Record<string, readonly number[]>> = {
  "/r/:room": [768],
  "/settings/devices": [390],
  "/settings/profiles": [768, 920],
};
