package box

import (
	"fmt"
	"time"

	"github.com/resident-labs/dyna/contract"
)

// Stack is what a compose file adds to a box: its sidecars and what the box itself takes from the file. Every name and
// value in it has passed the compose refuse list.
type Stack struct {
	// Hash changes when anything here changes, so a start can tell that the stack on its host is stale.
	Hash     string
	Env      []EnvVar // for the box
	Volumes  []Volume // for the box
	Sidecars []Sidecar
	// Names are every service's name, which reach the shared loopback through the pod's hosts file.
	Names []string
	// Infra is the image that holds the pod's network namespace. It needs tini.
	Infra string
}

// Sidecar is one compose service beside the box.
type Sidecar struct {
	Name, Image string
	Command     []string
	Env         []EnvVar
	Volumes     []Volume
	// Health is a compose healthcheck test, empty for none. Wait bounds how long a start waits for it to pass.
	Health        []string
	HealthTimeout time.Duration
	Wait          time.Duration
	CPUs          float64 // 0 leaves only the box's own limit, as do MemBytes and Pids.
	MemBytes      int64
	Pids          int
}

// Volume is a named compose volume, private to one box and removed with it.
type Volume struct {
	Name, Target string
	ReadOnly     bool
}

// VolumeNames lists the stack's volumes, each once.
func (s *Stack) VolumeNames() []string {
	seen := make(map[string]bool)
	var names []string
	add := func(volumes []Volume) {
		for _, v := range volumes {
			if !seen[v.Name] {
				seen[v.Name] = true
				names = append(names, v.Name)
			}
		}
	}
	add(s.Volumes)
	for _, sidecar := range s.Sidecars {
		add(sidecar.Volumes)
	}
	return names
}

// Tags lists the images a host must keep for this image.
func (i Image) Tags() []string {
	tags := []string{i.Tag}
	if i.Stack != nil {
		for _, sidecar := range i.Stack.Sidecars {
			tags = append(tags, sidecar.Image)
		}
	}
	return tags
}

// StackHash is the Hash of the stack, empty for a box with none.
func (i Image) StackHash() string {
	if i.Stack == nil {
		return ""
	}
	return i.Stack.Hash
}

// PodName names a box's pod.
func PodName(id contract.BoxID) string { return "dyna-pod-" + string(id) }

// SidecarName names one sidecar container of a box.
func SidecarName(id contract.BoxID, service string) string {
	return "dyna-sidecar-" + string(id) + "-" + service
}

// StackError is a stack that cannot run, in words for the owner. It holds no output of the sidecar's own.
type StackError struct{ Reason string }

func (e StackError) Error() string { return fmt.Sprintf("compose: %s", e.Reason) }
