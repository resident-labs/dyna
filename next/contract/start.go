package contract

import (
	"fmt"
	"time"
)

// StartStep identifies a startup operation independently of its presentation.
type StartStep string

const (
	StartInstructions StartStep = "instructions"
	StartBinary       StartStep = "binary"
	StartBuildCache   StartStep = "build_cache"
	StartShared       StartStep = "shared"
	StartImage        StartStep = "image"
	StartSlice        StartStep = "slice"
	StartForward      StartStep = "forward"
	StartStack        StartStep = "stack"
	StartCreate       StartStep = "container_create"
	StartSidecars     StartStep = "sidecars"
	StartContainer    StartStep = "container_start"
)

type BoxStartStatus struct {
	Host      HostName  `json:"host"`
	Step      StartStep `json:"step"`
	StartedAt time.Time `json:"startedAt"`
}

func (s BoxStartStatus) Label(now time.Time) string {
	label := "Starting on " + string(s.Host)
	elapsed := now.Sub(s.StartedAt)
	if elapsed < time.Minute {
		return label
	}
	return fmt.Sprintf("%s · %s (%d min)", label, s.Step.Label(), int(elapsed/time.Minute))
}

func (s StartStep) Label() string {
	switch s {
	case StartInstructions:
		return "syncing instructions"
	case StartBinary:
		return "updating Dyna on host"
	case StartBuildCache:
		return "starting compiler cache"
	case StartShared:
		return "mounting shared folder"
	case StartImage:
		return "preparing image"
	case StartSlice:
		return "setting resource limits"
	case StartForward:
		return "forwarding socket"
	case StartStack:
		return "creating compose stack"
	case StartCreate:
		return "creating container"
	case StartSidecars:
		return "starting sidecars"
	case StartContainer:
		return "starting container"
	}
	return string(s)
}

// HostHealth is scheduling health, independent of SSH reachability. Empty means no probe yet in this engine.
type HostHealth string

const (
	HostHealthUnknown HostHealth = ""
	HostHealthy       HostHealth = "healthy"
	HostDegraded      HostHealth = "degraded"
)
