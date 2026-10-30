package computerweb

import (
	"dyna-agent/internal/computers"
	"time"
)

// App is one window of a computer.
type App struct {
	AppID string `json:"appId"`
	Title string `json:"title"`
}

// Info is the view model of one session's computer. State is "none" (no
// computer yet), "idle" (registered, no compositor), "running" (labwc is up)
// "starting" (labwc is coming up), or "stopped" (stopped after idle or on request; last picture kept).
type Info struct {
	LatestStatus *computers.ActionIntent `json:"latestStatus,omitempty"`
	Session      string                  `json:"session"`
	Name         string                  `json:"name"`
	Room         string                  `json:"room,omitempty"`
	State        string                  `json:"state"`
	Reason       string                  `json:"reason,omitempty"`
	Width        int                     `json:"width,omitempty"`
	Height       int                     `json:"height,omitempty"`
	Renderer     string                  `json:"renderer,omitempty"`
	Apps         []App                   `json:"apps" tstype:"App[] | null"`
	Control      string                  `json:"control,omitempty"`
	Viewers      int                     `json:"viewers"`
	Stream       computers.Stream        `json:"stream"`
	IdleSeconds  int64                   `json:"idleSeconds,omitempty"`
	RSSKiB       int64                   `json:"rssKiB"`
	StreamRSSKiB int64                   `json:"streamRssKiB"`
	StartedAt    *time.Time              `json:"startedAt,omitempty"`
	StartupMS    int64                   `json:"startupMs,omitempty"`
	// ShotAt is when last.png was taken. It is nil when there is no picture.
	ShotAt *time.Time `json:"shotAt,omitempty"`
}

// Event is what the UI gets on the app event stream. Kind is always
// "computer". Type is the daemon's event type. Computer carries the new
// status when the event has one. Action is a short line for the feed.
type Event struct {
	Kind     string `json:"kind" tstype:"'computer'"`
	RoomID   string `json:"roomId,omitempty"`
	Session  string `json:"session"`
	Type     string `json:"type"`
	Computer *Info  `json:"computer,omitempty"`
	Action   string `json:"action,omitempty"`
}
