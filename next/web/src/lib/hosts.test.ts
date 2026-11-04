import { test } from "node:test";
import assert from "node:assert/strict";
import { diskLow, hang, hostBars, hostChips, hostGroup, hostLabel, hostLine, hostLoads, hostMeters, hostName, hostOrder, hostState, hostText, largeShared, lastSeen, machines, onHost, roomSessions } from "./hosts.ts";
import { listView } from "./settings-list.ts";
import type { HostSummary } from "./contract.gen.ts";

test("labels the Dyna host without implying that it runs this browser", () => {
  assert.equal(hostLabel("control-plane"), "Dyna host");
  assert.equal(hostLabel("build-server"), "build-server");
});

test("a box's state names its machine, as the host label does, once it has one", () => {
  assert.equal(onHost("Ready", "arch"), "Ready on arch");
  assert.equal(onHost("Asleep", "control-plane"), "Asleep on Dyna host");
  assert.equal(onHost("Starting", undefined), "Starting");
  assert.equal(onHost("Starting", ""), "Starting");
});

const machine = { name: "lab", reachable: true, boxes: 2, cpus: 8, memBytes: 8, cpuUsed: 0, memUsed: 0, diskBytes: 0, diskUsed: 0, diskLow: false, shared: [], history: null };

test("a machine's sessions group by room, rooms by name, each room in the engine's order", () => {
  const session = (room: string, id: string, boxState: "ready" | "stopped" = "ready") => ({ room, id, name: id, boxState });
  const sessions = [session("r2", "b"), session("r1", "z"), session("r2", "a", "stopped")];
  const names: Record<string, string> = { r1: "Zebra", r2: "Apple" };
  assert.deepEqual(roomSessions({ ...machine, sessions }, room => names[room]), [
    { room: "r2", name: "Apple", sessions: [sessions[0], sessions[2]] }, { room: "r1", name: "Zebra", sessions: [sessions[1]] }]);
  assert.deepEqual(roomSessions(machine, room => room), []);
  assert.deepEqual(roomSessions({ ...machine, sessions: null }, room => room), []);
});

test("a popover hangs under its button, right edges lined up, and over it when there is more room above", () => {
  assert.equal(hang({ top: 100, right: 700, bottom: 120 }, 1000, 800), "top: 126px; right: 300px; max-height: min(560px, 70vh, 658px)");
  // Near the viewport's right edge it keeps 12 px, and the height stops short of the bottom.
  assert.equal(hang({ top: 300, right: 995, bottom: 320 }, 1000, 800), "top: 326px; right: 12px; max-height: min(560px, 70vh, 458px)");
  // Low on the page it opens upward. A short viewport with more room below stays under the button.
  assert.equal(hang({ top: 700, right: 700, bottom: 720 }, 1000, 800), "top: auto; bottom: 106px; right: 300px; max-height: min(560px, 70vh, 678px)");
  assert.equal(hang({ top: 60, right: 700, bottom: 80 }, 1000, 300), "top: 86px; right: 300px; max-height: min(560px, 70vh, 198px)");
});

test("a merged card keeps the sessions both sides report, and an unmerged host keeps its own", () => {
  const session = (id: string) => ({ room: "r", id, name: id, boxState: "ready" as const });
  const plane = { ...machine, name: "control-plane", boxes: 0, sessions: null };
  const lab = { ...machine, engineHost: true, sessions: [session("a"), session("b")] };
  assert.deepEqual(machines([plane, lab])[0].sessions, lab.sessions);
  assert.deepEqual(machines([{ ...plane, sessions: [session("c")] }, lab])[0].sessions?.map(s => s.id), ["c", "a", "b"]);
  // Omitted on both sides, the engine's way of saying none, the card has none and no crash.
  assert.deepEqual(machines([{ ...plane, sessions: undefined }, { ...lab, sessions: undefined }])[0].sessions, []);
  assert.deepEqual(machines([{ ...plane, cpus: 0 }, lab])[1].sessions, lab.sessions);
});

test("one machine reported as the control plane and a registered host is one card, named for the host, with whole-machine figures", () => {
  const gib = 2 ** 30;
  const plane = { name: "control-plane", reachable: true, boxes: 0, cpus: 20, memBytes: 31 * gib, cpuUsed: 0.2, memUsed: 4 * gib, diskBytes: 1900 * gib, diskUsed: 1192 * gib,
    diskLow: false, shared: [{ room: "r", bytes: gib }], history: null, lastSeen: "2026-10-31T00:00:00Z" };
  const lab = { name: "lab", reachable: true, boxes: 3, cpus: 19, memBytes: 27 * gib, cpuUsed: 0, memUsed: 0, diskBytes: 1900 * gib, diskUsed: 1192 * gib,
    diskLow: false, shared: [], history: null, lastSeen: "2026-10-31T00:00:05Z", engineHost: true };
  const other = { ...lab, name: "gpu-box", engineHost: undefined, boxes: 1 };
  const [one, ...rest] = machines([plane, lab, other]);
  assert.deepEqual(rest, [other]);
  assert.deepEqual([one.name, one.boxes, one.shared], ["lab", 3, plane.shared]);
  assert.deepEqual(hostMeters(one).map(m => m.text), hostMeters(plane).map(m => m.text));
  assert.equal(hostLabel(one.name), "lab");
  // Either side's low disk shows, and a host the engine cannot reach still says so.
  assert.equal(machines([{ ...plane, diskLow: true }, lab])[0].diskLow, true);
  assert.equal(machines([plane, { ...lab, diskLow: true }])[0].diskLow, true);
  assert.equal(machines([plane, { ...lab, reachable: false }])[0].reachable, false);
  // With no registered host on the engine's machine, or none yet compared, both stay.
  assert.deepEqual(machines([plane, { ...lab, engineHost: false }]).map(h => h.name), ["control-plane", "lab"]);
  assert.deepEqual(machines([plane, other]).map(h => h.name), ["control-plane", "gpu-box"]);
  assert.deepEqual(machines([lab]), [lab]);
  // Before the control plane's first sample there are no whole-machine figures to show.
  assert.deepEqual(machines([{ ...plane, memBytes: 0 }, lab]).map(h => h.name), ["control-plane", "lab"]);
});

test("words a host's load, marks meters at 90% of their limit as busy, and has none without capacity", () => {
  const gib = 2 ** 30;
  const h = { name: "build-server", reachable: true, boxes: 1, cpus: 128, memBytes: 31 * gib, cpuUsed: 118.4, memUsed: 9.8 * gib,
    diskBytes: 2048 * gib, diskUsed: 700 * gib, diskLow: false, shared: [], lastSeen: "2026-10-31T00:00:00Z", history: null };
  assert.deepEqual(hostMeters(h).map(({ text, percent, busy }) => ({ text, percent, busy })), [
    { text: "118 of 128 cores", percent: 93, busy: true }, { text: "9.8 of 31 GB", percent: 32, busy: false },
    { text: `${(1348).toLocaleString()} GB free`, percent: 34, busy: false }]);
  assert.deepEqual(hostMeters({ ...h, name: "control-plane", cpus: 0, memBytes: 0 }), []);
  assert.equal(lastSeen(h, Date.parse("2026-10-31T00:04:10Z")), "Last seen 4 min ago");
});

test("the panel foot shows unreachable hosts first, then those low on disk, then reachable ones busiest first by CPU or memory, at most 3", () => {
  const host = (name: string, cpuUsed: number, memUsed: number, reachable = true) =>
    ({ name, reachable, boxes: 0, cpus: 10, memBytes: 100, cpuUsed, memUsed, diskBytes: 0, diskUsed: 0, diskLow: false, shared: [], history: null });
  const hosts = [host("calm", 1, 10), host("cpu", 9.5, 10), host("down", 10, 100, false), { ...host("control-plane", 0, 0), cpus: 0 },
    host("mem", 2, 60), host("quiet", 0.5, 5)];
  const lines = (list: typeof hosts) => hostLoads(list).map(h => [h.name, ...h.reachable ? h.meters.map(m => `${m.label} ${m.percent}%${m.busy ? " busy" : ""}`) : ["Unreachable"]]);
  assert.deepEqual(lines(hosts), [["down", "Unreachable"], ["cpu", "CPU 95% busy", "Memory 10%"], ["mem", "CPU 20%", "Memory 60%"]]);
  assert.deepEqual(hostLoads([...hosts, { ...host("full", 0.1, 1), diskLow: true }]).map(h => h.name), ["down", "full", "cpu"]);
  assert.deepEqual(lines(hosts.filter(h => h.reachable)), [["cpu", "CPU 95% busy", "Memory 10%"], ["mem", "CPU 20%", "Memory 60%"], ["calm", "CPU 10%", "Memory 10%"]]);
});

test("Home names a room's shared folder only past a quarter of its host's disk, largest first", () => {
  const host = (diskBytes: number, shared: { room: string; bytes: number }[] | null) => ({ name: "build-server", reachable: true, boxes: 1, cpus: 8, memBytes: 0,
    cpuUsed: 0, memUsed: 0, diskBytes, diskUsed: 0, diskLow: false, shared, history: null });
  const quarter = 100 * 2 ** 30;
  assert.deepEqual(largeShared(host(4 * quarter, [{ room: "a", bytes: quarter }, { room: "b", bytes: quarter + 1 }, { room: "c", bytes: 2 * quarter }])).map(f => f.room), ["c", "b"]);
  assert.deepEqual(largeShared(host(4 * quarter, null)), []);
  assert.deepEqual(largeShared(host(0, [{ room: "a", bytes: quarter }])), []);
});

test("an exec-only host with figures shows the same three meters and is never low at 99% disk", () => {
  const gib = 2 ** 30;
  const execOnly = { name: "omarchy", reachable: true, boxes: 0, cpus: 8, memBytes: 16 * gib, cpuUsed: 2, memUsed: 4 * gib, diskBytes: 1000 * gib, diskUsed: 990 * gib, diskLow: true, execOnly: true, shared: [], history: null };
  assert.deepEqual(hostMeters(execOnly).map(m => [m.label, m.percent]), [["CPU", 25], ["Memory", 25], ["Disk", 99]]);
  assert.equal(diskLow(execOnly), false);
  assert.deepEqual(hostLoads([execOnly]).map(h => [h.name, h.diskLow, h.meters.length]), [["omarchy", false, 2]]);
  assert.deepEqual(hostLoads([{ ...execOnly, reachable: false }]).map(h => [h.reachable, h.meters.length]), [[false, 0]]);
});

test("an exec-only host with no figures has no meters, as before", () => {
  const bare = { name: "omarchy", reachable: true, boxes: 0, cpus: 0, memBytes: 0, cpuUsed: 0, memUsed: 0, diskBytes: 0, diskUsed: 0, diskLow: false, execOnly: true, shared: [], history: null };
  assert.deepEqual(hostMeters(bare), []);
});

test("an exec-only host is online or offline and never low on disk", () => {
  const execOnly = { name: "omarchy", reachable: true, boxes: 0, cpus: 0, memBytes: 0, cpuUsed: 0, memUsed: 0, diskBytes: 0, diskUsed: 0, diskLow: true, execOnly: true, shared: [], history: null };
  assert.equal(diskLow(execOnly), false);
  assert.equal(diskLow({ ...execOnly, execOnly: false }), true);
  assert.equal(hostLoads([execOnly])[0]?.diskLow ?? false, false);
});


const gib = 2 ** 30;
const now = Date.parse("2026-11-04T12:00:00Z");
const host = (name: string, over: Partial<HostSummary> = {}): HostSummary => ({ name, reachable: true, boxes: 2, cpus: 10, memBytes: 100 * gib, cpuUsed: 5, memUsed: 50 * gib,
  diskBytes: 1000 * gib, diskUsed: 500 * gib, diskLow: false, shared: [], history: null, agents: true, ci: true, ...over });
const names = (hosts: readonly HostSummary[]) => hosts.map(h => h.name);
const all = hostChips[0];

test("Settings lists registered hosts: the Dyna host first, then by name with digits as numbers, then pool nodes; the control plane alone is left out", () => {
  const hosts = [host("node-10", { pool: "burst" }), host("Studio"), host("control-plane"), host("node-2", { pool: "burst" }), host("Arch"), host("lab", { engineHost: true }),
    host("box-10"), host("box-9"), host("gpu-1", { pool: "gpu" }), host("zed")];
  assert.deepEqual(names(hostOrder(hosts)), ["lab", "Arch", "box-9", "box-10", "Studio", "zed", "gpu-1", "node-2", "node-10"]);
  // The engine's machine comes first wherever its name sorts, and a pool interleaved anywhere sorts after every added host.
  assert.deepEqual(names(hostOrder([host("a"), host("z", { engineHost: true }), host("b", { pool: "p" }), host("c")])), ["z", "a", "c", "b"]);
  assert.deepEqual(names(hostOrder([host("control-plane")])), []);
  // The input is left as it came.
  const input = [host("b"), host("a")];
  hostOrder(input);
  assert.deepEqual(names(input), ["b", "a"]);
});

test("a host is added by you or from pools, and the groups come in that order", () => {
  assert.deepEqual(hostGroup(host("a")), { id: "own", title: "Added by you" });
  assert.deepEqual(hostGroup(host("a", { pool: "burst" })), { id: "pool", title: "From pools" });
  assert.deepEqual(hostGroup(host("a", { pool: "" })), { id: "own", title: "Added by you" });
  const view = listView(hostOrder([host("p", { pool: "x" }), host("a"), host("q", { pool: "y" })]), { text: hostText, key: h => h.name, query: "", chip: all, group: hostGroup, folded: new Set() });
  assert.deepEqual(view.rows.map(row => row.kind === "group" ? `${row.group.title} ${row.count}` : row.key), ["Added by you 1", "a", "From pools 2", "p", "q"]);
});

test("the engine's machine is named as the Dyna host; any other keeps its name", () => {
  assert.equal(hostName(host("hermeslab", { engineHost: true })), "hermeslab (Dyna host)");
  assert.equal(hostName(host("hermeslab")), "hermeslab");
});

test("a row's one state: unreachable first, then a host problem, then low disk, which an exec-only host never is", () => {
  assert.deepEqual(hostState(host("a")), undefined);
  assert.deepEqual(hostState(host("a", { reachable: false, diskLow: true, problem: "create_failed" })), { tone: "error", label: "Unreachable" });
  assert.deepEqual(hostState(host("a", { reachable: false, execOnly: true })), { tone: "error", label: "Unreachable" });
  assert.deepEqual(hostState(host("a", { problem: "create_failed", diskLow: true })), { tone: "warning", label: "Boxes failing" });
  assert.deepEqual(hostState(host("a", { problem: "share_unavailable" })), { tone: "warning", label: "No shared folder" });
  assert.deepEqual(hostState(host("a", { diskLow: true })), { tone: "warning", label: "Disk low" });
  assert.deepEqual(hostState(host("a", { diskLow: true, execOnly: true })), undefined);
});

test("a row's line: an exec-only host is online or last seen; any other host gives its workspaces, pool, agents off, rooms and last seen", () => {
  const seen = new Date(now - 4 * 60_000).toISOString();
  assert.equal(hostLine(host("o", { execOnly: true, boxes: 0 }), now), "Online");
  assert.equal(hostLine(host("o", { execOnly: true, reachable: false, lastSeen: seen }), now), "Last seen 4 min ago");
  assert.equal(hostLine(host("o", { execOnly: true, reachable: false }), now), "Not seen yet");
  assert.equal(hostLine(host("a", { boxes: 1 }), now), "1 workspace");
  assert.equal(hostLine(host("a", { boxes: 0 }), now), "0 workspaces");
  assert.equal(hostLine(host("a", { pool: "burst", agents: undefined, rooms: ["r1"] }), now), "2 workspaces · pool burst · agents off · 1 room");
  assert.equal(hostLine(host("a", { rooms: ["r1", "r2"], reachable: false, lastSeen: seen }), now), "2 workspaces · 2 rooms · Last seen 4 min ago");
  assert.equal(hostLine(host("a", { rooms: [] }), now), "2 workspaces");
});

test("a row's bars: CPU, memory and disk of a reachable host that is not exec only, warm from 90%, and no disk bar without a disk figure", () => {
  assert.deepEqual(hostBars(host("a", { cpuUsed: 9, memUsed: 89.9 * gib, diskUsed: 900 * gib })), [
    { label: "CPU", percent: 90, busy: true }, { label: "Mem", percent: 90, busy: false }, { label: "Disk", percent: 90, busy: true }]);
  assert.deepEqual(hostBars(host("a", { diskBytes: 0, diskUsed: 0 })).map(bar => bar.label), ["CPU", "Mem"]);
  assert.deepEqual(hostBars(host("a", { reachable: false })), []);
  assert.deepEqual(hostBars(host("a", { execOnly: true })), []);
  assert.deepEqual(hostBars(host("a", { cpus: 0 })), []);
});

test("chips: Needs attention takes unreachable hosts, host problems and low disk, never an exec-only host's disk; Exec only takes exec-only hosts", () => {
  const hosts = [host("ok"), host("down", { reachable: false }), host("stuck", { problem: "share_unavailable" }), host("full", { diskLow: true }),
    host("exec", { execOnly: true, diskLow: true }), host("exec-down", { execOnly: true, reachable: false })];
  const ids = hostChips.map(chip => chip.id);
  assert.deepEqual(ids, ["all", "attention", "exec"]);
  const view = listView(hosts, { text: hostText, key: h => h.name, query: "", chip: all, chips: hostChips, folded: new Set() });
  assert.deepEqual(view.counts, { all: 6, attention: 4, exec: 2 });
  assert.deepEqual(hosts.filter(hostChips[1].test!).map(h => h.name), ["down", "stuck", "full", "exec-down"]);
  assert.deepEqual(hosts.filter(hostChips[2].test!).map(h => h.name), ["exec", "exec-down"]);
});

test("the search matches a name in any case, Dyna host for the engine's machine, and the pool", () => {
  const hosts = [host("Hermeslab", { engineHost: true }), host("studio"), host("node-4", { pool: "hetzner-cx" }), host("dyna-builder")];
  const find = (query: string) => listView(hosts, { text: hostText, key: h => h.name, query, chip: all, folded: new Set() }).rows.map(row => row.kind === "item" ? row.key : "");
  assert.deepEqual(find("hermes"), ["Hermeslab"]);
  assert.deepEqual(find("dyna host"), ["Hermeslab"]);
  assert.deepEqual(find("dyna"), ["Hermeslab", "dyna-builder"]);
  assert.deepEqual(find("hetzner"), ["node-4"]);
  assert.deepEqual(find("STUDIO"), ["studio"]);
});

test("the engine's machine merges into its registered host and keeps that host's switches, rooms, pool and problem", () => {
  const plane = host("control-plane", { cpus: 20, memBytes: 31 * gib, agents: undefined, ci: undefined, boxes: 0 });
  const lab = host("lab", { engineHost: true, cpus: 19, agents: false, ci: true, rooms: ["r1"], problem: "create_failed", runners: 2, execOnly: false });
  const [one] = machines([plane, lab]);
  assert.deepEqual([one.name, one.cpus, one.memBytes, one.agents, one.ci, one.rooms, one.problem, one.runners, one.engineHost], ["lab", 20, 31 * gib, false, true, ["r1"], "create_failed", 2, true]);
});
