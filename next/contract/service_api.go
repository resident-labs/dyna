package contract

import (
	"encoding/json"
	"time"
)

// The service methods are the owner's in this part (spec 5.18); service.send is open to sessions listed in a feed's senders.
const (
	MethodServiceSet       Method = "service.set"
	MethodServiceList      Method = "service.list"
	MethodServiceShow      Method = "service.show"
	MethodServiceLogs      Method = "service.logs"
	MethodServiceRestart   Method = "service.restart"
	MethodServiceStop      Method = "service.stop"
	MethodServiceStart     Method = "service.start"
	MethodServiceUpdate    Method = "service.update"
	MethodServiceRollback  Method = "service.rollback"
	MethodServiceRemove    Method = "service.remove"
	MethodServiceSecretSet Method = "service.secret_set"
	MethodServiceSend      Method = "service.send"
)

// Refusals of service.set and feed socket endpoints that name their cause.
const (
	ErrPortTaken      ErrorCode = "port_taken"      // another service on the host publishes that host port and protocol
	ErrPortPrivileged ErrorCode = "port_privileged" // the host port is below the host's ip_unprivileged_port_start
	ErrRateLimited    ErrorCode = "rate_limited"    // feed service exceeded its inbound message rate limit
	ErrTooLarge       ErrorCode = "too_large"       // feed message exceeded its maximum size
)

// ServiceLogsMax is the most lines service.logs gives, and ServiceLogsDefault what it gives when asked for none.
const (
	ServiceLogsMax     = 2000
	ServiceLogsDefault = 200
)

// ServiceSetRequest declares a service, or changes the declaration of one with that name. Changing the image source, ports,
// resources, host or secrets of a placed service is allowed here; Host cannot change once the service is placed.
type ServiceSetRequest struct {
	Room RoomID      `json:"room"`
	Spec ServiceSpec `json:"spec"`
}
type ServiceSetResponse struct {
	Service Service `json:"service"`
	Note    string  `json:"note,omitempty"`
}

type ServiceListRequest struct {
	Room RoomID `json:"room,omitempty"` // empty lists every room's services
}
type ServiceListResponse struct {
	Services []Service      `json:"services"`
	Discord  []DiscordRoute `json:"discord,omitempty"`
}

// ServiceRef names a service in a room.
type ServiceRef struct {
	Room RoomID      `json:"room"`
	Name ServiceName `json:"name"`
}

type ServiceShowRequest = ServiceRef
type ServiceShowResponse struct {
	Service Service `json:"service"`
}

type ServiceLogsRequest struct {
	Room  RoomID      `json:"room"`
	Name  ServiceName `json:"name"`
	Lines int         `json:"lines,omitempty"` // 1 to ServiceLogsMax; 0 is ServiceLogsDefault
	Since Duration    `json:"since,omitempty"` // only lines newer than this long ago
}
type ServiceLogsResponse struct {
	Text string `json:"text"`
}

// ServiceActionResponse is the service after a restart, stop, start, update, rollback or remove.
type ServiceActionResponse struct {
	Service Service `json:"service"`
}
type (
	ServiceRestartRequest  = ServiceRef
	ServiceStopRequest     = ServiceRef
	ServiceStartRequest    = ServiceRef
	ServiceUpdateRequest   = ServiceRef
	ServiceRollbackRequest = ServiceRef
	ServiceRemoveRequest   = ServiceRef
)

// ServiceSecretSetRequest writes a declared secret's value. Nothing reads one back.
type ServiceSecretSetRequest struct {
	Room   RoomID      `json:"room"`
	Name   ServiceName `json:"name"`
	Secret string      `json:"secret"`
	Value  string      `json:"value"`
}
type ServiceSecretSetResponse struct{}

// ServiceSendRequest queues an outbound message for a feed service to poll and deliver externally.
type ServiceSendRequest struct {
	Room       RoomID      `json:"room,omitempty" desc:"Room ID; omitted means your room."`
	Service    ServiceName `json:"service" desc:"Feed service name."`
	To         string      `json:"to" desc:"Destination address understood by the service."`
	Text       string      `json:"text" desc:"Message text to send."`
	ToolCallID string      `json:"toolCallId,omitempty"`
}
type ServiceSendResponse struct {
	ID string `json:"id"`
}

// FeedMessageRequest is posted by a feed service on its engine socket (POST /v1/messages).
type FeedMessageRequest struct {
	ID     string          `json:"id"`
	Author string          `json:"author"`
	Text   string          `json:"text"`
	Data   json.RawMessage `json:"data,omitempty"`
}

// FeedMessageResponse answers POST /v1/messages on a feed service's socket.
type FeedMessageResponse struct {
	OK        bool `json:"ok"`
	Duplicate bool `json:"duplicate,omitempty"`
}

// FeedOutboxMessage is one queued outbound message returned by GET /v1/outbox on a feed service's socket.
type FeedOutboxMessage struct {
	ID   string    `json:"id"`
	To   string    `json:"to"`
	Text string    `json:"text"`
	At   time.Time `json:"at"`
}

// FeedOutboxResponse answers GET /v1/outbox on a feed service's socket.
type FeedOutboxResponse struct {
	Messages []FeedOutboxMessage `json:"messages"`
}

// ServiceChanged says a service's state or reason changed, or that it was declared or removed. From is empty for a new one and
// To is removing for the last.
type ServiceChanged struct {
	Service ServiceID     `json:"service"`
	Name    ServiceName   `json:"name"`
	Host    HostName      `json:"host,omitempty"`
	From    ServiceState  `json:"from,omitempty"`
	To      ServiceState  `json:"to"`
	Reason  ServiceReason `json:"reason,omitempty"`
	Cause   ServiceReason `json:"cause,omitempty"`
}

// ServiceFailure says a service entered crash_loop, rolled_back or failed, once for each time it did.
type ServiceFailure struct {
	Service ServiceID     `json:"service"`
	Name    ServiceName   `json:"name"`
	Host    HostName      `json:"host,omitempty"`
	Reason  ServiceReason `json:"reason"`
	Cause   ServiceReason `json:"cause,omitempty"`
	Detail  string        `json:"detail,omitempty"`
}

// ServiceMessage is an outside message posted by a feed service on its engine socket. It wakes nobody by itself;
// ordinary subscriptions route it to sessions or workflow services.
type ServiceMessage struct {
	Service ServiceName     `json:"service"`
	ID      string          `json:"id"`
	Author  string          `json:"author"`
	Text    string          `json:"text"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// ServiceSent records an outbound message queued for a feed service via service.send.
type ServiceSent struct {
	Service    ServiceName `json:"service"`
	ID         string      `json:"id"`
	To         string      `json:"to"`
	Text       string      `json:"text"`
	ToolCallID string      `json:"toolCallId,omitempty"`
}

func (ServiceChanged) EventType() EventType { return "service.changed" }
func (ServiceChanged) isEventBody()         {}

func (ServiceFailure) EventType() EventType { return "service.failed" }
func (ServiceFailure) isEventBody()         {}

func (ServiceMessage) EventType() EventType { return "service.message" }
func (ServiceMessage) isEventBody()         {}

func (ServiceSent) EventType() EventType { return "service.sent" }
func (ServiceSent) isEventBody()         {}

type (
	ServiceRestartResponse  = ServiceActionResponse
	ServiceStopResponse     = ServiceActionResponse
	ServiceStartResponse    = ServiceActionResponse
	ServiceUpdateResponse   = ServiceActionResponse
	ServiceRollbackResponse = ServiceActionResponse
	ServiceRemoveResponse   = ServiceActionResponse
)
