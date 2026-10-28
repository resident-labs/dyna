// Package computers manages session desktops. Computers share the owner's Unix
// account and files; they are not a security sandbox.
package computers

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"dyna-agent/internal/store"
)

type State string

const (
	StateCreated     State = "created"
	StateStarting    State = "starting"
	StateRunning     State = "running"
	StateIdleStopped State = "idle-stopped"
	StateDestroyed   State = "destroyed"
)

type ControlMode string

const (
	ControlAgent ControlMode = "agent"
	ControlHuman ControlMode = "human"
)

type App struct {
	AppID string `json:"app_id"`
	Title string `json:"title"`
}
type Stream struct {
	Running bool   `json:"running"`
	Codec   string `json:"codec"`
	Encoder string `json:"encoder"`
}
type ActionIntent struct {
	Text string    `json:"text"`
	At   time.Time `json:"at"`
}

type RestartAdmission struct {
	Reason string `json:"reason,omitempty"`
}

type Status struct {
	LatestStatus     *ActionIntent `json:"latest_status,omitempty"`
	ControlReason    *ActionIntent `json:"control_reason,omitempty"`
	SessionID        string        `json:"session_id"`
	Room             string        `json:"room,omitempty"`
	State            State         `json:"state"`
	Width            int           `json:"width"`
	Height           int           `json:"height"`
	RefreshHz        int           `json:"refresh_hz"`
	MemoryCurrentKiB int64         `json:"memory_current_kib,omitempty"`
	Renderer         string        `json:"renderer"`
	// RenderNode is the GPU the desktop renders on, such as
	// /dev/dri/renderD128; empty for the software renderer (D-352).
	RenderNode string `json:"render_node,omitempty"`
	// RenderEnv holds the GPU variables the running desktop resolved at its
	// start (DRI_PRIME or the GLVND NVIDIA pair). App launches outside the
	// daemon read these, so every path renders where the desktop renders.
	RenderEnv      map[string]string `json:"render_env,omitempty"`
	Apps           []App             `json:"apps"`
	Control        ControlMode       `json:"control"`
	Viewers        int               `json:"viewers"`
	Stream         Stream            `json:"stream"`
	IdleSeconds    int64             `json:"idle_seconds"`
	RSSKiB         int64             `json:"rss_kib"`
	StartedAt      time.Time         `json:"started_at"`
	StartupMS      int64             `json:"startup_ms"`
	ShotCapturedAt time.Time         `json:"shot_captured_at"`
	StreamRSSKiB   int64             `json:"stream_rss_kib"`
	Reason         string            `json:"reason,omitempty"`
	Recording      bool              `json:"recording,omitempty"`
	// Runs counts foreground computer run commands holding a lease.
	Runs int `json:"runs,omitempty"`
	// Keep stops the idle timeout from stopping the computer. It is stored
	// with the computer and survives a service restart.
	Keep bool `json:"keep,omitempty"`
}

// RestartReason reports active use that a computer service restart would
// interrupt. A desktop without an app or foreground command is quiet.
func (s Status) RestartReason() string {
	if s.Runs > 0 {
		return fmt.Sprintf("computer %s has %d foreground command(s) running", s.SessionID, s.Runs)
	}
	if (s.State == StateRunning || s.State == StateStarting) && len(s.Apps) > 0 {
		return fmt.Sprintf("computer %s has %d app(s) open", s.SessionID, len(s.Apps))
	}
	return ""
}

// Recording describes a screen recording: running now, or the last one finished.
type Recording struct {
	Path    string  `json:"path"`
	Seconds float64 `json:"seconds"`
	Bytes   int64   `json:"bytes,omitempty"`
	Running bool    `json:"running,omitempty"`
	Message string  `json:"message,omitempty"`
}
type Shot struct {
	Path       string    `json:"path"`
	CapturedAt time.Time `json:"captured_at"`
	Width      int       `json:"width"`
	Height     int       `json:"height"`
	Message    string    `json:"message,omitempty"`
}
type Frame struct {
	Shot
	ActionIndex *int  `json:"action_index"`
	OffsetMS    int64 `json:"offset_ms"`
}
type ActionResult struct {
	Shot
	Frames          []Frame `json:"frames"`
	ActionsExecuted int     `json:"actions_executed"`
}
type Opened struct {
	PID int    `json:"pid"`
	Log string `json:"log"`
	App App    `json:"app"`
}

type View struct {
	ViewerID  string `json:"viewer_id"`
	RFBSocket string `json:"rfb_socket"`
}
type Viewers struct {
	Viewers int `json:"viewers"`
}
type Control struct {
	Control ControlMode `json:"control"`
}
type Stopped struct {
	State State `json:"state"`
}

// Action retains raw fields so validation can distinguish missing values, reject
// unknown fields and reject fractional coordinates before any input is sent.
type Action map[string]json.RawMessage
type Request struct {
	Size        *Size    `json:"size,omitempty"`
	Command     []string `json:"command,omitempty"`
	Status      string   `json:"status,omitempty"`
	BrowserArgs []string `json:"browser_args,omitempty"`
	Session     string   `json:"session"`
	Room        string   `json:"room,omitempty"`
	Op          string   `json:"op"`
	Fresh       bool     `json:"fresh,omitempty"`
	// Keep is the new keep-running setting for the "keep" op.
	Keep      bool        `json:"keep,omitempty"`
	Actions   []Action    `json:"actions,omitempty"`
	ViewerID  string      `json:"viewer_id,omitempty"`
	Mode      ControlMode `json:"mode,omitempty"`
	TimeoutMS int64       `json:"timeout_ms,omitempty"`
	// Record limits for "record.start". Zero means the default.
	RecordSeconds int `json:"record_seconds,omitempty"`
	RecordFPS     int `json:"record_fps,omitempty"`
	RecordKbps    int `json:"record_kbps,omitempty"`
}
type Event struct {
	Type      string    `json:"type"`
	SessionID string    `json:"session_id"`
	TS        time.Time `json:"ts"`
	Status    *Status   `json:"status,omitempty"`
	Action    Action    `json:"action,omitempty"`
	Shot      *Shot     `json:"shot,omitempty"`
}

var validID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,95}$`)

func ValidateID(id string) error {
	if !validID.MatchString(id) {
		return fmt.Errorf("invalid computer session ID %q", id)
	}
	return nil
}
func Root() string         { return filepath.Join(store.DataDir(), "computers") }
func Dir(id string) string { return filepath.Join(Root(), id) }
func RuntimeDir(id string) string {
	return filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), "dyna", "computers", id)
}
func ControlSocket(id string) string {
	return filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), "dyna", "computers", "control.sock")
}
func EventsPath(id string) string { return filepath.Join(Dir(id), "events.jsonl") }

// Meta records stable public endpoints across daemon restarts.
type Meta struct {
	SessionID      string    `json:"session_id"`
	Room           string    `json:"room,omitempty"`
	WaylandDisplay string    `json:"wayland_display"`
	Display        string    `json:"display"`
	Created        time.Time `json:"created"`
}

// Browser identifies the acting computer's shared Chromium instance.
type Browser struct {
	Endpoint string `json:"endpoint"`
}
