package machines

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"dyna-agent/internal/sysload"
)

// Health is what the last probe found. A disabled machine is never probed, so
// its health stays disabled until it is enabled again.
type Health string

const (
	// HealthOnline means the last probe reached the machine.
	HealthOnline Health = "online"
	// HealthOffline means the last probe could not reach the machine, or the
	// machine answered with an error.
	HealthOffline Health = "offline"
	// HealthDisabled means the owner turned the machine off, so nothing probes
	// it.
	HealthDisabled Health = "disabled"
	// HealthUnknown means no probe has run since the machine was added.
	HealthUnknown Health = "unknown"
)

// Status is the health that the CLI and the clients show. A disabled machine
// shows disabled whatever its last probe read.
func (m Machine) Status() Health {
	if !m.Enabled {
		return HealthDisabled
	}
	return parseHealth(string(m.Health))
}

// parseHealth reads a stored health value, and treats anything it does not
// know as unknown.
func parseHealth(value string) Health {
	for _, health := range []Health{HealthOnline, HealthOffline, HealthDisabled, HealthUnknown} {
		if Health(value) == health {
			return health
		}
	}
	return HealthUnknown
}

// Containers is the container runtime of a machine. A machine can have both,
// and a CI machine uses whichever one it has.
type Containers struct {
	Docker bool `json:"docker"`
	Podman bool `json:"podman"`
}

// Names lists the runtimes the machine has, in the order Dyna prefers them.
func (c Containers) Names() []string {
	names := make([]string, 0, 2)
	if c.Docker {
		names = append(names, "docker")
	}
	if c.Podman {
		names = append(names, "podman")
	}
	return names
}

// Computers is whether a machine can host an agent computer. That needs the
// labwc compositor and Xwayland for X11 clients, which registration checks.
// Stream is separate: wayvnc only streams the desktop to the app, so computers
// run without it. Computers are a capability, not a role.
type Computers struct {
	Labwc    bool `json:"labwc"`
	Xwayland bool `json:"xwayland"`
	// Stream is whether wayvnc can stream this machine's desktop to the app.
	Stream bool `json:"stream"`
}

// Ready reports whether a computer can run on this machine.
func (c Computers) Ready() bool { return c.Labwc && c.Xwayland }

// Capabilities is what one probe read about a machine. A zero field means the
// probe could not read it, so a machine with no /proc still reports its OS.
// Memory values are bytes.
type Capabilities struct {
	OS           string     `json:"os"`
	Arch         string     `json:"arch"`
	CPUs         int        `json:"cpus"`
	MemTotal     uint64     `json:"memTotal"`
	MemAvailable uint64     `json:"memAvailable"`
	GPU          bool       `json:"gpu"`
	Containers   Containers `json:"containers"`
	Computers    Computers  `json:"computers"`
	DynaVersion  string     `json:"dynaVersion"`
	// Harnesses stays empty: no command gives harness versions without the
	// engine, and a probe never asks the machine's engine anything.
}

// Load is the last load reading of a machine. CPUPercent is nil when the probe
// could not take two /proc/stat readings. Memory values are bytes.
type Load struct {
	CPUs         int        `json:"cpus"`
	LoadAvg      [3]float64 `json:"loadAvg"`
	MemTotal     uint64     `json:"memTotal"`
	MemAvailable uint64     `json:"memAvailable"`
	CPUPercent   *float64   `json:"cpuPercent"`
}

// ErrDisabled marks a probe of a machine that is turned off.
var ErrDisabled = errors.New("machine is disabled")

// Probe limits and defaults.
const (
	// probeConcurrency bounds the probes that run at once, so a registry of
	// many machines does not start many SSH connections together.
	probeConcurrency = 4
	// probeBudget is the longest one probe may take, connect time included.
	probeBudget = 20 * time.Second
	// probeKeyPrefix marks a stored probe result in the machines topic, so a
	// health change does not wake the workers that a registry change wakes.
	probeKeyPrefix = "health:"
)

// IsProbeKey reports whether a machines event carries a probe result.
func IsProbeKey(key string) bool { return strings.HasPrefix(key, probeKeyPrefix) }

// Command runs one command with the probe script on its stdin. It matches the
// shape internal/localci uses for its Runner, so a test injects a fake runner
// and never starts a shell or an SSH connection.
type Command func(ctx context.Context, dir string, in io.Reader, out, stderr io.Writer, name string, args ...string) error

// Prober reads machines and stores what it read. The zero value probes the
// real hosts.
type Prober struct {
	Store Store
	// Command replaces exec, for tests.
	Command Command
	// Load returns this machine's load from the engine's sampler. A nil Load
	// makes the local probe read its load from the same script as a remote
	// probe, which is what a CLI probe without a running engine does.
	Load func() sysload.Load
	// Now replaces the clock, for tests.
	Now func() time.Time
}

// ProbeResult is one stored probe.
type ProbeResult struct {
	// At is when the probe started.
	At     time.Time
	Health Health
	// Capabilities and Load are ignored when Health is not online: a failed
	// probe keeps what the last good probe read.
	Capabilities Capabilities
	Load         Load
	// Err is the probe failure to show the owner.
	Err string
}

// ProbeAll probes every enabled machine, at most probeConcurrency at once. A
// probe that fails is stored on its machine and does not stop the others.
func (p Prober) ProbeAll(ctx context.Context) error {
	list, err := p.store().List(ctx)
	if err != nil {
		return err
	}
	slots := make(chan struct{}, probeConcurrency)
	var running sync.WaitGroup
	var mu sync.Mutex
	var failures []error
	for _, m := range list {
		if !m.Enabled {
			continue
		}
		running.Add(1)
		go func(m Machine) {
			defer running.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			if _, err := p.ProbeAndSave(ctx, m); err != nil {
				mu.Lock()
				failures = append(failures, err)
				mu.Unlock()
			}
		}(m)
	}
	running.Wait()
	return errors.Join(failures...)
}

// ProbeAndSave probes one machine and stores the result. It returns the stored
// row, which carries the new health.
func (p Prober) ProbeAndSave(ctx context.Context, m Machine) (Machine, error) {
	if !m.Enabled {
		return m, fmt.Errorf("machine %s: %w", m.Name, ErrDisabled)
	}
	result := ProbeResult{At: p.now(), Health: HealthOnline}
	var err error
	result.Capabilities, result.Load, result.Err, err = p.Probe(ctx, m)
	if err != nil {
		// The caller stopped the probe, so it says nothing about the machine.
		// Storing it would show a healthy machine as offline after a restart.
		return m, err
	}
	if result.Err != "" {
		result.Health = HealthOffline
	}
	if err := p.store().SaveProbe(ctx, m.ID, result); err != nil {
		return Machine{}, err
	}
	return p.store().Get(ctx, m.Name)
}

// Probe runs the probe script on one machine and parses what it printed. A
// machine that cannot be reached comes back as the message to show the owner,
// not as an error. An error means the caller stopped the probe, so there is no
// result to store.
func (p Prober) Probe(ctx context.Context, m Machine) (Capabilities, Load, string, error) {
	budget, cancel := context.WithTimeout(ctx, probeBudget)
	defer cancel()
	var out, stderr bytes.Buffer
	err := p.run(budget, m, &out, &stderr)
	if ctx.Err() != nil {
		// The probe's own budget gave a real result; the caller ending did not.
		return Capabilities{}, Load{}, "", ctx.Err()
	}
	if err != nil {
		return Capabilities{}, Load{}, probeFailure(ctx, err, stderr.String()), nil
	}
	caps, load, err := parseProbe(out.String())
	if err != nil {
		return Capabilities{}, Load{}, probeFailure(ctx, err, stderr.String()), nil
	}
	if m.Local {
		p.localLoad(&load)
	}
	return caps, load, "", nil
}

// localLoad replaces the script's reading of this machine with the engine
// sampler's, which already keeps CPU use and memory. A sampler that has no
// reading yet leaves the script's values alone.
//
// The computer capability is not taken from computers.Enabled(): that also
// depends on computers.enabled, DYNA_COMPUTERS_ENABLED and XDG_RUNTIME_DIR, so
// it is false with the feature off or from a shell without a session, and the
// script's own checks say what the machine has.
func (p Prober) localLoad(load *Load) {
	if p.Load == nil {
		return
	}
	sample := p.Load()
	if sample.CPUs == 0 {
		return
	}
	load.CPUs = max(sample.CPUs, load.CPUs)
	load.LoadAvg = sample.Load
	load.MemTotal = sample.Memory.Total
	if sample.Memory.Total > sample.Memory.Used {
		load.MemAvailable = sample.Memory.Total - sample.Memory.Used
	}
	load.CPUPercent = sample.CPUPercent
}

// run starts the probe command: the script over SSH for a remote machine, and
// a local shell for this machine. No credential or token is part of it.
func (p Prober) run(ctx context.Context, m Machine, out, stderr io.Writer) error {
	command := p.Command
	if command == nil {
		command = execCommand
	}
	in := strings.NewReader(probeScript)
	if m.Local {
		return command(ctx, "", in, out, stderr, "sh", "-s")
	}
	return command(ctx, "", in, out, stderr, "ssh",
		"-o", "BatchMode=yes", "-o", "ConnectTimeout=10", m.SSH, "sh", "-s")
}

// execCommand runs a real command. WaitDelay bounds a child that keeps the
// output pipe open after the command is killed, such as an ssh ProxyCommand.
func execCommand(ctx context.Context, dir string, in io.Reader, out, stderr io.Writer, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Stdin, cmd.Stdout, cmd.Stderr = in, out, stderr
	cmd.WaitDelay = 2 * time.Second
	return cmd.Run()
}

// probeFailure builds the message to show the owner for a failed probe.
func probeFailure(ctx context.Context, err error, stderr string) string {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Sprintf("probe timed out after %s", probeBudget)
	}
	message := strings.TrimSpace(err.Error())
	if detail := strings.TrimSpace(stderr); detail != "" && !strings.Contains(message, detail) {
		message += ": " + detail
	}
	// Cut by rune, so a long message never ends inside a character.
	if runes := []rune(message); len(runes) > 500 {
		message = string(runes[:500])
	}
	return message
}

// probeScript prints what one probe reads, as key=value lines. It needs only a
// POSIX sh and reads a few fixed files. A machine without /proc prints what it
// can, and the parser accepts the missing keys.
const probeScript = `printf 'os=%s\n' "$(uname -s 2>/dev/null)"
printf 'arch=%s\n' "$(uname -m 2>/dev/null)"
printf 'cpus=%s\n' "$(getconf _NPROCESSORS_ONLN 2>/dev/null || echo 0)"
if [ -r /proc/meminfo ]; then
  while read -r K V U; do
    case $K in
      MemTotal:) printf 'memtotal=%s\n' "$((V * 1024))" ;;
      MemAvailable:) printf 'memavailable=%s\n' "$((V * 1024))" ;;
    esac
  done < /proc/meminfo
fi
if [ -r /proc/loadavg ]; then
  read -r L1 L5 L15 _ < /proc/loadavg
  printf 'load1=%s\nload5=%s\nload15=%s\n' "$L1" "$L5" "$L15"
fi
if [ -r /proc/stat ]; then
  readstat() {
    T=0 I=0 N=0
    for F in $(head -n 1 /proc/stat); do
      case $F in cpu) continue ;; esac
      N=$((N + 1))
      T=$((T + F))
      if [ "$N" -eq 4 ] || [ "$N" -eq 5 ]; then I=$((I + F)); fi
    done
  }
  readstat; T1=$T I1=$I
  sleep 1
  readstat; T2=$T I2=$I
  if [ "$T2" -gt "$T1" ]; then
    printf 'idle=%s\n' "$((I2 - I1))"
    printf 'ticks=%s\n' "$((T2 - T1))"
  fi
fi
if [ -d /dev/dri ]; then echo 'gpu=yes'; else echo 'gpu=no'; fi
for C in docker podman labwc Xwayland wayvnc; do
  if command -v "$C" >/dev/null 2>&1; then printf 'has_%s=yes\n' "$C"; else printf 'has_%s=no\n' "$C"; fi
done
if command -v dyna >/dev/null 2>&1; then
  printf 'dyna=%s\n' "$(dyna --version 2>/dev/null | head -n 1)"
fi`

// parseProbe reads the key=value lines of one probe.
func parseProbe(text string) (Capabilities, Load, error) {
	var caps Capabilities
	var load Load
	var idle, total uint64
	for _, line := range strings.Split(text, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch key {
		case "os":
			caps.OS = value
		case "arch":
			caps.Arch = value
		case "cpus":
			caps.CPUs, load.CPUs = int(number(value)), int(number(value))
		case "memtotal":
			caps.MemTotal, load.MemTotal = number(value), number(value)
		case "memavailable":
			caps.MemAvailable, load.MemAvailable = number(value), number(value)
		case "load1":
			load.LoadAvg[0] = decimal(value)
		case "load5":
			load.LoadAvg[1] = decimal(value)
		case "load15":
			load.LoadAvg[2] = decimal(value)
		case "idle":
			idle = number(value)
		case "ticks":
			total = number(value)
		case "gpu":
			caps.GPU = value == "yes"
		case "has_docker":
			caps.Containers.Docker = value == "yes"
		case "has_podman":
			caps.Containers.Podman = value == "yes"
		case "has_labwc":
			caps.Computers.Labwc = value == "yes"
		case "has_Xwayland":
			caps.Computers.Xwayland = value == "yes"
		case "has_wayvnc":
			caps.Computers.Stream = value == "yes"
		case "dyna":
			caps.DynaVersion = value
		}
	}
	if total > 0 && idle <= total {
		// The script prints idle and total ticks; the busy share is the rest.
		percent := 100 * float64(total-idle) / float64(total)
		load.CPUPercent = &percent
	}
	if caps.OS == "" && caps.Arch == "" && caps.CPUs == 0 {
		return Capabilities{}, Load{}, errors.New("the probe printed nothing usable")
	}
	return caps, load, nil
}

// number reads an unsigned decimal field, and 0 when it is not one.
func number(value string) uint64 {
	n, err := strconv.ParseUint(strings.TrimSpace(value), 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// decimal reads a float field, and 0 when it is not one.
func decimal(value string) float64 {
	f, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil {
		return 0
	}
	return f
}

// store is the registry the prober writes to.
func (p Prober) store() Store { return p.Store }

// now is the prober's clock.
func (p Prober) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}
