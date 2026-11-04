import type { RoomSetRequest, RoomSummary, RootApproval } from "./contract.gen.ts";
import type { Chip } from "./settings-list.ts";

// The safeguards a room's root can take over from the owner, in the order the page shows them (spec 3.5). short heads the
// list's column and names the kind in a narrow row; the label and the note show once, in a room's detail.
export const rootApprovals: readonly { kind: RootApproval; label: string; short: string; note: string }[] = [
  { kind: "hostexec", label: "Root approves host exec", short: "Host exec", note: "Sessions' requests to run a command on a host go to this room's root. You can still decide them." },
  { kind: "addons", label: "Root approves global add-ons", short: "Add-ons", note: "Skills, Pi packages and MCP servers a session asks to add for every room." },
  { kind: "hosts", label: "Root changes room host rules", short: "Host rules", note: "Which hosts this room's new boxes may go to." },
  { kind: "pools", label: "Root adds node pools", short: "Pools", note: "New machines from a node pool. Pools run scripts that can cost money." },
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

const someOn = (room: Pick<RoomSummary, "rootApproves">) => rootApprovals.some(({ kind }) => approvalOn(room, kind));

// The list's chips: every room, Autopilot (all four on), some on, all off.
export const approvalChips: Chip<RoomSummary>[] = [
  { id: "all", label: "All" },
  { id: "autopilot", label: "Autopilot", test: autopilotOn },
  { id: "some", label: "Some on", test: room => someOn(room) && !autopilotOn(room) },
  { id: "off", label: "All off", test: room => !someOn(room) },
];

// What a narrow row says in place of the four switches.
export const approvalsLine = (room: RoomSummary) => autopilotOn(room) ? "Autopilot"
  : rootApprovals.filter(({ kind }) => approvalOn(room, kind)).map(item => item.short).join(", ") || "All off";
