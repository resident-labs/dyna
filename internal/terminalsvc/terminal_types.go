package terminalsvc

import "time"

type Info struct {
	Title        string    `json:"title"`
	Ref          string    `json:"ref"`
	Alive        bool      `json:"alive"`
	ID           string    `json:"id"`
	RoomID       string    `json:"roomId"`
	SessionID    string    `json:"sessionId,omitempty"`
	Cwd          string    `json:"cwd"`
	Shell        string    `json:"shell"`
	Cols         uint16    `json:"cols"`
	Rows         uint16    `json:"rows"`
	Created      time.Time `json:"created"`
	LastActivity time.Time `json:"lastActivity"`
	Exited       bool      `json:"exited"`
	ExitCode     int       `json:"exitCode"`
}

// Event carries raw bytes (base64 in JSON). Start and Offset are byte offsets.
// Reset means the requested offset fell outside the retained buffer.
type Event struct {
	ID       string `json:"id"`
	Data     []byte `json:"data" tstype:"string | null"`
	Start    uint64 `json:"start"`
	Offset   uint64 `json:"offset"`
	Reset    bool   `json:"reset,omitempty"`
	Exited   bool   `json:"exited"`
	ExitCode int    `json:"exitCode"`
}

type ShellJob struct {
	ID      string     `json:"id"`
	Owner   string     `json:"owner,omitempty"`
	Command []string   `json:"command" tstype:"string[] | null"`
	Name    string     `json:"name,omitempty"`
	Cwd     string     `json:"cwd"`
	State   string     `json:"state"`
	Code    int        `json:"code"`
	Started time.Time  `json:"started"`
	Ended   *time.Time `json:"ended,omitempty"`
	// Signal and StopBy say how a stopped job ended; BackgroundAt is when a
	// foreground call outlived its limit; OutputLines is known once it ended.
	Signal       string     `json:"signal,omitempty"`
	StopBy       string     `json:"stopBy,omitempty"`
	BackgroundAt *time.Time `json:"backgroundAt,omitempty"`
	OutputLines  int        `json:"outputLines,omitempty"`
	// OutputBytes and Tail (the last lines, plain text) show a job that is
	// open or just ended. The log has the rest.
	OutputBytes int64 `json:"outputBytes,omitempty"`
	// LogPruned is set when the job's log file is gone, for a job whose row
	// carries a tail.
	LogPruned bool   `json:"logPruned,omitempty"`
	Tail      string `json:"tail,omitempty"`
}

type ShellLog struct {
	Text      string `json:"text"`
	Truncated bool   `json:"truncated"`
}

// ShellLogChunk is one forward read of a log: Next is where to read on, and
// Size is the log's length now.
type ShellLogChunk struct {
	Text string `json:"text"`
	Next int64  `json:"next"`
	Size int64  `json:"size"`
}
