import { call } from "./client.ts";
import type { HostSummary } from "./contract.gen.ts";

export const hostLabel = (host: string) => host === "control-plane" ? "This computer" : host;

// One tile on a Home card: the label, the percent large with an optional muted unit after it, a bar, and the text.
export type Meter = { label: string; text: string; percent: number; busy: boolean; unit?: string };

// A host's CPU, memory and disk meters, "3.1 of 16 cores", "9.8 of 31 GB" and "412 GB free"; none while it reports
// no capacity, and no disk tile without a disk figure. Figures of 100 or more drop the decimal, so a tile's text
// stays on one line, and large ones group their digits ("1,348 GB free"). A meter at 90% of its limit or more is busy. GB counts 2^30 bytes, as v1's GiB did.
export function hostMeters(h: HostSummary): Meter[] {
  if (!h.cpus || !h.memBytes) return [];
  const gb = (bytes: number) => bytes / 2 ** 30;
  const whole = (n: number) => Math.round(n).toLocaleString();
  const fixed = (n: number) => n >= 100 ? whole(n) : n.toFixed(1);
  const meter = (label: string, used: number, limit: number, text: string) =>
    ({ label, text, percent: Math.min(100, Math.round(used / limit * 100)), busy: used >= 0.9 * limit });
  return [
    meter("CPU", h.cpuUsed, h.cpus, `${fixed(h.cpuUsed)} of ${whole(h.cpus)} cores`),
    meter("Memory", h.memUsed, h.memBytes, `${fixed(gb(h.memUsed))} of ${whole(gb(h.memBytes))} GB`),
    ...h.diskBytes ? [meter("Disk", h.diskUsed, h.diskBytes, `${whole(gb(h.diskBytes - h.diskUsed))} GB free`)] : [],
  ];
}

// "Last seen 4 min ago" for a host that stopped answering, from its last beat; undefined when it never answered.
export function lastSeen(h: HostSummary, now: number): string | undefined {
  if (!h.lastSeen) return undefined;
  const minutes = Math.max(0, Math.round((now - Date.parse(h.lastSeen)) / 60_000));
  return "Last seen " + (minutes < 1 ? "just now" : minutes < 60 ? `${minutes} min ago` : minutes < 1440 ? `${Math.round(minutes / 60)} h ago` : `${Math.round(minutes / 1440)} d ago`);
}

// The sessions panel's foot: at most 3 hosts. Unreachable hosts come first, then those low on disk, since they need
// attention; then the reachable ones that report load, busiest first by the higher of CPU and memory.
export function hostLoads(hosts: HostSummary[]): { name: string; reachable: boolean; diskLow: boolean; meters: Meter[] }[] {
  const load = (meters: Meter[]) => Math.max(...meters.map(m => m.percent));
  const loads = hosts.filter(h => h.reachable).map(h => ({ name: h.name, reachable: true, diskLow: h.diskLow, meters: hostMeters(h).slice(0, 2) }))
    .filter(h => h.diskLow || h.meters.length).sort((a, b) => Number(b.diskLow) - Number(a.diskLow) || load(b.meters) - load(a.meters));
  return [...hosts.filter(h => !h.reachable).map(h => ({ name: h.name, reachable: false, diskLow: h.diskLow, meters: [] })), ...loads].slice(0, 3);
}

// host.list now, then every 10 s to match the engine's beat, only while the page is visible. A tick skips while a
// call is out, so calls never pile up on a slow engine and answers arrive in order. It returns the stop function.
export function watchHosts(apply: (hosts: HostSummary[]) => void, fail: () => void): () => void {
  let timer: ReturnType<typeof setInterval> | undefined;
  let loading = false;
  const tick = () => {
    if (loading) return;
    loading = true;
    void call("host.list", {}).then(list => apply(list.hosts ?? []), fail).finally(() => { loading = false; });
  };
  const sync = () => { clearInterval(timer); if (document.hidden) return; tick(); timer = setInterval(tick, 10_000); };
  sync();
  document.addEventListener("visibilitychange", sync);
  return () => { clearInterval(timer); document.removeEventListener("visibilitychange", sync); };
}
