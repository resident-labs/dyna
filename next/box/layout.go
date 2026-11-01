package box

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"

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
//	<Data>/rooms/<room>/shared/                     /box/shared, the one copy, which the engine host's boxes bind directly
//	<HostData>/rooms/<room>/instructions/AGENTS.md  the room copy a box mounts
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
//	<HostData>/share/rooms/<room>/shared/           /box/shared on another host, through its one mount of <Data>/rooms
//	<HostData>/share/cache/                         that mount's local cache
//	<HostData>/sock/share.sock                      the forwarded socket that mount reads through
//	<HostData>/sock/<box>/engine.sock               a box's forwarded socket, gone with the box
//	<HostData>/runs/<run>/                          a subscription Run's files, gone with the Run
//
// Box volumes are Podman's, so they are not here. One path inside them is Dyna's:
//
//	/box/attachments/<seq>/<name>                   the files of post <seq>, stored when the post reaches the box
type Layout struct{ Data, HostData string }

// AttachmentFolder is where a box keeps the files of the posts it reads. It is a variable so that tests can run outside a box.
var AttachmentFolder = "/box/attachments"

const (
	instructionsFile = "AGENTS.md"
	composeFolder    = "compose"
	sharedFolder     = "shared"
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

// LEGACY(v1-transfer): Archive and the <Data>/archive/ line above, the v1 archive folder. Remove after the transfer.
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

// Shared is a room's shared folder, the one copy that the boxes of every host see at /box/shared.
func (l Layout) Shared(room contract.RoomID) string {
	return filepath.Join(l.room(room), sharedFolder)
}

// IsShared says whether path is a room's shared folder.
func (l Layout) IsShared(path string) bool {
	return filepath.Base(path) == sharedFolder && filepath.Dir(filepath.Dir(path)) == l.Rooms()
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

// BoxAttachments names the files of post seq inside a box, in the post's order. The post's sequence keeps two posts that
// both attach image.png apart, and the same post always gets the same paths, so a replay finds what it stored.
func (Layout) BoxAttachments(seq int64, files []contract.Attachment) []string {
	paths := make([]string, len(files))
	used := make(map[string]bool, len(files))
	for i, file := range files {
		name := attachmentName(file.Name)
		stem, ext := splitExtension(name)
		for n := 2; used[name]; n++ {
			name = stem + "-" + strconv.Itoa(n) + ext
		}
		used[name] = true
		paths[i] = filepath.Join(AttachmentFolder, strconv.FormatInt(seq, 10), name)
	}
	return paths
}

// attachmentName turns a sender's file name into one path segment that needs no quoting in a frame.
func attachmentName(name string) string {
	safe := strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsNumber(r) || r == '.' || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, strings.ToValidUTF8(name, "_"))
	safe = strings.TrimLeft(safe, ".")
	if stem, ext := splitExtension(safe); len(safe) > 200 {
		safe = strings.ToValidUTF8(stem[:200-len(ext)], "") + ext
	}
	if safe == "" {
		return "file"
	}
	return safe
}

// splitExtension keeps a short extension whole. A long one is just the end of the name.
func splitExtension(name string) (string, string) {
	ext := filepath.Ext(name)
	if len(ext) > 16 {
		ext = ""
	}
	return strings.TrimSuffix(name, ext), ext
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

// HostBinary is the engine binary a host runs, which the current link names.
func (l Layout) HostBinary() string { return filepath.Join(l.HostBin(), "current", "dyna") }

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

// HostRun is the folder of one subscription Run's files.
func (l Layout) HostRun(id contract.BoxID) string {
	return filepath.Join(l.HostData, "runs", string(id))
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

// HostShareMount is where a host other than the engine's mounts <Data>/rooms.
func (l Layout) HostShareMount() string { return filepath.Join(l.HostData, "share", "rooms") }

// HostShareCache is that mount's local cache.
func (l Layout) HostShareCache() string { return filepath.Join(l.HostData, "share", "cache") }

// HostShareSocket is the forwarded socket that mount reads through.
func (l Layout) HostShareSocket() string { return filepath.Join(l.HostData, "sock", "share.sock") }

// HostShared is a room's shared folder as a host other than the engine's sees it. A box binds this one folder at /box/shared.
func (l Layout) HostShared(room contract.RoomID) string {
	return filepath.Join(l.HostShareMount(), string(room), sharedFolder)
}
