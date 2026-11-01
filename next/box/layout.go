package box

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/resident-labs/dyna/contract"
)

// Layout names every path of Dyna's data on the control plane and on hosts, and no other code spells these paths.
//
// A room is one folder on each side, so deleting a room removes one folder per machine:
//
//	<Data>/rooms/<room>/AGENTS.md                   room instructions
//	<Data>/rooms/<room>/compose/<name>.yaml         room compose files
//	<Data>/rooms/<room>/files/<sha256>              attachments and large outputs
//	<Data>/rooms/<room>/transcripts/<session>/      transcript copies
//	<HostData>/rooms/<room>/instructions/AGENTS.md  the room copy a box mounts
//	<HostData>/rooms/<room>/shared/                 /box/shared
//
// Everything else belongs to no single room:
//
//	<Data>/AGENTS.md                                global instructions
//	<Data>/compose/<name>.yaml                      global compose files
//	<Data>/store.db, <Data>/store.lock              the store and its lock
//	<Data>/backups/                                 local copies of the store
//	<Data>/archive/                                 the read-only v1 archive
//	<Data>/connect/                                 Connect credentials
//	<Data>/cliproxy/, <Data>/cliproxy/auth-v2/      the model proxy and its sign-in folder
//	<Data>/git/<owner>/<repo>.git                   repository mirrors, which rooms share
//	<HostData>/instructions/global/AGENTS.md        the global copy a box mounts
//	<HostData>/git/<owner>/<repo>.git               repository caches, which rooms share
//	<HostData>/bin/<version>/dyna                   engine binaries, with a current link
//	<HostData>/cache/                               dependency caches, which boxes share
//	<HostData>/sock/<box>/engine.sock               a box's forwarded socket, gone with the box
//
// Box volumes are Podman's, so they are not here.
type Layout struct{ Data, HostData string }

const (
	instructionsFile = "AGENTS.md"
	composeFolder    = "compose"
	// SharedFolder is the name of a room's shared folder, for a host script that visits every room.
	SharedFolder = "shared"
	// EngineSocket is the name of a box's socket in its host folder.
	EngineSocket = "engine.sock"
)

func roomFolder(rooms string, room contract.RoomID) (string, error) {
	name := string(room)
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		return "", fmt.Errorf("invalid room id %q", room)
	}
	if !filepath.IsAbs(rooms) {
		return "", fmt.Errorf("room data folder %q is not absolute", rooms)
	}
	return filepath.Join(rooms, name), nil
}

func repoFolder(git string, repo contract.RepoName) (string, error) {
	parts := strings.Split(string(repo), "/")
	if len(parts) != 2 {
		return "", fmt.Errorf("invalid repository name %q", repo)
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || strings.Trim(part, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_.-") != "" {
			return "", fmt.Errorf("invalid repository name %q", repo)
		}
	}
	if !filepath.IsAbs(git) {
		return "", fmt.Errorf("repository folder %q is not absolute", git)
	}
	return filepath.Join(git, parts[0], parts[1]+".git"), nil
}

// Store is the engine's database.
func (l Layout) Store() string { return filepath.Join(l.Data, "store.db") }

// StoreLock is the file that keeps a second engine off the data folder.
func (l Layout) StoreLock() string { return filepath.Join(l.Data, "store.lock") }

// V1Store is where a v1 engine kept its database. The engine only checks for it, to refuse the folder.
func (l Layout) V1Store() string { return filepath.Join(l.Data, "dyna.db") }

// Backups is the folder of local store copies.
func (l Layout) Backups() string { return filepath.Join(l.Data, "backups") }

// Archive is the read-only archive that the v1 cutover left.
func (l Layout) Archive() string { return filepath.Join(l.Data, "archive") }

// Connect is the folder of Connect credentials.
func (l Layout) Connect() string { return filepath.Join(l.Data, "connect") }

// Proxy is the model proxy's folder.
func (l Layout) Proxy() string { return filepath.Join(l.Data, "cliproxy") }

// ProxyAuth is the model proxy's sign-in folder.
func (l Layout) ProxyAuth() string { return filepath.Join(l.Proxy(), "auth-v2") }

// Git is a repository's mirror on the control plane. It checks the name because a repository name comes from outside.
func (l Layout) Git(repo contract.RepoName) (string, error) {
	return repoFolder(filepath.Join(l.Data, "git"), repo)
}

// Rooms is the folder of every room on the control plane.
func (l Layout) Rooms() string { return filepath.Join(l.Data, "rooms") }

// Room is the room's whole folder on the control plane. It checks the ID and the root because callers remove this path: an empty ID would name every room, and an unset root would name a folder under the working directory.
func (l Layout) Room(room contract.RoomID) (string, error) { return roomFolder(l.Rooms(), room) }

func (l Layout) room(room contract.RoomID) string {
	return filepath.Join(l.Rooms(), string(room))
}

// Instructions is the instructions file of a room, or the global one for no room.
func (l Layout) Instructions(room contract.RoomID) string {
	if room == "" {
		return filepath.Join(l.Data, instructionsFile)
	}
	return filepath.Join(l.room(room), instructionsFile)
}

// Compose is the folder of a room's compose files, or the global one for no room.
func (l Layout) Compose(room contract.RoomID) string {
	if room == "" {
		return filepath.Join(l.Data, composeFolder)
	}
	return filepath.Join(l.room(room), composeFolder)
}

// Files is the folder of a room's blobs.
func (l Layout) Files(room contract.RoomID) string { return filepath.Join(l.room(room), "files") }

// File is one blob of a room. A room holds its own link to each blob it uses, so deleting another room cannot break it.
func (l Layout) File(room contract.RoomID, hash string) string {
	return filepath.Join(l.Files(room), hash)
}

// FindFile finds a blob in any room, so the owner can bring it to another room. The hash must be valid.
func (l Layout) FindFile(hash string) (string, bool) {
	rooms, err := os.ReadDir(l.Rooms())
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

// HostGitRoot is the folder of every repository cache on a host.
func (l Layout) HostGitRoot() string { return filepath.Join(l.HostData, "git") }

// HostGit is a repository's cache on a host, with the same check as Git.
func (l Layout) HostGit(repo contract.RepoName) (string, error) {
	return repoFolder(l.HostGitRoot(), repo)
}

// HostBin is the folder of engine binaries on a host.
func (l Layout) HostBin() string { return filepath.Join(l.HostData, "bin") }

// HostCache is the dependency cache folder of a host.
func (l Layout) HostCache() string { return filepath.Join(l.HostData, "cache") }

// HostSocketFolder is the folder a box mounts for its socket.
func (l Layout) HostSocketFolder(id contract.BoxID) string {
	return filepath.Join(l.HostData, "sock", string(id))
}

// HostSocket is a box's forwarded socket on its host.
func (l Layout) HostSocket(id contract.BoxID) string {
	return filepath.Join(l.HostSocketFolder(id), EngineSocket)
}

// HostProbeSocket is the socket that host admission forwards to test forwarding.
func (l Layout) HostProbeSocket(token string) string {
	return filepath.Join(l.HostData, "sock", "probe-"+token+".sock")
}

// HostRooms is the folder of every room on a host.
func (l Layout) HostRooms() string { return filepath.Join(l.HostData, "rooms") }

// HostRoom is the room's whole folder on a host, with the same check as Room.
func (l Layout) HostRoom(room contract.RoomID) (string, error) {
	return roomFolder(l.HostRooms(), room)
}

func (l Layout) hostRoom(room contract.RoomID) string {
	return filepath.Join(l.HostRooms(), string(room))
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
	return filepath.Join(l.hostRoom(room), SharedFolder)
}
