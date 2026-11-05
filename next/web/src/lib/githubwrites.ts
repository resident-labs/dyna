import type { GitHubWriteGroup, RoomSetRequest, RoomSummary } from "./contract.gen.ts";

// Settings > Approvals' GitHub writes (spec 7.9): one switch for each group, over the repositories the room's setting names, or all
// of the room's (*) when it has none set yet.
export const writeGroups: { group: GitHubWriteGroup; label: string; note: string }[] = [
  { group: "issues", label: "Issues", note: "Open and edit issues, comment, and label." },
  { group: "pulls", label: "Pull requests", note: "Comment, review without approving, and edit a PR's title, body and labels. Never merge, approve, close or retarget." },
  { group: "projects", label: "Projects", note: "Add items to a project and set their fields." },
];

export const writeOn = (room: Pick<RoomSummary, "githubWrites">, group: GitHubWriteGroup) => (room.githubWrites?.groups ?? []).includes(group);

// The repositories the setting names, as the help says them.
export const writesRepos = (room: Pick<RoomSummary, "githubWrites">) => {
  const repos = room.githubWrites?.repos ?? [];
  return repos.length === 0 || repos.includes("*") ? "all of this room's repositories" : repos.join(", ");
};

export const writeLabel = (group: GitHubWriteGroup, room: Pick<RoomSummary, "name">) =>
  `GitHub writes: ${writeGroups.find(each => each.group === group)!.label} in ${room.name}`;

export const writeRequest = (room: RoomSummary, group: GitHubWriteGroup, on: boolean): RoomSetRequest => ({
  room: room.id,
  githubWrites: {
    repos: room.githubWrites?.repos?.length ? room.githubWrites.repos : ["*"],
    groups: writeGroups.map(each => each.group).filter(each => each === group ? on : writeOn(room, each)),
  },
});
