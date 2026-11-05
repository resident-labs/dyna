package fakehost

import (
	"context"
	"sort"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/box/podman"
	"github.com/resident-labs/dyna/contract"
)

const (
	CheckServiceHost   Call = "check-service-host"
	EnsureServiceSlice Call = "ensure-service-slice"
	ListServiceFiles   Call = "list-service-files"
	ListeningPorts     Call = "listening-ports"
	PullService        Call = "pull-service"
	ImageSize          Call = "image-size"
	WriteServiceSecret Call = "write-service-secret"
	WriteServiceUnit   Call = "write-service-unit"
	RestartService     Call = "restart-service"
	StopService        Call = "stop-service"
	ShowService        Call = "show-service"
	ServiceHealthy     Call = "service-healthy"
	ServiceLogs        Call = "service-logs"
	RemoveService      Call = "remove-service"
)

// ServiceHost is the service side of a fake host. A service started on it runs until a test sets its state, and any call can be
// made to fail, which stands for the step that failed on a real host.
type ServiceHost struct {
	// Fail holds the error each named call returns while it is set.
	Fail map[Call]error
	// Check is what the service host check finds.
	Check box.ServiceHostCheck
	// Listening are the ports some process holds.
	Listening []contract.PortBinding
	// Files are the quadlet files on the host, by service, as their contents.
	Files map[contract.ServiceID]string
	// Units is what systemd says of each service. A restart of a service with a file sets it running.
	Units map[contract.ServiceID]box.ServiceUnit
	// Secrets are the podman secrets by name, with the values handed over.
	Secrets map[string][]byte
	// Sizes are the images present and their sizes.
	Sizes map[string]int64
	// HealthyAfter makes a health command fail until the service was restarted that many times.
	Health func(id contract.ServiceID, image string) error
	Log    string
	Slice  bool
}

func (h *Host) service(c Call) error {
	h.Calls = append(h.Calls, c)
	return h.Service.Fail[c]
}

func (h *Host) CheckServiceHost(context.Context) (box.ServiceHostCheck, error) {
	return h.Service.Check, h.service(CheckServiceHost)
}

func (h *Host) EnsureServiceSlice(context.Context) error {
	if err := h.service(EnsureServiceSlice); err != nil {
		return err
	}
	h.Service.Slice = true
	return nil
}

func (h *Host) ListServiceFiles(context.Context) ([]box.ServiceFile, error) {
	if err := h.service(ListServiceFiles); err != nil {
		return nil, err
	}
	var out []box.ServiceFile
	for id, file := range h.Service.Files {
		out = append(out, box.ServiceFile{ID: id, SHA: podman.ServiceUnitSHA(file)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (h *Host) ListeningPorts(context.Context) ([]contract.PortBinding, error) {
	return h.Service.Listening, h.service(ListeningPorts)
}

func (h *Host) PullService(_ context.Context, ref string) error {
	if err := h.service(PullService); err != nil {
		return err
	}
	h.Service.Sizes[ref] = 100 << 20
	return nil
}

func (h *Host) ImageSize(_ context.Context, ref string) (int64, error) {
	if err := h.service(ImageSize); err != nil {
		return 0, err
	}
	if h.Service.Sizes[ref] == 0 {
		h.Service.Sizes[ref] = 100 << 20
	}
	return h.Service.Sizes[ref], nil
}

func (h *Host) WriteServiceSecret(_ context.Context, id contract.ServiceID, name string, value []byte) error {
	if err := h.service(WriteServiceSecret); err != nil {
		return err
	}
	h.Service.Secrets[box.ServiceSecretName(id, name)] = append([]byte(nil), value...)
	return nil
}

func (h *Host) WriteServiceUnit(_ context.Context, u box.ServiceUnitSpec) (string, error) {
	if err := h.service(WriteServiceUnit); err != nil {
		return "", err
	}
	file := podman.RenderServiceUnit(u)
	h.Service.Files[u.ID] = file
	return podman.ServiceUnitSHA(file), nil
}

func (h *Host) RestartService(_ context.Context, id contract.ServiceID) error {
	if err := h.service(RestartService); err != nil {
		return err
	}
	unit := h.Service.Units[id]
	unit.ID, unit.State, unit.CrashLoop = id, box.UnitRunning, false
	h.Service.Units[id] = unit
	return nil
}

func (h *Host) StopService(_ context.Context, id contract.ServiceID) error {
	if err := h.service(StopService); err != nil {
		return err
	}
	unit := h.Service.Units[id]
	unit.ID, unit.State = id, box.UnitStopped
	h.Service.Units[id] = unit
	return nil
}

func (h *Host) ShowService(_ context.Context, id contract.ServiceID) (box.ServiceUnit, error) {
	if err := h.service(ShowService); err != nil {
		return box.ServiceUnit{}, err
	}
	unit, ok := h.Service.Units[id]
	if !ok {
		return box.ServiceUnit{ID: id, State: box.UnitAbsent}, nil
	}
	return unit, nil
}

func (h *Host) ServiceHealthy(_ context.Context, id contract.ServiceID, _ []string) error {
	if err := h.service(ServiceHealthy); err != nil {
		return err
	}
	if h.Service.Health != nil {
		return h.Service.Health(id, h.Service.Files[id])
	}
	return nil
}

func (h *Host) ServiceLogs(context.Context, contract.ServiceID, int, time.Duration) (string, error) {
	return h.Service.Log, h.service(ServiceLogs)
}

func (h *Host) RemoveService(_ context.Context, id contract.ServiceID) error {
	if err := h.service(RemoveService); err != nil {
		return err
	}
	delete(h.Service.Files, id)
	delete(h.Service.Units, id)
	for name := range h.Service.Secrets {
		if len(name) > len(box.ServiceName(id)) && name[:len(box.ServiceName(id))+1] == box.ServiceName(id)+"-" {
			delete(h.Service.Secrets, name)
		}
	}
	return nil
}

var _ box.ServiceDriver = (*Host)(nil)
