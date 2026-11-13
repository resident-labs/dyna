package contract

// RoomName is a room's name, as the owner knows it.
type RoomName string

// RoomLimits is the room budget; zero is unset.
type RoomLimits struct {
	CPUs     int   `json:"cpus"`
	MemBytes int64 `json:"memBytes"`
}

type RoomRepo struct {
	PREvents bool     `json:"prEvents"`
	Repo     RepoName `json:"repo"`
	Default  bool     `json:"default"`
}
