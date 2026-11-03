import type { RoomSetRequest, RoomSummary, RootApproval } from "./contract.gen.ts";

// The safeguards a room's root can take over from the owner, in the order the page shows them (spec 3.5).
export const rootApprovals: readonly { kind: RootApproval; label: string; note: string }[] = [
  { kind: "hostexec", label: "Root approves host exec", note: "Sessions' host exec requests go to root" },
  { kind: "addons", label: "Root approves global add-ons", note: "Skills, packages and MCP servers for every room" },
  { kind: "hosts", label: "Root changes room host rules", note: "Where this room's new boxes may go" },
  { kind: "pools", label: "Root adds node pools", note: "Pools run scripts that can cost money" },
];

// Whether the owner has turned a kind on for the room. The engine lists the kinds that are on.
export const approvalOn = (room: Pick<RoomSummary, "rootApproves">, kind: RootApproval) => (room.rootApproves ?? []).includes(kind);

// Autopilot is on when every kind is.
export const autopilotOn = (room: Pick<RoomSummary, "rootApproves">) => rootApprovals.every(({ kind }) => approvalOn(room, kind));

// The request carries every kind, as the generated type asks; a switch changes its own and keeps the rest as listed.
const request = (room: RoomSummary, on: (kind: RootApproval) => boolean): RoomSetRequest => ({ room: room.id, rootApproves: { hostexec: on("hostexec"), addons: on("addons"), hosts: on("hosts"), pools: on("pools") } });

export const switchRequest = (room: RoomSummary, kind: RootApproval, on: boolean) => request(room, other => other === kind ? on : approvalOn(room, other));

// The Autopilot button turns every kind on, or, when all are on, every kind off.
export const autopilotRequest = (room: RoomSummary) => request(room, () => !autopilotOn(room));

// What the Autopilot button asks before it turns the kinds on: the owner reads each one, and what pools can cost. Turning them off asks nothing.
export const autopilotConfirm = (room: RoomSummary) => autopilotOn(room) ? undefined : {
  title: `Turn on Autopilot for ${room.name}?`,
  message: `The root of ${room.name} will decide these requests, and you still can: ${rootApprovals.map(item => item.label.replace(/^Root /, "")).join("; ")}.`,
  note: "Node pools run scripts that can cost money.",
  action: "Turn on Autopilot",
};
