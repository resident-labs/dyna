//go:build linux

package computers

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// statusPIDs reads only this computer's cgroup or its tracked process trees.
// The full environment scan in owned is reserved for fallback cleanup.
func (s *supervisor) statusPIDs() map[int]string {
	for pid, start := range s.apps {
		if p, ok := processInfo(pid); !ok || p.start != start {
			delete(s.apps, pid)
		}
	}
	if s.cgroup != "" {
		if data, err := os.ReadFile(filepath.Join(s.cgroup, "cgroup.procs")); err == nil {
			owned := map[int]string{}
			for _, field := range strings.Fields(string(data)) {
				pid, err := strconv.Atoi(field)
				if err != nil {
					continue
				}
				if p, ok := processInfo(pid); ok {
					owned[pid] = p.start
				}
			}
			return owned
		}
	}
	roots := map[int]string{}
	for pid, start := range s.tracked {
		roots[pid] = start
	}
	for pid, start := range s.apps {
		roots[pid] = start
	}
	for _, process := range []*process{s.compositor, s.stream} {
		if process.alive() {
			if p, ok := processInfo(process.cmd.Process.Pid); ok {
				roots[p.pid] = p.start
			}
		}
	}
	owned := map[int]string{}
	var visit func(int, string)
	visit = func(pid int, start string) {
		if _, seen := owned[pid]; seen {
			return
		}
		p, ok := processInfo(pid)
		if !ok || p.start != start {
			return
		}
		owned[pid] = start
		// A child can be created by any thread, not only the process leader.
		tasks, _ := os.ReadDir(fmt.Sprintf("/proc/%d/task", pid))
		for _, task := range tasks {
			data, _ := os.ReadFile(fmt.Sprintf("/proc/%d/task/%s/children", pid, task.Name()))
			for _, field := range strings.Fields(string(data)) {
				child, err := strconv.Atoi(field)
				if err != nil {
					continue
				}
				if info, ok := processInfo(child); ok {
					visit(child, info.start)
				}
			}
		}
	}
	for pid, start := range roots {
		visit(pid, start)
	}
	s.tracked = owned
	return owned
}
