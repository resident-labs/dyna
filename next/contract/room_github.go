package contract

const MethodRoomGitHub Method = "room.github"

// RoomGitHubRequest carries a credential only over the owner socket. Responses
// and persisted events contain public metadata only.
type RoomGitHubRequest struct {
	Room  RoomID `json:"room"`
	Token string `json:"token,omitempty"`
	Name  string `json:"name,omitempty"`
	Email string `json:"email,omitempty"`
	Clear bool   `json:"clear,omitempty"`
}
type RoomGitHubResponse struct {
	Login string `json:"login"`
	Name  string `json:"name"`
	Email string `json:"email"`
}
