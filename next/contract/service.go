package contract

import (
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

type (
	ServiceID      string // "svc_<ulid>"
	ServiceName    string // 1 to 40 characters: lowercase letters, digits and "-", starting with a letter
	ServicePort    string // "PORT", "PORT/udp" or "HOSTPORT:PORT/tcp"
	ServiceSize    string // a size such as "512MiB": a whole number and B, KiB, MiB, GiB or TiB
	ServiceWant    string
	ServiceState   string
	ServiceReason  string
	ServiceRestart string
)

const (
	ServiceWantRun    ServiceWant = "run"
	ServiceWantStop   ServiceWant = "stop"
	ServiceWantRemove ServiceWant = "remove"
)

const (
	ServicePending     ServiceState = "pending"     // not placed yet, or held; the reason says why
	ServiceDeploying   ServiceState = "deploying"   // the reason names the phase
	ServiceRunning     ServiceState = "running"     // with reason rolled_back, after an update that failed
	ServiceCrashing    ServiceState = "crashing"    // systemd is restarting it or gave up
	ServiceStopped     ServiceState = "stopped"     // want is stop
	ServiceFailed      ServiceState = "failed"      // a deploy that could not finish, with no version running
	ServiceUnreachable ServiceState = "unreachable" // its host is; LastState keeps what it was
	ServiceRemoving    ServiceState = "removing"
)

// The reasons are set by the step that failed. The first four are the deploy phases of a deploying service.
const (
	ServicePhaseImage       ServiceReason  = "image"
	ServicePhaseUnit        ServiceReason  = "unit"
	ServicePhaseStart       ServiceReason  = "start"
	ServicePhaseHealth      ServiceReason  = "health"
	ServiceNoCPU            ServiceReason  = "cpu"
	ServiceNoMemory         ServiceReason  = "memory"
	ServiceNoDisk           ServiceReason  = "disk"
	ServiceRoomHost         ServiceReason  = "room-host"
	ServiceRoomLimit        ServiceReason  = "room-limit"
	ServiceHostCheck        ServiceReason  = "service_host"
	ServicePortTaken        ServiceReason  = "port_taken"
	ServicePortInUse        ServiceReason  = "port_in_use"
	ServicePortPrivileged   ServiceReason  = "port_privileged"
	ServiceSecretMissing    ServiceReason  = "secret_missing"
	ServiceImageBuild       ServiceReason  = "image_build"
	ServiceImagePull        ServiceReason  = "image_pull"
	ServiceUnitFailed       ServiceReason  = "unit_failed"
	ServiceStartFailed      ServiceReason  = "start_failed"
	ServiceHealthFailed     ServiceReason  = "health_failed"
	ServiceCrashLoop        ServiceReason  = "crash_loop"
	ServiceRolledBack       ServiceReason  = "rolled_back"
	ServiceHostUnreachable  ServiceReason  = "host_unreachable"
	ServiceHostRemoved      ServiceReason  = "host_removed"
	ServiceRestartAlways    ServiceRestart = "always"
	ServiceRestartOnFailure ServiceRestart = "on-failure"
)

// Limits of a declaration (spec 5.18).
const (
	ServiceMaxCPUMilli  = 8000
	ServiceMinCPUMilli  = 250
	ServiceCPUStepMilli = 250
	ServiceMinMemory    = 64 << 20
	ServiceMaxMemory    = 16 << 30
	ServiceMaxDisk      = 100 << 30
	ServiceDefaultCPUs  = 1.0
	ServiceDefaultMem   = ServiceSize("512MiB")
	ServiceDefaultDisk  = ServiceSize("1GiB")
	// ServiceImageEstimate is the size admission assumes for an image no host has measured yet.
	ServiceImageEstimate = 1 << 30
)

// ServiceSpec is the declaration: one shape for every kind of service. Only Name and Image are required, and Parse fills the
// rest with their defaults.
type ServiceSpec struct {
	Name    ServiceName       `json:"name"`
	Image   string            `json:"image" desc:"A room image name, or a registry reference pinned by digest (ghcr.io/o/r@sha256:...). A tag with no digest is refused."`
	Command []string          `json:"command,omitempty" desc:"Replaces the image's CMD."`
	Ports   []ServicePort     `json:"ports,omitempty" desc:"PORT, PORT/udp or HOSTPORT:PORT/tcp, each published on every address of the host. tcp unless named."`
	Env     map[string]string `json:"env,omitempty" desc:"Plain environment values. A name does not start with DYNA_."`
	Secrets []string          `json:"secrets,omitempty" desc:"Names of environment variables whose values the owner sets apart. A secret with no value holds the deploy."`
	Data    string            `json:"data,omitempty" desc:"Absolute path of a named volume that survives restarts and updates and is the only writable place besides /tmp."`
	CPUs    float64           `json:"cpus,omitempty" desc:"Hard limit and reservation, 0.25 to 8 in steps of 0.25. Default 1."`
	Memory  ServiceSize       `json:"memory,omitempty" desc:"Hard limit and reservation, 64MiB to 16GiB. Default 512MiB."`
	Disk    ServiceSize       `json:"disk,omitempty" desc:"Accounting for the data volume and logs, up to 100GiB. Default 1GiB."`
	Host    HostName          `json:"host,omitempty" desc:"A host in the room's rule. Without it admission picks one. Once placed the service stays."`
	Restart ServiceRestart    `json:"restart,omitempty" desc:"always (default) or on-failure."`
	Health  []string          `json:"health,omitempty" desc:"A command run in the container to gate an update. Without it, ten seconds running counts as healthy."`
}

// PortProto is the protocol of a published port.
type PortProto string

const (
	PortTCP PortProto = "tcp"
	PortUDP PortProto = "udp"
)

// PortBinding is a parsed ServicePort: a host port published for a container port.
type PortBinding struct {
	Host, Container int
	Proto           PortProto
}

// ServiceParts is a declaration validated once, with its defaults filled in and its text values parsed. Everything inside the
// engine works from it.
type ServiceParts struct {
	Spec                          ServiceSpec // canonical: the defaults filled in, ports as "HOST:PORT/proto"
	Ports                         []PortBinding
	CPUMilli, MemBytes, DiskBytes int64
	// Digest is the reference of a registry image, empty for a room image.
	Digest string
}

var (
	serviceNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}$`)
	roomImagePattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]*$`)
	digestPattern      = regexp.MustCompile(`^[a-z0-9]+([.-][a-z0-9]+)*(:[0-9]+)?(/[a-z0-9]+([._-][a-z0-9]+)*)+@sha256:[0-9a-f]{64}$`)
	envNamePattern     = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	sizePattern        = regexp.MustCompile(`^([0-9]+)(B|KiB|MiB|GiB|TiB)$`)
)

// ServiceCIRunnerImage is the room image name no service may use.
const ServiceCIRunnerImage = "ci-runner"

// Parse validates a declaration at the boundary that owns it and returns its typed parts. The messages name the field.
func (s ServiceSpec) Parse() (ServiceParts, error) {
	var p ServiceParts
	if !serviceNamePattern.MatchString(string(s.Name)) {
		return p, fmt.Errorf("name must be 1 to 40 characters: lowercase letters, digits and -, starting with a letter")
	}
	switch {
	case digestPattern.MatchString(s.Image):
		p.Digest = s.Image
	case roomImagePattern.MatchString(s.Image) && s.Image != ServiceCIRunnerImage:
	default:
		return p, fmt.Errorf("image must be a room image or name a digest")
	}
	for _, list := range [][]string{s.Command, s.Health} {
		for _, arg := range list {
			if hasControl(arg) {
				return p, fmt.Errorf("a command argument holds a control character")
			}
		}
	}
	if len(s.Health) > 0 && s.Health[0] == "" {
		return p, fmt.Errorf("health must start with a command")
	}
	if s.Data != "" && (!strings.HasPrefix(s.Data, "/") || s.Data == "/" || hasControl(s.Data) || strings.ContainsAny(s.Data, ":,\"% \\$=")) {
		return p, fmt.Errorf("data must be an absolute path with no space, colon, comma, quote or %%")
	}
	taken := map[string]bool{}
	for name, value := range s.Env {
		if !envNamePattern.MatchString(name) || strings.HasPrefix(name, "DYNA_") || hasControl(value) {
			return p, fmt.Errorf("env %q: a name is letters, digits and _, not starting with DYNA_, and a value holds no control character", name)
		}
		taken[name] = true
	}
	for _, name := range s.Secrets {
		if !envNamePattern.MatchString(name) || strings.HasPrefix(name, "DYNA_") || taken[name] {
			return p, fmt.Errorf("secret %q: a name is letters, digits and _, not starting with DYNA_, and not also in env or listed twice", name)
		}
		taken[name] = true
	}
	switch s.Restart {
	case "":
		s.Restart = ServiceRestartAlways
	case ServiceRestartAlways, ServiceRestartOnFailure:
	default:
		return p, fmt.Errorf("restart is always or on-failure")
	}
	cpus := s.CPUs
	if cpus == 0 {
		cpus = ServiceDefaultCPUs
	}
	p.CPUMilli = int64(math.Round(cpus * 1000))
	if math.Abs(cpus*1000-float64(p.CPUMilli)) > 1e-6 || p.CPUMilli < ServiceMinCPUMilli || p.CPUMilli > ServiceMaxCPUMilli || p.CPUMilli%ServiceCPUStepMilli != 0 {
		return p, fmt.Errorf("cpus is 0.25 to 8, in steps of 0.25")
	}
	var err error
	if s.Memory == "" {
		s.Memory = ServiceDefaultMem
	}
	if s.Disk == "" {
		s.Disk = ServiceDefaultDisk
	}
	if p.MemBytes, err = s.Memory.Bytes(); err != nil || p.MemBytes < ServiceMinMemory || p.MemBytes > ServiceMaxMemory {
		return p, fmt.Errorf("memory is 64MiB to 16GiB, a whole number with B, KiB, MiB, GiB or TiB")
	}
	if p.DiskBytes, err = s.Disk.Bytes(); err != nil || p.DiskBytes > ServiceMaxDisk {
		return p, fmt.Errorf("disk is up to 100GiB, a whole number with B, KiB, MiB, GiB or TiB")
	}
	s.CPUs = float64(p.CPUMilli) / 1000
	seen := map[string]bool{}
	s.Ports = slices.Clone(s.Ports)
	for i, port := range s.Ports {
		binding, err := port.Parse()
		if err != nil {
			return p, err
		}
		key := fmt.Sprintf("%d/%s", binding.Host, binding.Proto)
		if seen[key] {
			return p, fmt.Errorf("port %s is listed twice", key)
		}
		seen[key] = true
		p.Ports = append(p.Ports, binding)
		s.Ports[i] = ServicePort(fmt.Sprintf("%d:%d/%s", binding.Host, binding.Container, binding.Proto))
	}
	p.Spec = s
	return p, nil
}

func hasControl(s string) bool {
	return strings.IndexFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0
}

// Parse reads a port in any of its three forms.
func (p ServicePort) Parse() (PortBinding, error) {
	text, proto, hasProto := strings.Cut(string(p), "/")
	b := PortBinding{Proto: PortTCP}
	if hasProto {
		if proto != string(PortTCP) && proto != string(PortUDP) {
			return b, fmt.Errorf("port %q: the protocol is tcp or udp", p)
		}
		b.Proto = PortProto(proto)
	}
	host, container, mapped := strings.Cut(text, ":")
	if !mapped {
		host, container = text, text
	}
	var err error
	if b.Host, err = strconv.Atoi(host); err == nil {
		b.Container, err = strconv.Atoi(container)
	}
	if err != nil || b.Host < 1 || b.Host > 65535 || b.Container < 1 || b.Container > 65535 || host != strconv.Itoa(b.Host) || container != strconv.Itoa(b.Container) {
		return b, fmt.Errorf("port %q is PORT, PORT/udp or HOSTPORT:PORT/tcp, from 1 to 65535", p)
	}
	return b, nil
}

// Bytes reads a size.
func (s ServiceSize) Bytes() (int64, error) {
	m := sizePattern.FindStringSubmatch(string(s))
	if m == nil {
		return 0, fmt.Errorf("size %q", s)
	}
	n, err := strconv.ParseInt(m[1], 10, 64)
	unit := map[string]int64{"B": 1, "KiB": 1 << 10, "MiB": 1 << 20, "GiB": 1 << 30, "TiB": 1 << 40}[m[2]]
	if err != nil || n > math.MaxInt64/unit {
		return 0, fmt.Errorf("size %q", s)
	}
	return n * unit, nil
}

// Source is what an update may not change: the room image's name, or a registry image's repository without its digest.
func (p ServiceParts) Source() string {
	repository, _, _ := strings.Cut(p.Spec.Image, "@")
	return repository
}

// Pids is the process limit of the container: 512 for each CPU, at least 256.
func (p ServiceParts) Pids() int { return int(max(256, p.CPUMilli*512/1000)) }

// SamePorts says whether two port lists publish the same host ports and protocols.
func SamePorts(a, b []PortBinding) bool {
	key := func(x, y PortBinding) int {
		return strings.Compare(fmt.Sprintf("%d/%s", x.Host, x.Proto), fmt.Sprintf("%d/%s", y.Host, y.Proto))
	}
	a, b = slices.Clone(a), slices.Clone(b)
	slices.SortFunc(a, key)
	slices.SortFunc(b, key)
	return slices.EqualFunc(a, b, func(x, y PortBinding) bool { return key(x, y) == 0 })
}

// ServiceSecretStatus says whether a declared secret has a value. The value is never given back.
type ServiceSecretStatus struct {
	Name string `json:"name"`
	Set  bool   `json:"set"`
}

// Service is a declaration with what the engine observed of it.
type Service struct {
	ID         ServiceID             `json:"id"`
	Room       RoomID                `json:"room"`
	Spec       ServiceSpec           `json:"spec"`
	Generation int                   `json:"generation"`
	Host       HostName              `json:"host,omitempty"`
	Want       ServiceWant           `json:"want"`
	State      ServiceState          `json:"state"`
	Reason     ServiceReason         `json:"reason,omitempty"`
	Cause      ServiceReason         `json:"cause,omitempty"`     // why an update rolled back
	Detail     string                `json:"detail,omitempty"`    // the tail of a failed build or pull
	LastState  ServiceState          `json:"lastState,omitempty"` // what an unreachable service was
	Deployed   int                   `json:"deployed"`            // the generation that runs
	ImageRef   string                `json:"imageRef,omitempty"`
	Previous   string                `json:"previousRef,omitempty"`
	CPUMilli   int64                 `json:"cpuMilli"`
	MemBytes   int64                 `json:"memBytes"`
	DiskBytes  int64                 `json:"diskBytes"`
	Restarts   int                   `json:"restarts"`
	Since      time.Time             `json:"since,omitzero"`
	Secrets    []ServiceSecretStatus `json:"secrets,omitempty"`
}

// HostService is a service as a host's summary lists it.
type HostService struct {
	Room     RoomID       `json:"room"`
	Name     ServiceName  `json:"name"`
	State    ServiceState `json:"state"`
	CPUMilli int64        `json:"cpuMilli"`
	MemBytes int64        `json:"memBytes"`
}

// ServiceHostIssue names what a service host check found missing.
type ServiceHostIssue string

const (
	ServiceHostNoLinger  ServiceHostIssue = "no_linger"
	ServiceHostNoQuadlet ServiceHostIssue = "no_quadlet"
	ServiceHostNoPasta   ServiceHostIssue = "no_pasta"
)

// ServiceHostStatus is the latest service host check of a host.
type ServiceHostStatus struct {
	Issues []ServiceHostIssue `json:"issues,omitempty"`
	// LowestPort is the host's net.ipv4.ip_unprivileged_port_start: a service publishes no host port below it.
	LowestPort int `json:"lowestPort"`
}
