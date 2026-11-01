// Package fakehost provides an in-memory host driver for unit tests.
package fakehost

import (
	"context"
	"sort"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

type Call string

const (
	EnsureSlice     Call = "ensure-slice"
	RemoveSlice     Call = "remove-slice"
	CreateContainer Call = "create-container"
	StartContainer  Call = "start-container"
	StopContainer   Call = "stop-container"
	RemoveContainer Call = "remove-container"
	RemoveVolume    Call = "remove-volume"
	RemovePod       Call = "remove-pod"
	RemoveStack     Call = "remove-stack-volumes"
	ListContainers  Call = "list-containers"
	AddForward      Call = "add-forward"
	RemoveForward   Call = "remove-forward"
	Build           Call = "build"
	PushCache       Call = "push-cache"
)

type Image struct {
	Base       string
	ContextTar []byte
}

type Host struct {
	Calls      []Call
	FailNext   error
	Slices     map[string]box.Slice
	Containers map[contract.BoxID]box.ContainerSpec
	Volumes    map[contract.BoxID]bool
	Running    map[contract.BoxID]bool
	Forwards   map[box.Forward]bool
	Images     map[string]Image
	Caches     map[string][]string
}

func New() *Host {
	return &Host{Slices: map[string]box.Slice{}, Containers: map[contract.BoxID]box.ContainerSpec{}, Volumes: map[contract.BoxID]bool{}, Running: map[contract.BoxID]bool{}, Forwards: map[box.Forward]bool{}, Images: map[string]Image{}, Caches: map[string][]string{}}
}
func (h *Host) call(c Call) error {
	h.Calls = append(h.Calls, c)
	err := h.FailNext
	h.FailNext = nil
	return err
}
func (h *Host) EnsureSlice(_ context.Context, s box.Slice) error {
	if err := h.call(EnsureSlice); err != nil {
		return err
	}
	h.Slices[s.Name] = s
	return nil
}
func (h *Host) RemoveSlice(_ context.Context, name string) error {
	if err := h.call(RemoveSlice); err != nil {
		return err
	}
	delete(h.Slices, name)
	return nil
}
func (h *Host) CreateContainer(_ context.Context, c box.ContainerSpec) error {
	if err := h.call(CreateContainer); err != nil {
		return err
	}
	h.Containers[c.Box] = c
	h.Volumes[c.Box] = true
	return nil
}
func (h *Host) StartContainer(_ context.Context, id contract.BoxID) error {
	if err := h.call(StartContainer); err != nil {
		return err
	}
	h.Running[id] = true
	return nil
}
func (h *Host) StopContainer(_ context.Context, id contract.BoxID) error {
	if err := h.call(StopContainer); err != nil {
		return err
	}
	h.Running[id] = false
	return nil
}
func (h *Host) RemoveContainer(_ context.Context, id contract.BoxID) error {
	if err := h.call(RemoveContainer); err != nil {
		return err
	}
	delete(h.Containers, id)
	delete(h.Running, id)
	return nil
}
func (h *Host) RemoveVolume(_ context.Context, id contract.BoxID) error {
	if err := h.call(RemoveVolume); err != nil {
		return err
	}
	delete(h.Volumes, id)
	return nil
}
func (h *Host) RemovePod(context.Context, contract.BoxID) error { return h.call(RemovePod) }
func (h *Host) RemoveStackVolumes(context.Context, contract.BoxID) error {
	return h.call(RemoveStack)
}
func (h *Host) ListContainers(_ context.Context) ([]box.ContainerStatus, error) {
	if err := h.call(ListContainers); err != nil {
		return nil, err
	}
	out := make([]box.ContainerStatus, 0, len(h.Containers))
	for id := range h.Containers {
		out = append(out, box.ContainerStatus{Box: id, Running: h.Running[id]})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Box < out[j].Box })
	return out, nil
}
func (h *Host) AddForward(_ context.Context, f box.Forward) error {
	if err := h.call(AddForward); err != nil {
		return err
	}
	h.Forwards[f] = true
	return nil
}
func (h *Host) RemoveForward(_ context.Context, f box.Forward) error {
	if err := h.call(RemoveForward); err != nil {
		return err
	}
	delete(h.Forwards, f)
	return nil
}

func (h *Host) Build(_ context.Context, tag, base string, tar []byte) error {
	if err := h.call(Build); err != nil {
		return err
	}
	if _, ok := h.Images[tag]; !ok {
		h.Images[tag] = Image{Base: base, ContextTar: append([]byte(nil), tar...)}
	}
	return nil
}
func (h *Host) PushCache(_ context.Context, mirror, cache string, refs []string) error {
	if err := h.call(PushCache); err != nil {
		return err
	}
	h.Caches[cache] = append([]string(nil), refs...)
	return nil
}

var _ box.HostDriver = (*Host)(nil)
