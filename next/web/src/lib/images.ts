import type { HostSummary, RoomImage } from "./contract.gen.ts";
import { count, fileSize } from "./feed.ts";
import type { Group } from "./settings-list.ts";

// Settings > Images (spec 5.13): every room's images (5.2) from image.list, grouped by room. names maps a room's ID to
// its name from room.list; a room it no longer has is gone, and all such rooms share one group, last.

// The room's CI runner image; every other name is a box image.
export const runnerImage = "ci-runner";

// An image is its room and its name: two rooms may hold an image of one name. Neither holds a colon.
export const imageKey = (image: RoomImage) => `${image.room}:${image.name}`;

const gone: Group = { id: "", title: "A room no longer listed" };

export const imageGroup = (names: ReadonlyMap<string, string>) => (image: RoomImage): Group => {
  const name = names.get(image.room);
  return name === undefined ? gone : { id: image.room, title: name };
};

// Rooms by name (room names are unique), rooms no longer listed last; in each, images by name with digits as numbers, node-9 before node-22.
export function imageOrder(images: readonly RoomImage[], names: ReadonlyMap<string, string>): RoomImage[] {
  const room = (image: RoomImage) => names.get(image.room);
  return [...images].sort((a, b) => {
    const left = room(a), right = room(b);
    if (left === undefined || right === undefined) return Number(left === undefined) - Number(right === undefined) || a.name.localeCompare(b.name, "en", { numeric: true });
    return left.localeCompare(right, "en") || a.name.localeCompare(b.name, "en", { numeric: true });
  });
}

export const imageText = (names: ReadonlyMap<string, string>) => (image: RoomImage) => `${image.name} ${names.get(image.room) ?? ""}`;

export const imageLine = (image: RoomImage) => `${count(image.files, "file")} · ${fileSize(image.bytes)}`;

// The CI hosts that could not build this ci-runner save, or could not read the room's image at all (no image ID), by
// host name with digits as numbers. A failure of an earlier save is not this one's: the host builds the new save anew.
export function runnerFailures(image: RoomImage, hosts: readonly HostSummary[]): { host: string; reason: string }[] {
  if (image.name !== runnerImage) return [];
  return hosts.flatMap(h => (h.runnerImages ?? []).filter(problem => problem.room === image.room && (problem.image === image.id || !problem.image))
    .map(problem => ({ host: h.name, reason: problem.reason }))).sort((a, b) => a.host.localeCompare(b.host, "en", { numeric: true }));
}

export const imageSummary = (images: readonly RoomImage[], hosts: readonly HostSummary[]) =>
  ({ images: images.length, rooms: new Set(images.map(image => image.room)).size, failed: images.filter(image => runnerFailures(image, hosts).length).length });

// An image may hold hundreds of files; the detail lists the first 40 and counts the rest.
export const fileList = (files: readonly string[] | null) => ({ shown: (files ?? []).slice(0, 40), more: Math.max(0, (files ?? []).length - 40) });
