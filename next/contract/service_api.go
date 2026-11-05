package contract

// The service methods are the owner's in this part (spec 5.18): a session's request, and the agent tool, are 7.9.
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
)

// Refusals of service.set that name their cause.
const (
	ErrPortTaken      ErrorCode = "port_taken"      // another service on the host publishes that host port and protocol
	ErrPortPrivileged ErrorCode = "port_privileged" // the host port is below the host's ip_unprivileged_port_start
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
}

type ServiceListRequest struct {
	Room RoomID `json:"room,omitempty"` // empty lists every room's services
}
type ServiceListResponse struct {
	Services []Service `json:"services"`
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

func (ServiceChanged) EventType() EventType { return "service.changed" }
func (ServiceChanged) isEventBody()         {}

func (ServiceFailure) EventType() EventType { return "service.failed" }
func (ServiceFailure) isEventBody()         {}

type (
	ServiceRestartResponse  = ServiceActionResponse
	ServiceStopResponse     = ServiceActionResponse
	ServiceStartResponse    = ServiceActionResponse
	ServiceUpdateResponse   = ServiceActionResponse
	ServiceRollbackResponse = ServiceActionResponse
	ServiceRemoveResponse   = ServiceActionResponse
)
