package jobs

import (
	"fmt"
	"strconv"
	"strings"
)

// A job's default resources. The slot pool hands every job two CPUs and the
// default container memory, so a request that asks for nothing keeps exactly
// the limits a CI check had before it could ask for more.
const (
	// DefaultCPUs is what a job that asks for no CPUs runs with.
	DefaultCPUs = 2
	// DefaultContainerMemoryMiB is the memory a container job runs with.
	DefaultContainerMemoryMiB = int64(4096)
	// DefaultHostMemoryMiB is the memory a host job runs with.
	DefaultHostMemoryMiB = int64(5120)
	// HostTasksMax is the least a job may start, and what every scope that is
	// not a job keeps.
	HostTasksMax = 768
	// TasksPerCPU is how many more tasks a job may start for each CPU it was
	// given. A compiler and its linker threads need many tasks per CPU, so a
	// large build with a fixed bound runs out of them.
	TasksPerCPU = 256
)

// Resources is the CPU count and memory one job runs with, after the node
// bounded the request.
type Resources struct {
	CPUs      int
	MemoryMiB int64
	// Note says what Dyna clamped, and is empty when the job got what it
	// asked for.
	Note string
}

// Facts is the machine capacity a request is clamped against.
type Facts struct {
	Slots       int
	CPUs        int
	MemoryBytes uint64
}

// ClampResources bounds a job's request by the machine. The slot pool gives
// two CPUs and the default container memory per slot, and a probe of the
// machine bounds both again. A request within the bound is granted. A default
// is never clamped, so a small machine keeps the limits its jobs already had.
func ClampResources(host bool, cpus int, memoryMiB int64, facts Facts) Resources {
	resources := Resources{CPUs: DefaultCPUs, MemoryMiB: DefaultContainerMemoryMiB}
	if host {
		resources.MemoryMiB = DefaultHostMemoryMiB
	}
	if cpus > 0 {
		resources.CPUs = cpus
	}
	if memoryMiB > 0 {
		resources.MemoryMiB = memoryMiB
	}
	cpuLimit := facts.Slots * DefaultCPUs
	memoryLimit := int64(facts.Slots) * DefaultContainerMemoryMiB
	if facts.CPUs > 0 {
		cpuLimit = min(cpuLimit, facts.CPUs)
	}
	if facts.MemoryBytes > 0 {
		// A probe total sits a little below the nominal size, so the bound is
		// a whole GiB and reads as one number in a report.
		probed := int64(facts.MemoryBytes / (1 << 20))
		memoryLimit = min(memoryLimit, probed-probed%1024)
	}
	cpuLimit = max(cpuLimit, DefaultCPUs)
	memoryLimit = max(memoryLimit, DefaultContainerMemoryMiB)
	if host {
		memoryLimit = max(memoryLimit, DefaultHostMemoryMiB)
	}
	var clamped []string
	if resources.CPUs > cpuLimit {
		clamped = append(clamped, fmt.Sprintf("cpus from %d to %d", resources.CPUs, cpuLimit))
		resources.CPUs = cpuLimit
	}
	if resources.MemoryMiB > memoryLimit {
		clamped = append(clamped, "memory from "+MemoryLabel(resources.MemoryMiB)+" to "+MemoryLabel(memoryLimit))
		resources.MemoryMiB = memoryLimit
	}
	if len(clamped) > 0 {
		resources.Note = "Dyna clamped " + strings.Join(clamped, " and ")
	}
	return resources
}

// MemoryLabel writes a memory size in MiB or GiB, as a person reads it.
func MemoryLabel(mib int64) string {
	if mib%1024 == 0 {
		return strconv.FormatInt(mib/1024, 10) + " GiB"
	}
	return strconv.FormatInt(mib, 10) + " MiB"
}

// DockerMemory writes a memory size in the unit docker reads.
func DockerMemory(mib int64) string {
	if mib%1024 == 0 {
		return strconv.FormatInt(mib/1024, 10) + "g"
	}
	return strconv.FormatInt(mib, 10) + "m"
}

// SystemdMemory writes a memory size in the unit systemd reads.
func SystemdMemory(mib int64) string {
	if mib%1024 == 0 {
		return strconv.FormatInt(mib/1024, 10) + "G"
	}
	return strconv.FormatInt(mib, 10) + "M"
}

// Limits bound one systemd scope.
type Limits struct {
	CPUs      int
	MemoryMiB int64
	TasksMax  int
}

// DefaultLimits bound a scope that is not a job, such as node setup or
// cleanup.
var DefaultLimits = Limits{CPUs: DefaultCPUs, MemoryMiB: DefaultHostMemoryMiB, TasksMax: HostTasksMax}

// Scope is the systemd scope limit a granted job runs in. A container keeps
// one GiB above its own memory limit for the docker client, as it always had.
// A host job has no container limit, so the scope is its only memory bound.
func (r Resources) Scope(host bool) Limits {
	memory := r.MemoryMiB
	if !host {
		memory += 1024
	}
	return Limits{CPUs: r.CPUs, MemoryMiB: memory, TasksMax: TasksMaxFor(r.CPUs)}
}

// TasksMaxFor bounds the tasks a job with this many CPUs may start: a compiler
// and its linker threads need many tasks per CPU, so the bound follows the
// CPUs the job was given and never drops below the fixed minimum.
func TasksMaxFor(cpus int) int {
	if cpus < 1 {
		cpus = 1
	}
	if tasks := TasksPerCPU * cpus; tasks > HostTasksMax {
		return tasks
	}
	return HostTasksMax
}
