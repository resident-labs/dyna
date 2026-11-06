import { call } from "./client.ts";
import type { GPU, HostProblem, HostSummary, RoomShared } from "./contract.gen.ts";
import { count } from "./feed.ts";
import type { Chip, Group } from "./settings-list.ts";

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

// A host's low-disk mark. An exec-only host has no disk figures, so it is never low.
export const diskLow = (h: HostSummary) => h.diskLow && !h.execOnly;

// One tile on a Home card: the label, the percent large with an optional muted unit after it, a bar, and the text.
export type Meter = { label: string; text: string; percent: number; busy: boolean; unit?: string };

// One card per machine. When the engine runs on a registered host, host.list reports that machine twice: the control
// plane with whole-machine figures, and the registered host with only dyna.slice's use of its allowance (cores less
// one, memory less the floor). They merge under the host's name with the whole-machine figures, both sides' workspaces
// and sessions, either side's low disk, and whether the engine can reach it. The host's runners, switches, rooms and
// problem come along for Settings; Home's card shows none but the runners. Until the control plane's first sample, the
// host stands alone.
export function machines(hosts: HostSummary[]): HostSummary[] {
  const plane = hosts.find(h => h.name === "control-plane");
  const same = hosts.find(h => h.engineHost);
  if (!plane || !same || !hostMeters(plane).length) return hosts;
  const merged = { ...plane, name: same.name, reachable: same.reachable, lastSeen: same.lastSeen, boxes: plane.boxes + same.boxes, runners: same.runners,
    agents: same.agents, ci: same.ci, rooms: same.rooms, problem: same.problem, health: same.health,
    diskLow: diskLow(plane) || diskLow(same), sessions: [...plane.sessions ?? [], ...same.sessions ?? []], engineHost: true };
  return hosts.filter(h => h !== same).map(h => h === plane ? merged : h);
}

// Figures of 100 or more drop the decimal, so a tile's text stays on one line, and large ones group their digits
// ("1,348 GB free"). GB counts 2^30 bytes, as v1's GiB did. A meter at 90% of its limit or more is busy.
export const gb = (bytes: number) => bytes / 2 ** 30;
export const whole = (n: number) => Math.round(n).toLocaleString();
export const fixed = (n: number) => n >= 100 ? whole(n) : n.toFixed(1);
export const meter = (label: string, used: number, limit: number, text: string): Meter =>
  ({ label, text, percent: Math.min(100, Math.round(used / limit * 100)), busy: used >= 0.9 * limit });

// A GPU's line in Settings: its name, the VRAM boxes asked of it and its total. Asked VRAM is a claim, not a measure of use.
export const gpuLine = (g: GPU) => `${g.name}, ${fixed(gb(g.requestedBytes ?? 0))} of ${whole(gb(g.memBytes))} GB asked`;

// A host's CPU, memory and disk meters, "3.1 of 16 cores", "9.8 of 31 GB" and "412 GB free"; none while it reports
// no capacity, and no disk tile without a disk figure. A host with GPUs adds one tile for the VRAM asked of all its cards.
export function hostMeters(h: HostSummary): Meter[] {
  if (!h.cpus || !h.memBytes) return [];
  const asked = (h.gpus ?? []).reduce((sum, g) => sum + (g.requestedBytes ?? 0), 0), vram = (h.gpus ?? []).reduce((sum, g) => sum + g.memBytes, 0);
  return [
    meter("CPU", h.cpuUsed, h.cpus, `${fixed(h.cpuUsed)} of ${whole(h.cpus)} cores`),
    meter("Memory", h.memUsed, h.memBytes, `${fixed(gb(h.memUsed))} of ${whole(gb(h.memBytes))} GB`),
    ...h.diskBytes ? [meter("Disk", h.diskUsed, h.diskBytes, `${whole(gb(h.diskBytes - h.diskUsed))} GB free`)] : [],
    ...vram ? [meter("GPU asked", asked, vram, `${fixed(gb(asked))} of ${whole(gb(vram))} GB`)] : [],
  ];
}

// "Last seen 4 min ago" for a host that stopped answering, from its last beat; undefined when it never answered.
export function lastSeen(h: HostSummary, now: number): string | undefined {
  if (!h.lastSeen) return undefined;
  const minutes = Math.max(0, Math.round((now - Date.parse(h.lastSeen)) / 60_000));
  return "Last seen " + (minutes < 1 ? "just now" : minutes < 60 ? `${minutes} min ago` : minutes < 1440 ? `${Math.round(minutes / 60)} h ago` : `${Math.round(minutes / 1440)} d ago`);
}

// The rooms whose shared folder holds more than a quarter of the machine's disk, largest first (spec 5.6). Each has one
// copy, on the engine's machine, and no hard limit, so Home names the room and the size for the owner to clean up. The
// engine sends null for no folders.
export const largeShared = (h: HostSummary): RoomShared[] =>
  h.diskBytes ? (h.shared ?? []).filter(folder => folder.bytes > h.diskBytes / 4).sort((a, b) => b.bytes - a.bytes) : [];

// A folder's size in whole GB of 2^30 bytes, as the meters count.
export const sharedSize = (bytes: number) => `${Math.round(bytes / 2 ** 30).toLocaleString()} GB`;

// The sessions panel's foot: at most 3 hosts. Unreachable hosts come first, then those low on disk, since they need
// attention; then the reachable ones that report load, busiest first by the higher of CPU and memory.
export function hostLoads(hosts: HostSummary[]): { name: string; reachable: boolean; diskLow: boolean; meters: Meter[] }[] {
  const load = (meters: Meter[]) => Math.max(...meters.map(m => m.percent));
  const loads = hosts.filter(h => h.reachable).map(h => ({ name: h.name, reachable: true, diskLow: diskLow(h), meters: hostMeters(h).slice(0, 2) }))
    .filter(h => h.diskLow || h.meters.length).sort((a, b) => Number(b.diskLow) - Number(a.diskLow) || load(b.meters) - load(a.meters));
  return [...hosts.filter(h => !h.reachable).map(h => ({ name: h.name, reachable: false, diskLow: diskLow(h), meters: [] })), ...loads].slice(0, 3);
}

// A read now and then every 10 s to match the engine's beat, only while the page is visible. A tick skips while a read is
// out, so reads never pile up on a slow engine and answers arrive in order; a failed one leaves the next to try again. It
// returns the stop function.
export function watch<T>(read: () => Promise<T>, apply: (value: T) => void, fail: (reason: unknown) => void): () => void {
  let timer: ReturnType<typeof setInterval> | undefined;
  let loading = false;
  const tick = () => {
    if (loading) return;
    loading = true;
    void read().then(apply, fail).finally(() => { loading = false; });
  };
  const sync = () => { clearInterval(timer); if (document.hidden) return; tick(); timer = setInterval(tick, 10_000); };
  sync();
  document.addEventListener("visibilitychange", sync);
  return () => { clearInterval(timer); document.removeEventListener("visibilitychange", sync); };
}

// host.list as machines, watched. shape turns the list into what apply gets: machines by default; Settings > Images reads
// every host's own runner images, which the merge leaves out.
export const watchHosts = (apply: (hosts: HostSummary[]) => void, fail: () => void, shape: (hosts: HostSummary[]) => HostSummary[] = machines) =>
  watch(() => call("host.list", {}).then(list => shape(list.hosts ?? [])), apply, fail);

// Settings > Hosts (spec 5.13) lists the registered hosts: the control plane alone is no registered host, so it is left
// out. Hosts the owner added come first, the engine's machine first among them, then pool nodes, where it has no place; each by name, its
// digits as numbers, so node-2 comes before node-10.
export const hostOrder = (hosts: readonly HostSummary[]): HostSummary[] => hosts.filter(h => h.name !== "control-plane")
  .sort((a, b) => Number(!!a.pool) - Number(!!b.pool) || Number(!b.pool && !!b.engineHost) - Number(!a.pool && !!a.engineHost) || a.name.localeCompare(b.name, "en", { numeric: true }));

const added: Group = { id: "own", title: "Added by you" }, pooled: Group = { id: "pool", title: "From pools" };
export const hostGroup = (h: HostSummary) => h.pool ? pooled : added;

// The engine's machine says so beside its name.
export const hostName = (h: HostSummary) => h.engineHost ? `${h.name} (Dyna host)` : h.name;

const problems: Record<HostProblem, string> = { create_failed: "Boxes failing", share_unavailable: "No shared folder" };

// A row's one state, the most pressing: a host the engine cannot reach, then a host problem, then low disk.
export function hostState(h: HostSummary): { tone: "error" | "warning"; label: string } | undefined {
  if (!h.reachable) return { tone: "error", label: "Unreachable" };
  if (h.health === "degraded") return { tone: "warning", label: "Degraded" };
  if (h.problem) return { tone: "warning", label: problems[h.problem] };
  return diskLow(h) ? { tone: "warning", label: "Disk low" } : undefined;
}

// A row's line of values. An exec-only host runs no workspaces, so it is online or when it was last seen.
export function hostLine(h: HostSummary, now: number): string {
  const seen = h.reachable ? undefined : lastSeen(h, now) ?? "Not seen yet";
  if (h.execOnly) return seen ?? "Online";
  const rooms = h.rooms?.length ?? 0;
  return [count(h.boxes, "workspace"), h.pool && `pool ${h.pool}`, !h.agents && "agents off", rooms && count(rooms, "room"), seen].filter(Boolean).join(" · ");
}

// A row's short bars, Home's meters at a glance: none for a host whose figures are old or that places nothing.
export const hostBars = (h: HostSummary): { label: string; percent: number; busy: boolean }[] => h.reachable && !h.execOnly
  ? hostMeters(h).slice(0, 3).map(({ percent, busy }, index) => ({ label: ["CPU", "Mem", "Disk"][index], percent, busy })) : [];

export const hostChips: Chip<HostSummary>[] = [
  { id: "all", label: "All" },
  { id: "attention", label: "Needs attention", test: h => !h.reachable || h.health === "degraded" || !!h.problem || diskLow(h) },
  { id: "exec", label: "Exec only", test: h => !!h.execOnly },
];

export const hostText = (h: HostSummary) => `${h.name} ${h.engineHost ? "Dyna host" : ""} ${h.pool ?? ""}`;
