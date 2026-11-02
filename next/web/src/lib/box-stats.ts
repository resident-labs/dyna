import { call } from "./client.ts";
import type { BoxReading, BoxStatsResponse } from "./contract.gen.ts";
import { count } from "./feed.ts";
import { fixed, gb, meter, whole, type Meter } from "./hosts.ts";

// Under a gigabyte a box's memory reads in MB, since "0.0 of 2 GB" would look like a box with nothing in it.
const small = (bytes: number) => `${Math.max(1, Math.round(bytes / 2 ** 20)).toLocaleString()} MB`;

// A box's CPU and memory meters, in the order of a host's, "0.4 of 2 cores" and "1.2 of 4 GB". None without a reading.
export function boxMeters(size: BoxStatsResponse["size"], reading?: BoxReading | null): Meter[] {
  if (!reading || !size.cpus || !size.memBytes) return [];
  const memory = gb(reading.memUsed), limit = whole(gb(size.memBytes));
  return [
    meter("CPU", reading.cpuUsed, size.cpus, `${fixed(reading.cpuUsed)} of ${count(size.cpus, "core")}`),
    meter("Memory", reading.memUsed, size.memBytes, memory < 1 ? `${small(reading.memUsed)} of ${limit} GB` : `${fixed(memory)} of ${limit} GB`),
  ];
}

// What the box's own volume holds, "12.4 GB". A measure that has not finished, or that failed, gives no figure.
export function boxDisk(reading?: BoxReading | null): string | undefined {
  if (!reading?.diskUsed) return undefined;
  return gb(reading.diskUsed) < 1 ? small(reading.diskUsed) : `${fixed(gb(reading.diskUsed))} GB`;
}

type Read = (session: string) => Promise<BoxStatsResponse>;

// box.stats for one session: once now, for its size, and then every 5 s while its box runs and the page is visible. CPU
// is an average since the last read, so a slower beat would blur it. A tick skips while a call is out, so calls never
// pile up on a silent box. Nothing else reads a box, so a closed panel costs that box nothing. It returns the stop function.
export function watchBoxStats(session: string, running: boolean, apply: (stats: BoxStatsResponse) => void, fail: () => void,
  read: Read = id => call("box.stats", { session: id })): () => void {
  let timer: ReturnType<typeof setInterval> | undefined;
  let loading = false, stopped = false;
  const tick = () => {
    if (loading) return;
    loading = true;
    void read(session).then(stats => { if (!stopped) apply(stats); }, () => { if (!stopped) fail(); }).finally(() => { loading = false; });
  };
  const sync = () => { clearInterval(timer); if (document.hidden) return; tick(); if (running) timer = setInterval(tick, 5_000); };
  sync();
  document.addEventListener("visibilitychange", sync);
  return () => { stopped = true; clearInterval(timer); document.removeEventListener("visibilitychange", sync); };
}
