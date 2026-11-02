package contract

import "time"

const (
	MethodAddonList       Method = "addon.list"
	MethodAddonSetEnabled Method = "addon.set_enabled"
	MethodBoxNotice       Method = "box.notice"
	MethodMCPAdd          Method = "mcp.add"
	MethodMCPList         Method = "mcp.list"
	MethodMCPRemove       Method = "mcp.remove"
	MethodPackageAdd      Method = "package.add"
	MethodPackageList     Method = "package.list"
	MethodPackageRemove   Method = "package.remove"
	MethodSkillList       Method = "skill.list"
	MethodSkillRemove     Method = "skill.remove"
	MethodSkillSave       Method = "skill.save"
)

// AddonKind names what an add-on is. Not every skill is a Pi package, so the three stay apart.
type AddonKind string

const (
	AddonSkill   AddonKind = "skill"
	AddonPackage AddonKind = "package"
	AddonMCP     AddonKind = "mcp"
)

// Addon is one skill, Pi package or MCP server. Room is empty for a global one.
type Addon struct {
	Kind        AddonKind  `json:"kind"`
	Name        string     `json:"name"`
	Room        RoomID     `json:"room,omitempty"`
	BuiltIn     bool       `json:"builtIn,omitempty"` // ships in the binary, and every box has it
	Disabled    bool       `json:"disabled,omitempty"`
	By          string     `json:"by,omitempty"` // "owner" or the name of the session that added it
	At          *time.Time `json:"at,omitempty"`
	Description string     `json:"description,omitempty"` // a skill's
	Source      string     `json:"source,omitempty"`      // a package's pinned source, or the link a skill came from
	URL         string     `json:"url,omitempty"`         // an HTTP MCP server's
	Command     string     `json:"command,omitempty"`     // a stdio MCP server's
	Args        []string   `json:"args,omitempty"`
}

// AddonListRequest asks for what a session in Room has: built-ins, global and that room. The owner's empty room means every room.
type AddonListRequest struct {
	Room RoomID `json:"room,omitempty"`
}

type AddonListResponse struct {
	Items []Addon `json:"items"`
}

// AddonChangeResponse holds the added item, or the operation that waits for the owner.
type AddonChangeResponse struct {
	Item      *Addon `json:"item,omitempty"`
	Operation OpID   `json:"operation,omitempty"`
}

type PackageAddRequest struct {
	Source string `json:"source"`
	Room   RoomID `json:"room,omitempty"` // a session's own room when empty
	Global bool   `json:"global,omitempty"`
}

// SkillSaveRequest carries a gzip tar of a skill's folder, or a link for the engine to fetch.
type SkillSaveRequest struct {
	Archive []byte `json:"archive,omitempty"`
	URL     string `json:"url,omitempty"`
	Room    RoomID `json:"room,omitempty"`
	Global  bool   `json:"global,omitempty"`
}

// MCPAddRequest names a server by URL or by Command and Args, which are one executable and its arguments, not a shell line.
type MCPAddRequest struct {
	Name    string   `json:"name"`
	URL     string   `json:"url,omitempty"`
	Command string   `json:"command,omitempty"`
	Args    []string `json:"args,omitempty"`
	Room    RoomID   `json:"room,omitempty"`
	Global  bool     `json:"global,omitempty"`
}

type AddonRemoveRequest struct {
	Name   string `json:"name"`
	Room   RoomID `json:"room,omitempty"`
	Global bool   `json:"global,omitempty"`
}

// Each kind's list, remove and add answer alike, and the method map wants one name per method.
type (
	MCPAddResponse        = AddonChangeResponse
	MCPListRequest        = AddonListRequest
	MCPListResponse       = AddonListResponse
	MCPRemoveRequest      = AddonRemoveRequest
	MCPRemoveResponse     = AddonChangeResponse
	PackageAddResponse    = AddonChangeResponse
	PackageListRequest    = AddonListRequest
	PackageListResponse   = AddonListResponse
	PackageRemoveRequest  = AddonRemoveRequest
	PackageRemoveResponse = AddonChangeResponse
	SkillListRequest      = AddonListRequest
	SkillListResponse     = AddonListResponse
	SkillRemoveRequest    = AddonRemoveRequest
	SkillRemoveResponse   = AddonChangeResponse
	SkillSaveResponse     = AddonChangeResponse
)

// AddonSetEnabledRequest turns one added item on or off. Room is empty for a global one.
type AddonSetEnabledRequest struct {
	Kind    AddonKind `json:"kind"`
	Name    string    `json:"name"`
	Room    RoomID    `json:"room,omitempty"`
	Enabled bool      `json:"enabled"`
}

type AddonSetEnabledResponse struct{}

// BoxNoticeRequest is what a box could not set up, in plain words.
type BoxNoticeRequest struct {
	Text string `json:"text"`
}

type BoxNoticeResponse struct{}

// SessionNotice tells a session and the owner what its box could not set up. It waits for the session's next turn and wakes nobody.
type SessionNotice struct {
	Session SessionID `json:"session"`
	Text    string    `json:"text"`
}

func (SessionNotice) EventType() EventType { return "session.notice" }
func (SessionNotice) isEventBody()         {}

// AddonGlobalArgs is a session's global change, which waits for the owner. Bundle is the hash of a skill's archive among the
// room's files; the other fields are as in the requests.
type AddonGlobalArgs struct {
	Remove      bool      `json:"remove,omitempty"`
	Kind        AddonKind `json:"addon"`
	Name        string    `json:"name"`
	Source      string    `json:"source,omitempty"`
	Description string    `json:"description,omitempty"`
	Bundle      string    `json:"bundle,omitempty"`
	URL         string    `json:"url,omitempty"`
	Command     string    `json:"command,omitempty"`
	Args        []string  `json:"args,omitempty"`
}

func (AddonGlobalArgs) OpKind() OpKind { return "addon.global" }
func (AddonGlobalArgs) isOpArgs()      {}
