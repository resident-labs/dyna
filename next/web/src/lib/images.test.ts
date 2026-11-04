import { test } from "node:test";
import assert from "node:assert/strict";
import { fileList, imageGroup, imageKey, imageLine, imageOrder, imageSummary, imageText, runnerFailures } from "./images.ts";
import { listView } from "./settings-list.ts";
import type { HostSummary, RoomImage, RunnerImageProblem } from "./contract.gen.ts";

const image = (room: string, name: string, more: Partial<RoomImage> = {}): RoomImage => ({ room, name, id: `${room}-${name}`, files: 3, bytes: 2048, ...more });
const names = new Map([["r1", "payments"], ["r2", "Infra"], ["r3", "data-pipeline"], ["r10", "omarchy"]]);
const host = (name: string, runnerImages?: RunnerImageProblem[] | null): HostSummary =>
  ({ name, reachable: true, boxes: 0, cpus: 4, memBytes: 4, cpuUsed: 0, memUsed: 0, diskBytes: 0, diskUsed: 0, diskLow: false, shared: [], history: null, runnerImages });

test("an image's key is its room and its name, so two rooms' images of one name differ", () => {
  assert.equal(imageKey(image("r1", "node-22")), "r1:node-22");
  assert.notEqual(imageKey(image("r1", "node-22")), imageKey(image("r10", "node-22")));
});

test("images sort by room name in any case, rooms no longer listed last, then by name with digits as numbers", () => {
  const items = [image("gone-a", "b"), image("r1", "node-22"), image("r10", "x"), image("gone-b", "a"), image("r1", "Go-1.25"), image("r1", "node-9"), image("r2", "rust"), image("r3", "cuda"), image("gone-a", "node-22"), image("gone-b", "node-9")];
  assert.deepEqual(imageOrder(items, names).map(imageKey),
    ["r3:cuda", "r2:rust", "r10:x", "r1:Go-1.25", "r1:node-9", "r1:node-22", "gone-b:a", "gone-a:b", "gone-b:node-9", "gone-a:node-22"]);
  // The engine's list is not changed.
  assert.equal(imageKey(items[0]), "gone-a:b");
});

test("a room's images group under its name; every room no longer listed shares one group", () => {
  const group = imageGroup(names);
  assert.deepEqual(group(image("r1", "a")), { id: "r1", title: "payments" });
  assert.deepEqual(group(image("r10", "a")), { id: "r10", title: "omarchy" });
  assert.deepEqual(group(image("gone-a", "a")), group(image("gone-b", "a")));
  assert.equal(group(image("gone-a", "a")).title, "A room no longer listed");
  assert.notEqual(group(image("gone-a", "a")).id, "gone-a");
  const view = listView(imageOrder([image("r1", "a"), image("gone", "x"), image("r2", "b"), image("r1", "c"), image("gone2", "y")], names),
    { text: imageText(names), key: imageKey, query: "", chip: { id: "", label: "" }, group, folded: new Set() });
  assert.deepEqual(view.rows.map(row => row.kind === "group" ? `${row.group.title} ${row.count}` : row.key),
    ["Infra 1", "r2:b", "payments 2", "r1:a", "r1:c", "A room no longer listed 2", "gone:x", "gone2:y"]);
});

test("the search matches the name and the room's name in any case, and nothing else", () => {
  const items = [image("r1", "node-22", { id: "infra" }), image("r2", "rust-dev"), image("r10", "Cuda-12"), image("gone", "plain")];
  const find = (query: string) => listView(items, { text: imageText(names), key: imageKey, query, chip: { id: "", label: "" }, folded: new Set() }).rows.map(row => row.kind === "item" ? row.key : "");
  assert.deepEqual(find("infra"), ["r2:rust-dev"]);
  assert.deepEqual(find("CUDA"), ["r10:Cuda-12"]);
  assert.deepEqual(find("payments node"), ["r1:node-22"]);
  assert.deepEqual(find("pay rust"), []);
  assert.deepEqual(find("listed"), []);
  assert.deepEqual(find("r1"), []);
});

test("a row's line is its file count and its size", () => {
  assert.equal(imageLine(image("r1", "a", { files: 1, bytes: 1023 })), "1 file · 1023 B");
  assert.equal(imageLine(image("r1", "a", { files: 2, bytes: 1024 })), "2 files · 1.0 KB");
  assert.equal(imageLine(image("r1", "a", { files: 14, bytes: 3 * 1024 * 1024 })), "14 files · 3.0 MB");
});

test("a ci-runner image's failures are the hosts that could not build its current save or read the room's image; any other image has none", () => {
  const runner = image("r1", "ci-runner", { id: "abc" });
  const hosts = [
    host("node-10", [{ room: "r1", image: "abc", reason: "exit 1: apt failed" }, { room: "r10", image: "abc", reason: "other room" }]),
    host("node-2", [{ room: "r1", image: "", reason: "the image could not be read: denied" }]),
    host("node-3", [{ room: "r1", image: "ab", reason: "an earlier save, same prefix" }, { room: "r1", image: "abcd", reason: "a later id, same prefix" }]),
    host("node-4", null), host("node-5"),
    host("node-1", [{ room: "r", image: "abc", reason: "a room id that is a prefix" }]),
  ];
  assert.deepEqual(runnerFailures(runner, hosts), [
    { host: "node-2", reason: "the image could not be read: denied" }, { host: "node-10", reason: "exit 1: apt failed" }]);
  assert.deepEqual(runnerFailures(image("r1", "ci-runner2", { id: "abc" }), hosts), []);
  assert.deepEqual(runnerFailures(image("r1", "node-22", { id: "abc" }), hosts), []);
  assert.deepEqual(runnerFailures(image("r10", "ci-runner", { id: "abc" }), hosts), [{ host: "node-10", reason: "other room" }]);
  assert.deepEqual(runnerFailures(runner, []), []);
});

test("the summary counts the images, the rooms that hold them and the images whose runner build failed", () => {
  const items = [image("r1", "ci-runner", { id: "a" }), image("r1", "node"), image("r2", "ci-runner", { id: "b" }), image("r3", "x"), image("r10", "ci-runner", { id: "c" })];
  const hosts = [host("h1", [{ room: "r1", image: "a", reason: "x" }, { room: "r2", image: "b", reason: "y" }]), host("h2", [{ room: "r1", image: "a", reason: "z" }, { room: "r10", image: "old", reason: "w" }])];
  assert.deepEqual(imageSummary(items, hosts), { images: 5, rooms: 4, failed: 2 });
  assert.deepEqual(imageSummary([], hosts), { images: 0, rooms: 0, failed: 0 });
});

test("an image's files show the first 40 and count the rest", () => {
  const files = (n: number) => Array.from({ length: n }, (_, i) => `f${i}`);
  assert.deepEqual(fileList(files(39)), { shown: files(39), more: 0 });
  assert.deepEqual(fileList(files(40)), { shown: files(40), more: 0 });
  assert.deepEqual(fileList(files(41)), { shown: files(40), more: 1 });
  assert.deepEqual(fileList(files(100)).more, 60);
  assert.deepEqual(fileList(null), { shown: [], more: 0 });
});
