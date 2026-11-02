package contract

// RoomName is a room's name, as the owner knows it.
type RoomName string

// RoomLimits is the room budget; zero is unset.
type RoomLimits struct {
	CPUs     int   `json:"cpus"`
	MemBytes int64 `json:"memBytes"`
}

type CIMode string

const (
	CIGitHub CIMode = "github"
	CIBox    CIMode = "box"
)

type RoomRepo struct {
	Repo    RepoName `json:"repo"`
	Default bool     `json:"default"`
}
