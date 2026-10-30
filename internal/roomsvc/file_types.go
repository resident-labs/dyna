package roomsvc

// PathCheck says what New room would do with a typed path.
type PathCheck struct {
	State string `json:"state"`
	// Repo is the repository root a room would use (state repo).
	Repo string `json:"repo,omitempty"`
	// Name is the default room name.
	Name string `json:"name,omitempty"`
	// RoomID and RoomName name the room that already holds this repo.
	RoomID   string `json:"roomId,omitempty"`
	RoomName string `json:"roomName,omitempty"`
}

// CreatedRoom is the room a request made or found.
type CreatedRoom struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Repo string `json:"repo"`
	// Existed is true when the repo already had a room.
	Existed bool `json:"existed"`
}
