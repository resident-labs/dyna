import { call } from "./client.ts";
import type { HostSummary, RoomShared } from "./contract.gen.ts";

export const hostLabel = (host: string) => host === "control-plane" ? "Dyna host" : host;

// A box's state with the machine that holds it, "Ready on arch". A box not placed yet has no machine to name.
export const onHost = (word: string, host?: string) => host ? `${word} on ${hostLabel(host)}` : word;

// A machine's sessions, one group per room, rooms by name. Each room keeps the engine's order, oldest first.
export const roomSessions = (h: HostSummary, roomName: (room: string) => string) =>
  [...Map.groupBy(h.sessions ?? [], session => session.room)].map(([room, sessions]) => ({ room, name: roomName(room), sessions }))
    .sort((a, b) => a.name.localeCompare(b.name));

// Where a popover hangs from its button in a viewport of the given size: under it, with the right edges lined up and
// at least 12 px from the viewport's, or over it when under leaves less than 240 px and over has more. It stops short
// of the viewport's edge, and its list scrolls. CSS for a fixed element, as the top layer has no parent to measure from.
export function hang(button: Pick<DOMRect, "top" | "right" | "bottom">, width: number, height: number): string {
  const gap = 6, edge = 16;
  const below = height - button.bottom - gap - edge, above = button.top - gap - edge, up = below < 240 && above > below;
  return `${up ? `top: auto; bottom: ${height - button.top + gap}px` : `top: ${button.bottom + gap}px`}; right: ${Math.max(12, width - button.right)}px; max-height: min(560px, 70vh, ${up ? above : below}px)`;
}

// One tile on a Home card: the label, the percent large with an optional muted unit after it, a bar, and the text.
export type Meter = { label: string; text: string; percent: number; busy: boolean; unit?: string };

// One card per machine. When the engine runs on a registered host, host.list reports that machine twice: the control
// plane with whole-machine figures, and the registered host with only dyna.slice's use of its allowance (cores less
// one, memory less the floor). They merge under the host's name with the whole-machine figures, the host's workspaces,
// sessions and shared folders, and whether the engine can reach it. Until the control plane's first sample, the host stands alone.
export function machines(hosts: HostSummary[]): HostSummary[] {
  const plane = hosts.find(h => h.name === "control-plane");
  const same = hosts.find(h => h.engineHost);
  if (!plane || !same || !hostMeters(plane).length) return hosts;
  const merged = { ...plane, name: same.name, reachable: same.reachable, lastSeen: same.lastSeen, boxes: plane.boxes + same.boxes,
    diskLow: plane.diskLow || same.diskLow, sessions: [...plane.sessions ?? [], ...same.sessions ?? []], shared: [...plane.shared ?? [], ...same.shared ?? []], engineHost: true };
  return hosts.filter(h => h !== same).map(h => h === plane ? merged : h);
}

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

// The rooms whose shared folder holds more than a quarter of the host's disk, largest first (spec 5.6). The folder has
// no hard limit, so Home names the room and the size for the owner to clean up. The engine sends null for no folders.
export const largeShared = (h: HostSummary): RoomShared[] =>
  h.diskBytes ? (h.shared ?? []).filter(folder => folder.bytes > h.diskBytes / 4).sort((a, b) => b.bytes - a.bytes) : [];

// A folder's size in whole GB of 2^30 bytes, as the meters count.
export const sharedSize = (bytes: number) => `${Math.round(bytes / 2 ** 30).toLocaleString()} GB`;

// The off-host backup problem Home marks, if any (spec 4.1): the stored failure, or a copy over 48 hours old. No copy
// yet, a copy in time and backups that are not configured all show nothing.
export function backupProblem(h: HostSummary, now: number): { tone: "warning" | "error"; label: string; title?: string } | undefined {
  if (!h.backupCopyEnabled) return undefined;
  if (h.backupCopyFailure) return { tone: "error", ...h.backupCopyFailure === "no_space" ? { label: "Backup machine full", title: "The second machine has no room for the backup" } : { label: "Backup failed" } };
  if (!h.backupCopyAt || now - Date.parse(h.backupCopyAt) <= 48 * 3_600_000) return undefined;
  return { tone: "warning", label: `Backup ${Math.floor((now - Date.parse(h.backupCopyAt)) / 86_400_000)} days old` };
}

// The sessions panel's foot: at most 3 hosts. Unreachable hosts come first, then those low on disk, since they need
// attention; then the reachable ones that report load, busiest first by the higher of CPU and memory.
export function hostLoads(hosts: HostSummary[]): { name: string; reachable: boolean; diskLow: boolean; meters: Meter[] }[] {
  const load = (meters: Meter[]) => Math.max(...meters.map(m => m.percent));
  const loads = hosts.filter(h => h.reachable).map(h => ({ name: h.name, reachable: true, diskLow: h.diskLow, meters: hostMeters(h).slice(0, 2) }))
    .filter(h => h.diskLow || h.meters.length).sort((a, b) => Number(b.diskLow) - Number(a.diskLow) || load(b.meters) - load(a.meters));
  return [...hosts.filter(h => !h.reachable).map(h => ({ name: h.name, reachable: false, diskLow: h.diskLow, meters: [] })), ...loads].slice(0, 3);
}

// host.list as machines, now and then every 10 s to match the engine's beat, only while the page is visible. A tick skips while a
// call is out, so calls never pile up on a slow engine and answers arrive in order. It returns the stop function.
export function watchHosts(apply: (hosts: HostSummary[]) => void, fail: () => void): () => void {
  let timer: ReturnType<typeof setInterval> | undefined;
  let loading = false;
  const tick = () => {
    if (loading) return;
    loading = true;
    void call("host.list", {}).then(list => apply(machines(list.hosts ?? [])), fail).finally(() => { loading = false; });
  };
  const sync = () => { clearInterval(timer); if (document.hidden) return; tick(); timer = setInterval(tick, 10_000); };
  sync();
  document.addEventListener("visibilitychange", sync);
  return () => { clearInterval(timer); document.removeEventListener("visibilitychange", sync); };
}
