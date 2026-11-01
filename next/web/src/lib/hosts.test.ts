import { test } from "node:test";
import assert from "node:assert/strict";
import { backupProblem, hang, hostLabel, hostLoads, hostMeters, largeShared, lastSeen, machines, onHost, roomSessions } from "./hosts.ts";

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
    diskLow: false, shared: [{ room: "r", bytes: gib }], history: null, backupCopyEnabled: true, lastSeen: "2026-10-31T00:00:00Z" };
  const lab = { name: "lab", reachable: true, boxes: 3, cpus: 19, memBytes: 27 * gib, cpuUsed: 0, memUsed: 0, diskBytes: 1900 * gib, diskUsed: 1192 * gib,
    diskLow: false, shared: [], history: null, lastSeen: "2026-10-31T00:00:05Z", engineHost: true };
  const other = { ...lab, name: "gpu-box", engineHost: undefined, boxes: 1 };
  const [one, ...rest] = machines([plane, lab, other]);
  assert.deepEqual(rest, [other]);
  assert.deepEqual([one.name, one.boxes, one.shared], ["lab", 3, plane.shared]);
  assert.deepEqual(hostMeters(one).map(m => m.text), hostMeters(plane).map(m => m.text));
  assert.equal(one.backupCopyEnabled, true);
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

test("Home marks a stored backup failure or a copy over 48 hours old, and nothing else", () => {
  const now = Date.parse("2026-10-31T12:00:00Z");
  const h = { name: "control-plane", reachable: true, boxes: 0, cpus: 8, memBytes: 8, cpuUsed: 1, memUsed: 1, diskBytes: 0, diskUsed: 0, diskLow: false, shared: [], history: null };
  const on = { ...h, backupCopyEnabled: true };
  assert.equal(backupProblem(h, now), undefined);
  assert.equal(backupProblem(on, now), undefined);
  assert.equal(backupProblem({ ...on, backupCopyAt: "2026-10-29T12:00:00Z" }, now), undefined);
  assert.deepEqual(backupProblem({ ...on, backupCopyAt: "2026-10-28T11:00:00Z" }, now), { tone: "warning", label: "Backup 3 days old" });
  assert.deepEqual(backupProblem({ ...on, backupCopyAt: "2026-10-31T00:00:00Z", backupCopyFailure: "no_space" }, now), { tone: "error", label: "Backup machine full", title: "The second machine has no room for the backup" });
  assert.equal(backupProblem({ ...on, backupCopyFailure: "failed" }, now)?.label, "Backup failed");
});

test("Home names a room's shared folder only past a quarter of its host's disk, largest first", () => {
  const host = (diskBytes: number, shared: { room: string; bytes: number }[] | null) => ({ name: "build-server", reachable: true, boxes: 1, cpus: 8, memBytes: 0,
    cpuUsed: 0, memUsed: 0, diskBytes, diskUsed: 0, diskLow: false, shared, history: null });
  const quarter = 100 * 2 ** 30;
  assert.deepEqual(largeShared(host(4 * quarter, [{ room: "a", bytes: quarter }, { room: "b", bytes: quarter + 1 }, { room: "c", bytes: 2 * quarter }])).map(f => f.room), ["c", "b"]);
  assert.deepEqual(largeShared(host(4 * quarter, null)), []);
  assert.deepEqual(largeShared(host(0, [{ room: "a", bytes: quarter }])), []);
});
