import type { PostSource, RoomID, RoomSummary } from "./contract.gen.ts";

// sourceLabel names the author of a cross-room post as its source says, in the room it came from.
export const sourceLabel = (source: PostSource): string => `${source.roomName}:${source.sessionName}`;

// roomIsLive is true for a room the owner can still open: known, and not being deleted.
export const roomIsLive = (rooms: ReadonlyMap<RoomID, RoomSummary>, id: RoomID): boolean => {
  const room = rooms.get(id);
  return room !== undefined && !room.deleting;
};
