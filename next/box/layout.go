package box

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/resident-labs/dyna/contract"
)

// Layout names the folders of room data, so a room is one folder on each side and no other code spells these paths.
//
//	<Data>/AGENTS.md                                global instructions
//	<Data>/rooms/<room>/AGENTS.md                   room instructions
//	<Data>/rooms/<room>/files/<sha256>              attachments and large outputs
//	<Data>/rooms/<room>/transcripts/<session>/      transcript copies
//	<HostData>/instructions/global/AGENTS.md        the global copy a box mounts
//	<HostData>/rooms/<room>/instructions/AGENTS.md  the room copy a box mounts
//	<HostData>/rooms/<room>/shared/                 /box/shared
type Layout struct{ Data, HostData string }

const instructionsFile = "AGENTS.md"

func roomFolder(root string, room contract.RoomID) (string, error) {
	name := string(room)
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		return "", fmt.Errorf("invalid room id %q", room)
	}
	return filepath.Join(root, "rooms", name), nil
}

// Room is the room's whole folder on the control plane. It checks the ID because callers remove this path, and an empty ID would name every room.
func (l Layout) Room(room contract.RoomID) (string, error) { return roomFolder(l.Data, room) }

// HostRoom is the room's whole folder on a host, with the same check.
func (l Layout) HostRoom(room contract.RoomID) (string, error) { return roomFolder(l.HostData, room) }

func (l Layout) room(room contract.RoomID) string {
	return filepath.Join(l.Data, "rooms", string(room))
}

func (l Layout) hostRoom(room contract.RoomID) string {
	return filepath.Join(l.HostData, "rooms", string(room))
}

// Instructions is the instructions file of a room, or the global one for no room.
func (l Layout) Instructions(room contract.RoomID) string {
	if room == "" {
		return filepath.Join(l.Data, instructionsFile)
	}
	return filepath.Join(l.room(room), instructionsFile)
}

// Files is the folder of a room's blobs.
func (l Layout) Files(room contract.RoomID) string { return filepath.Join(l.room(room), "files") }

// File is one blob of a room. A room holds its own link to each blob it uses, so deleting another room cannot break it.
func (l Layout) File(room contract.RoomID, hash string) string {
	return filepath.Join(l.Files(room), hash)
}

// FindFile finds a blob in any room, so the owner can bring it to another room. The hash must be valid.
func (l Layout) FindFile(hash string) (string, bool) {
	rooms, err := os.ReadDir(filepath.Join(l.Data, "rooms"))
	if err != nil {
		return "", false
	}
	for _, room := range rooms {
		path := l.File(contract.RoomID(room.Name()), hash)
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
			return path, true
		}
	}
	return "", false
}

// Transcripts is the folder of one session's transcript copies.
func (l Layout) Transcripts(room contract.RoomID, session contract.SessionID) string {
	return filepath.Join(l.room(room), "transcripts", string(session))
}

// HostInstructions is the instructions file a host keeps for a room, or the global one for no room. A box mounts its folder.
func (l Layout) HostInstructions(room contract.RoomID) string {
	if room == "" {
		return filepath.Join(l.HostData, "instructions", "global", instructionsFile)
	}
	return filepath.Join(l.hostRoom(room), "instructions", instructionsFile)
}

// HostShared is the folder that every box of the room mounts at /box/shared.
func (l Layout) HostShared(room contract.RoomID) string {
	return filepath.Join(l.hostRoom(room), "shared")
}
