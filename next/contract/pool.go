package contract

const (
	MethodPoolAdd    Method = "pool.add"
	MethodPoolList   Method = "pool.list"
	MethodPoolRemove Method = "pool.remove"
	MethodRoomHosts  Method = "room.hosts"
	MethodHostRemove Method = "host.remove"
)

// PoolAddRequest adds a node pool: a script that gets machines, and the most it may hold. Without PerSession, sessions share
// its nodes, and an empty one goes after Idle, which zero makes 30 minutes. A session's request waits for the owner, so this
// is also the operation's arguments, and the card shows the script.
type PoolAddRequest struct {
	Name       string     `json:"name"`
	Script     string     `json:"script"`
	Max        int        `json:"max"`
	PerSession bool       `json:"perSession,omitempty"`
	Idle       DurationMS `json:"idle,omitempty"`
}

func (PoolAddRequest) OpKind() OpKind { return "pool.add" }
func (PoolAddRequest) isOpArgs()      {}

// PoolAddResponse names the operation when the change waits for the owner.
type PoolAddResponse struct {
	Operation OpID `json:"operation,omitempty"`
}

// Pool describes a pool. Its script stays on the engine host. Problem is its last failure, until it recovers.
type Pool struct {
	Name       string     `json:"name"`
	Max        int        `json:"max"`
	PerSession bool       `json:"perSession,omitempty"`
	Idle       DurationMS `json:"idle,omitempty"`
	Nodes      int        `json:"nodes"` // every node, counting those being created
	Problem    string     `json:"problem,omitempty"`
}

type PoolListRequest struct{}
type PoolListResponse struct {
	Pools []Pool `json:"pools"`
}

// PoolRemoveRequest removes an empty pool that no room names.
type PoolRemoveRequest struct {
	Name string `json:"name"`
}
type PoolRemoveResponse struct{}

// RoomHostsRequest replaces the hosts a room's new boxes may use: host names and at most one pool. No names clears the rule,
// so they may use any host again. A box already placed stays where it is. A session's request waits for the owner, so this
// is also the operation's arguments.
type RoomHostsRequest struct {
	Room RoomID   `json:"room"`
	Only []string `json:"only,omitempty"`
}

func (RoomHostsRequest) OpKind() OpKind { return "room.hosts" }
func (RoomHostsRequest) isOpArgs()      {}

// OperationRoom ties the operation to the room it changes.
func (r RoomHostsRequest) OperationRoom() RoomID { return r.Room }

// RoomHostsResponse names the operation when the change waits for the owner.
type RoomHostsResponse struct {
	Operation OpID `json:"operation,omitempty"`
}

// HostRemoveRequest removes a host with nothing on it. A pool's node goes back through its script. DiscardBoxes removes it
// with its boxes, as a host lost for good: each becomes failed with cause host_lost, its volume goes, and work not pushed is
// lost. Each session can then be replaced with session.restart, and its parent hears of the failure once.
type HostRemoveRequest struct {
	Host         HostName `json:"host"`
	DiscardBoxes bool     `json:"discardBoxes,omitempty"`
}

// HostRemoveResponse lists the sessions whose boxes DiscardBoxes failed.
type HostRemoveResponse struct {
	Failed []SessionID `json:"failed,omitempty"`
}
