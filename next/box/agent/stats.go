package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/resident-labs/dyna/contract"
)

const (
	boxVolume = "/box"
	// A du over a large volume can take minutes, so one runs off the channel, at low priority, at most this often.
	diskEvery = 5 * time.Minute
	diskLimit = 10 * time.Minute
	// A CPU figure needs two samples. A reading with no recent sample takes this long to get a second one.
	cpuWindow = 250 * time.Millisecond
	cpuStale  = 30 * time.Second
	cpuTooNew = 100 * time.Millisecond
)

// boxStats answers box.stats from the box's own cgroup and a cached du. It does nothing until asked.
type boxStats struct {
	root  string // the cgroup, whose counters take in the agent and computer children
	du    func(context.Context) (int64, error)
	now   func() time.Time
	after func(time.Duration) <-chan time.Time

	mu        sync.Mutex
	cpuAt     time.Time
	cpuTotal  int64
	cores     float64
	diskAt    time.Time
	diskUsed  int64
	measuring bool
}

func newBoxStats() *boxStats {
	return &boxStats{root: "/sys/fs/cgroup", du: measureDisk, now: time.Now, after: time.After}
}

func (s *boxStats) read(ctx context.Context) (contract.BoxReading, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cores, err := s.cpu(ctx)
	if err != nil {
		return contract.BoxReading{}, err
	}
	memory, err := s.memory()
	if err != nil {
		return contract.BoxReading{}, err
	}
	return contract.BoxReading{CPUUsed: cores, MemUsed: memory, DiskUsed: s.disk(ctx)}, nil
}

func (s *boxStats) cpu(ctx context.Context) (float64, error) {
	now, total, err := s.cpuSample()
	if err != nil {
		return 0, err
	}
	if s.cpuAt.IsZero() || now.Sub(s.cpuAt) > cpuStale {
		s.cpuAt, s.cpuTotal = now, total
		select {
		case <-s.after(cpuWindow):
		case <-ctx.Done():
			return 0, ctx.Err()
		}
		if now, total, err = s.cpuSample(); err != nil {
			return 0, err
		}
	}
	if elapsed := now.Sub(s.cpuAt); elapsed >= cpuTooNew { // two viewers asking at once share a figure
		s.cores = float64(total-s.cpuTotal) / float64(elapsed.Microseconds())
		s.cpuAt, s.cpuTotal = now, total
	}
	return s.cores, nil
}

func (s *boxStats) cpuSample() (time.Time, int64, error) {
	total, err := statValue(filepath.Join(s.root, "cpu.stat"), "usage_usec")
	return s.now(), total, err
}

// memory leaves out the page cache the kernel can drop at once, as podman stats does. Otherwise a box that has read a
// large tree looks full.
func (s *boxStats) memory() (int64, error) {
	data, err := os.ReadFile(filepath.Join(s.root, "memory.current"))
	if err != nil {
		return 0, err
	}
	current, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return 0, err
	}
	reclaimable, err := statValue(filepath.Join(s.root, "memory.stat"), "inactive_file")
	if err != nil {
		return 0, err
	}
	return max(0, current-reclaimable), nil
}

// disk returns the last measure and starts another if that one is old. It never waits for du. A du that fails waits its
// turn too, so a box without one does not spawn it on every reading.
func (s *boxStats) disk(ctx context.Context) int64 {
	if !s.measuring && (s.diskAt.IsZero() || s.now().Sub(s.diskAt) >= diskEvery) {
		s.measuring = true
		go func() {
			used, err := s.du(ctx)
			s.mu.Lock()
			defer s.mu.Unlock()
			s.measuring, s.diskAt = false, s.now()
			if err != nil {
				log.Printf("box stats: disk: %v", err)
				return
			}
			s.diskUsed = used
		}()
	}
	return s.diskUsed
}

// measureDisk counts what lives in the box's own volume. /box/cache and /box/shared are mounts from the same disk, so
// du's -x would walk into them, and they are excluded by name.
func measureDisk(ctx context.Context) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, diskLimit)
	defer cancel()
	args := []string{"-n", "19", "du", "-skx"}
	for _, mount := range mountsUnder("/proc/self/mountinfo", boxVolume) {
		args = append(args, "--exclude="+mount)
	}
	out, err := exec.CommandContext(ctx, "nice", append(args, boxVolume)...).Output()
	return duBytes(string(out), err)
}

// duBytes reads du's total. du exits 1 after any folder it could not read, and still prints the total of the rest.
func duBytes(out string, err error) (int64, error) {
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		return 0, err
	}
	fields := strings.Fields(out)
	if len(fields) == 0 {
		return 0, fmt.Errorf("du wrote no total: %v", err)
	}
	kib, parseErr := strconv.ParseInt(fields[0], 10, 64)
	if parseErr != nil {
		return 0, parseErr
	}
	return kib * 1024, nil
}

// mountsUnder lists the mount points inside folder, from a mountinfo file. A file that cannot be read gives none.
func mountsUnder(path, folder string) []string {
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer file.Close()
	var mounts []string
	for lines := bufio.NewScanner(file); lines.Scan(); {
		if fields := strings.Fields(lines.Text()); len(fields) > 4 && strings.HasPrefix(fields[4], folder+"/") {
			mounts = append(mounts, fields[4])
		}
	}
	return mounts
}

// statValue reads one number from a cgroup file of "name value" lines.
func statValue(path, name string) (int64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	for line := range strings.Lines(string(data)) {
		if key, value, ok := strings.Cut(strings.TrimSpace(line), " "); ok && key == name {
			return strconv.ParseInt(value, 10, 64)
		}
	}
	return 0, fmt.Errorf("%s has no %s", path, name)
}

// reportStats answers one ask. If it fails, the engine's wait runs out and the panel dims its last figures.
func (c *Client) reportStats(ctx context.Context, stats *boxStats, ask uint64) {
	reading, err := stats.read(ctx)
	if err != nil {
		log.Printf("box stats: %v", err)
		return
	}
	body, err := json.Marshal(contract.BoxStatsReportRequest{Ask: ask, Reading: reading})
	if err != nil {
		return
	}
	if _, problem := c.forward(ctx, contract.MethodBoxStatsReport, body); problem != nil {
		log.Printf("box stats: %s", problem.Message)
	}
}
