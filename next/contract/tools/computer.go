package tools

import "reflect"

func typeOf(v any) reflect.Type { return reflect.TypeOf(v) }

// The computer's actions are v1's, one to a call (spec 5.14). The box handles every action itself (box/computer).

// computerHelp opens the tool's description, for every action.
const computerHelp = "The box desktop is 1280x800, with Chromium in a window and a taskbar; any action but stop starts it. Computer tool actions in a box run one at a time, so parallel calls queue. Chromium page actions time out after 30 s; if a page screenshot times out, take a desktop screenshot or use the visual tool."

// computerFields describes the fields several actions take, once, so the tool's flat schema reads the same for each.
var computerFields = map[string]string{
	"status": "A few words on what this is for, shown to the owner watching the computer.",
	"x":      "Desktop pixels from the top left of the 1280x800 desktop.",
	"y":      "Desktop pixels from the top left of the 1280x800 desktop.",
	"keys":   "Key names: a character such as l, an alias (CTRL, SHIFT, ALT, SUPER, ENTER, ESC, TAB, SPACE, BACKSPACE, UP, PAGEDOWN) or an X keysym name such as F5 or Page_Down; a name such as ctrl+l is a chord. key presses them; click, drag and scroll hold them down meanwhile.",
	"width":  "A region's width, with x, y and height.",
	"height": "A region's height, with x, y and width.",
}

type ComputerPoint struct {
	X int `json:"x"`
	Y int `json:"y"`
}

type ComputerOpenRequest struct {
	URL    string `json:"url"`
	Status string `json:"status,omitempty"`
}

type ComputerClickRequest struct {
	Selector string   `json:"selector,omitempty"`
	X        *int     `json:"x,omitempty"`
	Y        *int     `json:"y,omitempty"`
	Button   string   `json:"button,omitempty" desc:"left (default), middle or right."`
	Double   bool     `json:"double,omitempty" desc:"A double click."`
	Keys     []string `json:"keys,omitempty"`
	Status   string   `json:"status,omitempty"`
}

type ComputerMoveRequest struct {
	X      *int   `json:"x,omitempty"`
	Y      *int   `json:"y,omitempty"`
	Status string `json:"status,omitempty"`
}

type ComputerDragRequest struct {
	Path   []ComputerPoint `json:"path" desc:"2 to 200 desktop points, each {x, y}. The left button goes down at the first and up at the last."`
	StepMS *int            `json:"step_ms,omitempty" desc:"The pause after each point, 0 to 500 ms; 10 by default. Set, the press lasts a step too, so a frame-driven app sees each point."`
	Keys   []string        `json:"keys,omitempty"`
	Status string          `json:"status,omitempty"`
}

type ComputerScrollRequest struct {
	X       *int     `json:"x,omitempty"`
	Y       *int     `json:"y,omitempty"`
	ScrollX int      `json:"scroll_x,omitempty" desc:"Wheel notches, -100 to 100; positive scrolls right. A notch is about 50 px in Chromium."`
	ScrollY int      `json:"scroll_y,omitempty" desc:"Wheel notches, -100 to 100; positive scrolls down. A notch is about 50 px in Chromium."`
	Keys    []string `json:"keys,omitempty"`
	Status  string   `json:"status,omitempty"`
}

type ComputerKeyRequest struct {
	Keys   []string `json:"keys"`
	HoldMS *int     `json:"hold_ms,omitempty" desc:"How long the keys stay down, 0 to 10000 ms; 50 by default. Hold a game key with it."`
	Status string   `json:"status,omitempty"`
}

type ComputerTypeRequest struct {
	Selector string `json:"selector,omitempty"`
	Text     string `json:"text"`
	Status   string `json:"status,omitempty"`
}

type ComputerTextRequest struct {
	Selector string `json:"selector,omitempty"`
}

type ComputerWaitRequest struct {
	MS *int `json:"ms,omitempty" desc:"0 to 10000 ms; 1000 by default."`
}

type ComputerScreenshotRequest struct {
	X      *int `json:"x,omitempty"`
	Y      *int `json:"y,omitempty"`
	Width  int  `json:"width,omitempty"`
	Height int  `json:"height,omitempty"`
	Page   bool `json:"page,omitempty" desc:"Capture Chromium's page beyond its window, clipped to 16384 px tall."`
}

type ComputerLaunchRequest struct {
	Command   string `json:"command" desc:"A command for sh, such as \"chromium --new-window https://example.com\" or an app the box has installed."`
	TimeoutMS int    `json:"timeout_ms,omitempty" desc:"How long to wait for the new window, up to 60000 ms; 15000 by default."`
	Status    string `json:"status,omitempty"`
}

type ComputerStopRequest struct {
	Status string `json:"status,omitempty"`
}

func computerActions() []BoxAction {
	action := func(name string, handler BoxHandler, request any, help string) BoxAction {
		return BoxAction{Tool: "computer", Name: name, Handler: handler, Request: typeOf(request), Help: help, CLI: []string{"computer " + name}}
	}
	return []BoxAction{
		action("open", ComputerOpen, ComputerOpenRequest{}, "Open url in Chromium's current tab and wait for it to load; returns the page title. Scripts can drive the same Chromium with Playwright's connectOverCDP(\"http://127.0.0.1:9222\")."),
		action("click", ComputerClick, ComputerClickRequest{}, "Click with a real pointer, at x, y on the desktop in any app, or at the center of the element at selector in Chromium, which scrolls into view first. button and double as needed; keys stay down during it, such as SHIFT for a shift-click. Real clicks pass checks that refuse synthetic ones. Returns what changed on the screen once it settles."),
		action("move", ComputerMove, ComputerMoveRequest{}, "Move the pointer to x, y, for hover menus and tooltips."),
		action("drag", ComputerDrag, ComputerDragRequest{}, "Press the left button at the path's first point, move through the rest and let go at the last; for sliders, selections and moving windows. Returns what changed."),
		action("scroll", ComputerScroll, ComputerScrollRequest{}, "Turn the mouse wheel at x, y, or the desktop's center: scroll_y notches down (negative up) and scroll_x right (negative left). Returns what changed."),
		action("key", ComputerKey, ComputerKeyRequest{}, "Press keys together on the desktop, modifiers first, and let them go after hold_ms: [\"CTRL\", \"l\"], [\"ctrl+shift+t\"], [\"shift+tab\"], [\"ENTER\"], [\"F5\"] or a character such as [\"é\"]. It goes to the window with the keyboard focus. Returns what changed."),
		action("type", ComputerType, ComputerTypeRequest{}, "Type text: with selector, into that element in Chromium, as a paste; without, as keystrokes into the window with the keyboard focus, in any app, any characters. Newlines press Enter. Returns what changed."),
		action("text", ComputerText, ComputerTextRequest{}, "Read the text of the element at selector in Chromium's page, or of the whole page without one."),
		action("wait", ComputerWait, ComputerWaitRequest{}, "Wait ms, for an app that needs time before the next input."),
		action("screenshot", ComputerScreenshot, ComputerScreenshotRequest{}, "Return a PNG of the whole desktop, any app, without the pointer; or of a region, x, y, width and height, which costs less to read; or of Chromium's whole page with page."),
		action("launch", ComputerLaunch, ComputerLaunchRequest{}, "Start command on the desktop and wait for its new window; returns the window's title and the log of its output. It runs until it exits or the desktop stops. For a command without a window, use bash; DISPLAY=:1 reaches the desktop from there."),
		action("stop", ComputerStop, ComputerStopRequest{}, "Stop the desktop and everything on it, to free its memory; the next action starts it again."),
	}
}
