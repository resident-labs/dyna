package roomsvc

// LoadOwner names the session or workflow run that owns a system-load row.
// Address and Room are empty for a run or an ID that cannot be resolved.
type LoadOwner struct {
	ID      string `json:"id"`
	Address string `json:"address,omitempty"`
	Name    string `json:"name"`
	Room    string `json:"room,omitempty"`
	RoomID  string `json:"roomId,omitempty"`
	Hue     int    `json:"hue"`
}

// LoadOwnerCores is a LoadOwner with the cores it used.
type LoadOwnerCores struct {
	LoadOwner `tstype:",extends"`
	Cores     float64 `json:"cores"`
}
