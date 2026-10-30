package websvc

// Route is one way to reach the server: the app origin a browser opens and
// the card origin that serves its cards. Both are origins: scheme, host and
// a port only when it is not the scheme's default.
type Route struct {
	App   string `json:"app"`
	Cards string `json:"cards"`
}

// PushStatus is the reply to a push "status" call. The web client reads all
// three fields on every reply, so none of them are omitted.
type PushStatus struct {
	Subscribed bool   `json:"subscribed"`
	Names      bool   `json:"names"`
	Key        string `json:"key"`
}
