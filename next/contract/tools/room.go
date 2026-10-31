package tools

import "github.com/resident-labs/dyna/contract"

func roomEntries() []Entry {
	return []Entry{
		entry("room", "log", contract.MethodEventsList, typ[contract.EventsListRequest](), Session, "Read events in room. afterSeq and beforeSeq bound the page; limit, types and home filter it.", "room log"),
		entry("room", "repos", contract.MethodRoomRepos, typ[contract.RoomReposRequest](), Session, "List repositories in room.", "room repo list"),
		entry("room", "pause", contract.MethodRoomPause, typ[contract.RoomPauseRequest](), Tree, "Set paused for room.", "room pause", "room resume"),
		entry("room", "add_repo", contract.MethodRoomRepoAdd, typ[contract.RoomRepoAddRequest](), Owner, "Add a repository", "room repo add"),
		entry("room", "create", contract.MethodRoomCreate, typ[contract.RoomCreateRequest](), Owner, "Create a room", "room create"),
		entry("room", "limit", contract.MethodRoomSetLimits, typ[contract.RoomSetLimitsRequest](), Owner, "Set room limits", "room limit"),
	}
}
