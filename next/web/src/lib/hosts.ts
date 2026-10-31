import type { HostSummary } from "./contract.gen.ts";

export const hostLabel = (host: string) => host === "control-plane" ? "This computer" : host;

// One tile on a Home card: the label, the percent large with an optional muted unit after it, a bar, and the text.
export type Meter = { label: string; text: string; percent: number; busy: boolean; unit?: string };

// A host's CPU and memory meters, "3.1 of 16 cores" and "9.8 of 31 GB"; none while it reports no capacity.
// A meter at 90% of its limit or more is busy. GB counts 2^30 bytes, as v1's GiB did.
export function hostMeters(h: HostSummary): Meter[] {
  if (!h.cpus || !h.memBytes) return [];
  const gb = (bytes: number) => bytes / 2 ** 30;
  const meter = (label: string, used: number, limit: number, text: string) =>
    ({ label, text, percent: Math.min(100, Math.round(used / limit * 100)), busy: used >= 0.9 * limit });
  return [
    meter("CPU", h.cpuUsed, h.cpus, `${h.cpuUsed.toFixed(1)} of ${h.cpus} cores`),
    meter("Memory", h.memUsed, h.memBytes, `${gb(h.memUsed).toFixed(1)} of ${Math.round(gb(h.memBytes))} GB`),
  ];
}
