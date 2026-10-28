//go:build linux

package computers

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"dyna-agent/internal/mode"
	"dyna-agent/internal/runprocs"
	"dyna-agent/internal/shellquote"
	"dyna-agent/internal/shells"
	"dyna-agent/internal/store"
	"dyna-agent/internal/turnlock"
)

type SupervisorOptions struct {
	IdleAfter time.Duration
	ViewGrace time.Duration
	// MonitorEvery is the app and idle poll interval. Zero means one second.
	MonitorEvery  time.Duration
	StreamBackend StreamBackend
	// StartOutputTimeout bounds a new desktop's first output size request.
	// Zero means defaultStartOutputTimeout.
	StartOutputTimeout time.Duration
}
type desktopStart struct {
	done   chan struct{}
	cancel context.CancelFunc
	err    error
}
type desktop struct {
	process                    *process
	wayland, display, renderer string
	// gpu is the render node the desktop and its apps use; empty for the
	// pixman software renderer (D-352).
	gpu renderGPU
	// bus is the computer's own session bus, and busAddress its address. Both
	// are empty when dbus-daemon is missing or failed; apps then use the host bus.
	bus        *process
	busAddress string
}
type supervisor struct {
	mu                 sync.Mutex
	rec                *recording
	lastRec            Recording
	browserMu          sync.Mutex
	starting           *desktopStart
	startOutput        time.Duration // injected start budget; see startOutputBudget
	captureMu          sync.Mutex
	inputGate          chan struct{}
	keyboard           *virtualKeyboard
	meta               Meta
	state              Status
	ctx                context.Context
	cancel             context.CancelFunc
	compositor, stream *process
	// desktopGPU is the render node the running desktop started on (D-352).
	desktopGPU                     renderGPU
	bus                            *process // the computer's own D-Bus session bus
	busAddress                     string
	privateWayland, privateDisplay string
	// actPointer is the pointer of the running action batch. A takeover
	// interrupts it while holding mu.
	actPointer                         *guardedInput
	listeners                          []*net.UnixListener
	relayConns                         map[*net.UnixConn]bool
	apps                               map[int]string
	tracked                            map[int]string
	viewers                            map[string]string
	humanViewer                        string
	emit                               func(Event)
	viewSeq                            uint64
	lastActivity, lastView             time.Time
	viewGrace, idleAfter, monitorEvery time.Duration
	shot                               Shot
	displayLock                        string
	cgroup                             string
	cgroupMu                           sync.Mutex // serializes ensureCgroup
	roomSlice                          string     // the room's slice unit, when the computer's cgroup is a room scope
	streamBackend                      StreamBackend
	monitorWake                        chan struct{}
	x11Conns                           map[*net.UnixConn]bool
}

// newComputer reserves public sockets; the caller holds the daemon registry lock.
func newComputer(ctx context.Context, id, room string, options SupervisorOptions, emit func(Event)) (*supervisor, error) {
	if err := ValidateID(id); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(Dir(id), 0700); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	s := &supervisor{inputGate: make(chan struct{}, 1), ctx: ctx, cancel: cancel, apps: map[int]string{}, viewers: map[string]string{}, relayConns: map[*net.UnixConn]bool{}, x11Conns: map[*net.UnixConn]bool{}, lastActivity: time.Now(), monitorWake: make(chan struct{}, 1), idleAfter: options.IdleAfter, viewGrace: options.ViewGrace, monitorEvery: options.MonitorEvery, emit: emit}
	if s.idleAfter == 0 {
		s.idleAfter = 10 * time.Minute
	}
	if s.viewGrace == 0 {
		s.viewGrace = 15 * time.Second
	}
	s.meta = Meta{SessionID: id, Room: room, WaylandDisplay: publicName(id), Created: time.Now()}
	s.streamBackend = options.StreamBackend
	s.startOutput = options.StartOutputTimeout
	if s.streamBackend == nil {
		s.streamBackend = wayVNCBackend{}
	}
	if previous, err := ReadMeta(id); err == nil {
		s.meta = previous
		if room != "" {
			s.meta.Room = room
		}
	}
	s.state = Status{SessionID: id, Room: s.meta.Room, State: StateCreated, Width: 1280, Height: 800, Control: ControlAgent, Apps: []App{}}
	var prior Status
	if ok, err := readRecord(id, statusRecord, &prior); err == nil && ok {
		s.state.State = StateIdleStopped
		if (Size{Width: prior.Width, Height: prior.Height}).Validate() == nil {
			s.state.Width, s.state.Height = prior.Width, prior.Height
		}
		s.state.Keep = prior.Keep
		s.state.ShotCapturedAt = prior.ShotCapturedAt
		s.state.LatestStatus = prior.LatestStatus
		path := filepath.Join(Dir(id), "last.png")
		if size, err := readShotSize(path); err == nil {
			s.shot = Shot{Path: path, CapturedAt: prior.ShotCapturedAt, Width: size.Width, Height: size.Height}
		}
	}
	if err := os.MkdirAll(RuntimeDir(id), 0700); err != nil {
		cancel()
		return nil, err
	}
	_ = os.Chmod(RuntimeDir(id), 0700)
	// A room computer takes a scope below its room's slice when a desktop
	// starts; until then it keeps whatever cgroup record an earlier daemon
	// life left, so that life's scope can be adopted.
	if s.meta.Room == "" || !roomSlicesOn() {
		s.cgroup = computerCgroup(id)
		s.recordCgroup(s.cgroup)
	}
	public := filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), s.meta.WaylandDisplay)
	_ = os.Remove(public)
	wl, err := unixListener(public)
	if err != nil {
		cancel()
		return nil, err
	}
	s.listeners = append(s.listeners, wl)
	if err = s.listenX11(); err != nil {
		s.cleanup(false)
		return nil, err
	}
	if err = browserDir(id); err != nil {
		s.cleanup(false)
		return nil, err
	}
	if err = saveMeta(s.meta); err != nil {
		s.cleanup(false)
		return nil, err
	}
	s.record("created", nil)
	go s.accept(wl, false)
	for _, l := range s.listeners[1:] {
		go s.accept(l, true)
	}
	go s.maintenance()
	return s, nil
}
func browserDir(id string) error { _, err := browserShims(id, os.Environ()); return err }
func waitPath(ctx context.Context, path string, p *process) error {
	deadline := time.NewTimer(8 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		if _, err := os.Stat(path); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-p.done:
			return fmt.Errorf("process exited before creating %s: %v", path, p.err)
		case <-deadline.C:
			return fmt.Errorf("timed out waiting for %s", path)
		case <-tick.C:
		}
	}
}

// x11Root holds the X11 lock files and the .X11-unix socket directory.
var x11Root = "/tmp"

// listenX11 takes the computer's previous display when it is free, so apps that
// outlive a daemon restart keep a working DISPLAY. A taken previous display is
// only a preference: the computer falls back to the first free display instead
// of staying unable to start.
func (s *supervisor) listenX11() error {
	socketDir := filepath.Join(x11Root, ".X11-unix")
	if err := os.MkdirAll(socketDir, os.ModeSticky|0777); err != nil {
		return err
	}
	pinned := -1
	if s.meta.Display != "" {
		n, err := strconv.Atoi(strings.TrimPrefix(s.meta.Display, ":"))
		if err != nil {
			return err
		}
		pinned = n
	}
	candidates := make([]int, 0, 1801)
	if pinned >= 0 {
		candidates = append(candidates, pinned)
	}
	for n := 200; n <= 1999; n++ {
		if n != pinned {
			candidates = append(candidates, n)
		}
	}
	for _, n := range candidates {
		lock := filepath.Join(x11Root, fmt.Sprintf(".X%d-lock", n))
		if n == pinned {
			if b, err := os.ReadFile(lock); err == nil {
				pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
				if pid > 1 && unix.Kill(pid, 0) == unix.ESRCH {
					_ = os.Remove(lock)
					_ = os.Remove(filepath.Join(socketDir, fmt.Sprintf("X%d", n)))
				}
			}
		}
		f, err := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			continue
		}
		path := filepath.Join(socketDir, fmt.Sprintf("X%d", n))
		l, err := unixListener(path)
		if err != nil {
			f.Close()
			os.Remove(lock)
			continue
		}
		a, err := unixListener("@" + path)
		if err != nil {
			l.Close()
			os.Remove(path)
			f.Close()
			os.Remove(lock)
			continue
		}
		fmt.Fprintf(f, "%10d\n", os.Getpid())
		f.Close()
		s.listeners = append(s.listeners, l, a)
		s.displayLock = lock
		s.meta.Display = ":" + strconv.Itoa(n)
		return nil
	}
	return fmt.Errorf("no free X11 display")
}
func (s *supervisor) accept(l *net.UnixListener, x11 bool) {
	for {
		client, err := l.AcceptUnix()
		if err != nil {
			return
		}
		go func() {
			// The first client may connect before any desktop or browser
			// start, so its processes need the computer's cgroup first.
			s.ensureCgroup(s.ctx)
			s.mu.Lock()
			if s.ctx.Err() != nil {
				s.mu.Unlock()
				client.Close()
				return
			}
			if err := s.trackPeer(client); err != nil {
				s.mu.Unlock()
				client.Close()
				return
			}
			s.mu.Unlock()
			err := s.start()
			s.mu.Lock()
			target := filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), s.privateWayland)
			if x11 {
				number := strings.TrimPrefix(s.privateDisplay, ":")
				number, _, _ = strings.Cut(number, ".")
				target = "/tmp/.X11-unix/X" + number
			}
			s.mu.Unlock()
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				client.Close()
				return
			}
			backend, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: target, Net: "unix"})
			if err != nil {
				client.Close()
				return
			}
			s.mu.Lock()
			s.relayConns[client] = true
			s.relayConns[backend] = true
			if x11 {
				s.x11Conns[client] = true
			}
			s.lastActivity = time.Now()
			s.mu.Unlock()
			_ = Relay(client, backend)
			s.mu.Lock()
			delete(s.relayConns, client)
			delete(s.relayConns, backend)
			if x11 {
				delete(s.x11Conns, client)
			}
			s.lastActivity = time.Now()
			s.mu.Unlock()
		}()
	}
}

// Abstract X11 sockets have no filesystem permissions. Check credentials
// before relaying, so another Unix user cannot borrow the daemon's identity.
func peerPID(conn *net.UnixConn) (int, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return 0, err
	}
	var cred *unix.Ucred
	var credentialErr error
	err = raw.Control(func(fd uintptr) {
		cred, credentialErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	})
	if err != nil {
		return 0, err
	}
	if credentialErr != nil {
		return 0, credentialErr
	}
	if cred.Uid != uint32(os.Getuid()) && cred.Uid != 0 {
		return 0, fmt.Errorf("computer clients must use the daemon's Unix account")
	}
	return int(cred.Pid), nil
}
func (s *supervisor) trackPeer(conn *net.UnixConn) error {
	pid, err := peerPID(conn)
	if err != nil {
		return err
	}
	pid = computerPeerPID(pid)
	if p, ok := processInfo(pid); ok && pid != os.Getpid() {
		s.apps[pid] = p.start
		s.attachCgroup(pid)
	}
	return nil
}
func (s *supervisor) desktopEnv() []string {
	values := map[string]string{"WAYLAND_DISPLAY": s.privateWayland, "DISPLAY": s.privateDisplay, "XDG_SESSION_TYPE": "wayland", "XDG_CURRENT_DESKTOP": "labwc", "DYNA_COMPUTER_PROCESS": s.meta.SessionID}
	// An app in the computer talks to the computer's own session bus, so its
	// portal requests (file choosers) reach a portal that draws on the private
	// display, not the host's.
	if s.busAddress != "" {
		values["DBUS_SESSION_BUS_ADDRESS"] = s.busAddress
	}
	// Apps that start their own systemd units join the room's slice, so a
	// room's systemd-run shim keeps the computer inside the room's limits.
	// An empty value clears a slice the daemon itself inherited, so a
	// roomless computer never carries a foreign room's name into its apps.
	values[mode.EnvRoomSlice] = s.roomSlice
	env := mergeEnvironment(os.Environ(), values)
	// Apps render on the node this desktop started on; the settings value can
	// change while a desktop runs, and a running desktop must not mix GPUs.
	return gpuAppEnvironment(env, s.desktopGPU)
}

// start shares one startup attempt among this computer's first connections.
// No caller holds mu while waiting for labwc or an existing attempt.
func (s *supervisor) start() error {
	s.mu.Lock()
	if s.starting != nil {
		attempt := s.starting
		s.mu.Unlock()
		<-attempt.done
		return attempt.err
	}
	if err := s.ctx.Err(); err != nil {
		s.mu.Unlock()
		return err
	}
	if s.compositor.alive() {
		s.lastActivity = time.Now()
		s.mu.Unlock()
		return nil
	}
	ctx, cancel := context.WithCancel(s.ctx)
	attempt := &desktopStart{done: make(chan struct{}), cancel: cancel}
	s.starting = attempt
	s.state.State = StateStarting
	s.state.Reason = ""
	width, height := s.state.Width, s.state.Height
	s.mu.Unlock()
	started := time.Now()
	desktop, err := s.startDesktop(ctx, width, height)
	var keyboard *virtualKeyboard
	if err == nil {
		keymap, codes, mapErr := keyboardMap(nil, nil)
		if mapErr != nil {
			err = mapErr
		} else {
			keyboard, err = newVirtualKeyboard(ctx, desktop.wayland, keymap, codes)
		}
	}
	s.mu.Lock()
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if err != nil {
		// Stop a completed but cancelled launch before allowing the next attempt.
		s.mu.Unlock()
		if keyboard != nil {
			keyboard.close()
		}
		if desktop != nil {
			desktop.bus.stop()
			removeBusSocket(s.meta.SessionID)
			desktop.process.stop()
		}
		s.mu.Lock()
		if ctx.Err() == nil {
			s.state.State = StateIdleStopped
			s.state.Reason = err.Error()
			s.record("stopped", nil)
		}
	} else {
		s.compositor = desktop.process
		s.bus, s.busAddress = desktop.bus, desktop.busAddress
		s.keyboard = keyboard
		s.privateWayland, s.privateDisplay = desktop.wayland, desktop.display
		s.state.Renderer = desktop.renderer
		s.state.RenderNode = desktop.gpu.Node
		s.state.RenderEnv = gpuEnvValues(desktop.gpu)
		s.desktopGPU = desktop.gpu
		s.state.RefreshHz = idleRefreshHz
		s.state.State = StateRunning
		s.state.StartedAt = time.Now()
		s.state.StartupMS = time.Since(started).Milliseconds()
		s.lastActivity = time.Now()
		s.record("started", nil)
		select {
		case s.monitorWake <- struct{}{}:
		default:
		}
	}
	attempt.err = err
	s.starting = nil
	close(attempt.done)
	s.mu.Unlock()
	cancel()
	return err
}
func (s *supervisor) startDesktop(ctx context.Context, width, height int) (*desktop, error) {
	// The desktop's processes land in the computer's cgroup as they start, so
	// the cgroup must exist first: a room computer takes its room scope now.
	s.ensureCgroup(ctx)
	s.mu.Lock()
	roomSlice := s.roomSlice
	s.mu.Unlock()
	config := filepath.Join(Dir(s.meta.SessionID), "config")
	if err := os.MkdirAll(config, 0700); err != nil {
		return nil, err
	}
	files := map[string]string{"rc.xml": "<labwc_config><core><xwaylandPersistence>no</xwaylandPersistence></core><keyboard><default/></keyboard><mouse><default/></mouse></labwc_config>", "autostart": "", "environment": ""}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(config, name), []byte(body), 0600); err != nil {
			return nil, err
		}
	}
	ready := filepath.Join(RuntimeDir(s.meta.SessionID), "desktop.env")
	script := filepath.Join(config, "ready")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s\\n%s\\n' \"$WAYLAND_DISPLAY\" \"$DISPLAY\" > "+shellquote.Quote(ready+".tmp")+"\nmv "+shellquote.Quote(ready+".tmp")+" "+shellquote.Quote(ready)+"\n"), 0700); err != nil {
		return nil, err
	}
	childBin, err := s.childShims(config)
	if err != nil {
		return nil, err
	}
	renderers := []string{"pixman"}
	// The render node comes from computers.renderNode, which by default keeps
	// Dyna computers off the GPU that drives the owner's display (D-352).
	gpu := configuredRenderGPU()
	if gpu.Node != "" {
		renderers = []string{"gles2", "pixman"}
	}
	for _, renderer := range renderers {
		_ = os.Remove(ready)
		values := map[string]string{"PATH": childBin + ":" + os.Getenv("PATH"), "WAYLAND_DISPLAY": "", "DISPLAY": "", "WLR_BACKENDS": "headless", "WLR_HEADLESS_OUTPUTS": "1", "WLR_LIBINPUT_NO_DEVICES": "1", "WLR_RENDERER": renderer, "XDG_CURRENT_DESKTOP": "labwc", "XDG_SESSION_TYPE": "wayland", "DYNA_COMPUTER_PROCESS": s.meta.SessionID, mode.EnvRoomSlice: roomSlice}
		if renderer == "gles2" {
			values["WLR_RENDER_DRM_DEVICE"] = gpu.Node
		}
		env := mergeEnvironment(os.Environ(), values)
		// The desktop's own children, such as XWayland, inherit the GPU choice.
		env = gpuAppEnvironment(env, gpu)
		p, err := startProcess(env, filepath.Join(Dir(s.meta.SessionID), "labwc.log"), "labwc", "-C", config, "-s", shellquote.Quote(script))
		if err != nil {
			return nil, err
		}
		_ = os.WriteFile(filepath.Join(RuntimeDir(s.meta.SessionID), "compositor.pid"), []byte(strconv.Itoa(p.cmd.Process.Pid)+"\n"), 0600)
		s.attachCgroup(p.cmd.Process.Pid)
		err = waitPath(ctx, ready, p)
		if err != nil {
			p.stop()
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			continue
		}
		b, err := os.ReadFile(ready)
		if err != nil {
			p.stop()
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			continue
		}
		lines := strings.Split(strings.TrimSpace(string(b)), "\n")
		if len(lines) < 2 {
			p.stop()
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			continue
		}
		d := &desktop{process: p, wayland: lines[0], display: lines[1], renderer: renderer, gpu: gpu}
		env = mergeEnvironment(env, map[string]string{"WAYLAND_DISPLAY": d.wayland, "DISPLAY": d.display})
		if err := s.sizeNewDesktop(ctx, env, width, height); err != nil {
			p.stop()
			return nil, err
		}
		s.startBus(ctx, d, env)
		return d, nil
	}
	return nil, fmt.Errorf("labwc failed with GPU and software rendering; inspect %s", filepath.Join(Dir(s.meta.SessionID), "labwc.log"))
}

// startBus gives the computer its own D-Bus session bus. Without one, an app's
// request to the desktop portal goes over the host's session bus, and the host's
// portal backend draws the file chooser on the host display. The portal and its
// backend start on this bus by activation, from the bus's environment, which
// names the private display. A failure leaves the computer working as before.
func (s *supervisor) startBus(ctx context.Context, d *desktop, env []string) {
	path := BusSocket(s.meta.SessionID)
	removeBusSocket(s.meta.SessionID)
	address := "unix:path=" + path
	p, err := startProcess(busEnvironment(env, address), filepath.Join(Dir(s.meta.SessionID), "dbus.log"), busCommand(address)...)
	if err != nil {
		return
	}
	s.attachCgroup(p.cmd.Process.Pid)
	if err := waitPath(ctx, path, p); err != nil {
		p.stop()
		return
	}
	d.bus, d.busAddress = p, address
}

// removeBusSocket deletes a computer's bus socket, left behind when its bus
// stops, so a stopped computer's apps do not get a dead address.
func removeBusSocket(id string) { _ = os.Remove(BusSocket(id)) }

// busCommand is the private session bus: a session bus on a socket in the
// computer's runtime directory, in the foreground so the computer owns it.
func busCommand(address string) []string {
	return []string{"dbus-daemon", "--session", "--nofork", "--nopidfile", "--address=" + address}
}

// busEnvironment is what the bus and every service it activates inherit: the
// private display, so a portal backend opens its windows inside the computer.
func busEnvironment(env []string, address string) []string {
	return mergeEnvironment(env, map[string]string{"DBUS_SESSION_BUS_ADDRESS": address, "XDG_CURRENT_DESKTOP": "labwc", "XDG_SESSION_TYPE": "wayland"})
}

// recordIntent requires the caller to hold mu, like record.
func (s *supervisor) recordIntent(intent string) {
	s.state.LatestStatus = &ActionIntent{Text: intent, At: time.Now()}
	s.record("status", nil)
}

func (s *supervisor) record(kind string, shot *Shot) {
	_ = saveStatus(s.state)
	f, err := os.OpenFile(EventsPath(s.meta.SessionID), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err == nil {
		_ = json.NewEncoder(f).Encode(Event{Type: kind, SessionID: s.meta.SessionID, TS: time.Now(), Status: &s.state, Shot: shot})
		f.Close()
	}
	if s.emit != nil {
		s.emit(Event{Type: kind, SessionID: s.meta.SessionID, TS: time.Now(), Status: statusCopy(s.state), Shot: shot})
	}
}
func (s *supervisor) owned() map[int]string {
	all := processes()
	roots := map[int]string{}
	if s.compositor != nil {
		pid := s.compositor.cmd.Process.Pid
		if p, ok := all[pid]; ok {
			roots[pid] = p.start
		}
	}
	if s.stream != nil {
		pid := s.stream.cmd.Process.Pid
		if p, ok := all[pid]; ok {
			roots[pid] = p.start
		}
	}
	for pid, start := range s.apps {
		roots[pid] = start
	}
	owned := descendants(all, roots)
	for _, pid := range runprocs.Stamped("DYNA_COMPUTER_PROCESS=" + s.meta.SessionID) {
		if p, ok := all[pid]; ok {
			owned[pid] = p.start
		}
	}
	return owned
}
func (s *supervisor) status() Status {
	for pid := range browserDaemonPIDs(s.meta.SessionID) {
		s.attachCgroup(pid)
	}
	s.state.MemoryCurrentKiB = 0
	if memory, ok := cgroupMemoryKiB(s.cgroup); ok {
		s.state.MemoryCurrentKiB = memory
	}
	owned := s.statusPIDs()
	s.state.RSSKiB = rssKiB(owned)
	s.state.StreamRSSKiB = 0
	if s.stream.alive() {
		pid := s.stream.cmd.Process.Pid
		if p, ok := processInfo(pid); ok {
			s.state.StreamRSSKiB = rssKiB(map[int]string{pid: p.start})
		}
	}
	s.state.IdleSeconds = int64(time.Since(s.lastActivity).Seconds())
	if s.compositor != nil && !s.compositor.alive() && s.state.State == StateRunning {
		s.state.Reason = "compositor exited"
		s.stop()
	}
	if s.compositor.alive() {
		s.readApps()
	}
	return s.state
}
func (s *supervisor) stop() error {
	if s.rec != nil {
		s.rec.cancel()
	}
	if s.starting != nil {
		s.starting.cancel()
	}
	s.stopStream()
	if s.keyboard != nil {
		s.keyboard.close()
		s.keyboard = nil
	}
	stopProfileBrowsers(s.meta.SessionID)
	for c := range s.relayConns {
		c.Close()
	}
	s.relayConns = map[*net.UnixConn]bool{}
	s.x11Conns = map[*net.UnixConn]bool{}
	stopBrowserDaemons(s.meta.SessionID)
	owned := s.owned()
	s.killCgroup()
	for pid := range owned {
		_ = unix.Kill(pid, unix.SIGSTOP)
	}
	for pid := range owned {
		_ = unix.Kill(pid, unix.SIGKILL)
	}
	// Wait for the selected processes before copying their login state.
	deadline := time.Now().Add(2 * time.Second)
	for browserProcessesAlive(owned) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	saveCtx, saveCancel := context.WithTimeout(context.Background(), 5*time.Second)
	saveErr := withBrowserCleanup(saveCtx, s.meta.SessionID, func() error { return saveBrowserProfile(saveCtx, s.meta.SessionID) })
	if saveErr != nil {
		s.state.Reason += "; browser save-back: " + saveErr.Error()
	}
	saveCancel()
	s.bus.stop()
	// A stopped computer has no bus; apps launched meanwhile keep their own.
	removeBusSocket(s.meta.SessionID)
	s.bus, s.busAddress = nil, ""
	s.compositor.stop()
	s.compositor = nil
	s.desktopGPU = renderGPU{}
	// A stopped computer has no desktop: forget its display names, so nothing
	// dials a socket the next desktop will not reuse.
	s.privateWayland, s.privateDisplay = "", ""
	s.apps = map[int]string{}
	s.tracked = nil
	s.state.Apps = []App{}
	// killCgroup took the room scope down with its anchor; the next start
	// asks systemd for a new one, and nothing may join the dead one meanwhile.
	if s.roomSlice != "" {
		s.cgroup, s.roomSlice = "", ""
		s.recordCgroup("")
	}
	s.state.State = StateIdleStopped
	s.state.RefreshHz = 0
	s.state.Control = ControlAgent
	s.state.ControlReason = nil
	s.humanViewer = ""
	s.viewers = map[string]string{}
	s.state.Viewers = 0
	s.record("stopped", nil)
	s.lastActivity = time.Now()
	reapAdopted(owned)
	return saveErr
}
func reapAdopted(owned map[int]string) {
	for pid := range owned {
		reapUnownedPID(pid)
	}
}
func (s *supervisor) cleanup(destroy bool) error {
	s.cancel()
	s.mu.Lock()
	if s.starting != nil {
		done := s.starting.done
		s.mu.Unlock()
		<-done
		s.mu.Lock()
	}
	defer s.mu.Unlock()
	s.state.Reason = "daemon stopped"
	if destroy {
		s.state.Reason = "session destroyed"
	}
	saveErr := s.stop()
	for _, l := range s.listeners {
		path := l.Addr().String()
		l.Close()
		if !strings.HasPrefix(path, "@") {
			_ = os.Remove(path)
		}
	}
	if s.displayLock != "" {
		_ = os.Remove(s.displayLock)
	}
	// The room scope holds its anchor past the daemon; this computer is going
	// away, so take the anchor with it before the record that names it goes.
	killRecordedScope(s.meta.SessionID)
	_ = os.RemoveAll(RuntimeDir(s.meta.SessionID))
	_ = os.RemoveAll(browserSocketDir(os.Getenv("XDG_RUNTIME_DIR"), s.meta.SessionID))
	if destroy && saveErr == nil {
		// Hold the same lock the offline path uses, so it cannot read the lease
		// while this profile is being removed.
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = withBrowserCleanup(cleanupCtx, s.meta.SessionID, func() error {
			return os.RemoveAll(filepath.Join(Dir(s.meta.SessionID), "profiles"))
		})
		cancel()
		s.state.State = StateDestroyed
	}
	s.state.RSSKiB = 0
	s.state.MemoryCurrentKiB = 0
	// A room scope belongs to systemd: killing its contents lets systemd
	// remove it, and only the daemon's own leaf is unlinked here.
	if s.cgroup != "" && underCgroup(s.cgroup, computerGroups.root) {
		_ = os.Remove(s.cgroup)
	}
	s.record("stopped", nil)
	return saveErr
}

// monitorInterval is how often the maintenance loop polls; zero means one second.
func (s *supervisor) monitorInterval() time.Duration {
	if s.monitorEvery > 0 {
		return s.monitorEvery
	}
	return time.Second
}
func (s *supervisor) maintenance() {
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-s.monitorWake:
		}
		tick := time.NewTicker(s.monitorInterval())
		active := true
		for active {
			select {
			case <-s.ctx.Done():
				tick.Stop()
				return
			case <-tick.C:
				s.mu.Lock()
				if s.compositor != nil && !s.compositor.alive() {
					s.state.Reason = "compositor exited"
					s.stop()
				}
				if s.compositor.alive() {
					s.readApps()
				}
				if s.stream != nil && !s.stream.alive() {
					s.stream = nil
					s.state.Stream = Stream{}
					s.record("viewers", nil)
				}
				if s.stream != nil && len(s.viewers) == 0 && time.Since(s.lastView) >= s.viewGrace {
					s.stopStream()
					s.record("viewers", nil)
				}
				s.stopIfIdle()
				active = s.compositor.alive() || s.stream != nil
				s.mu.Unlock()
			}
		}
		tick.Stop()
	}
}
func (s *supervisor) readApps() {
	apps, err := desktopApps(s.ctx, s.privateWayland)
	if err != nil {
		return
	}
	if len(s.x11Conns) > 0 {
		if xapps, err := x11Apps(s.ctx, s.privateDisplay); err == nil {
			apps = append(apps, xapps...)
		}
	}
	sort.Slice(apps, func(i, j int) bool {
		a, b := apps[i], apps[j]
		if a.AppID == b.AppID {
			return a.Title < b.Title
		}
		return a.AppID < b.AppID
	})
	if !sameApps(s.state.Apps, apps) {
		s.state.Apps = apps
		s.record("apps", nil)
	}
}
func (s *supervisor) dispatch(ctx context.Context, r Request) (any, error) {
	if r.Op == "run.prepare" {
		return s.prepareRun(ctx, r)
	}
	if r.Op == "size" {
		return s.resize(ctx, r)
	}
	if r.Op == "open" {
		return s.openApp(ctx, r)
	}
	if r.Op == "record.start" {
		return s.startRecord(r)
	}
	if r.Op == "record.stop" {
		return s.stopRecord()
	}
	if r.Op == "browser.start" {
		return s.startBrowser(ctx)
	}
	if r.Op == "browser.stop" {
		s.browserMu.Lock()
		defer s.browserMu.Unlock()
	}

	if r.Op == "browser.prepare" {
		// The launcher joins the computer's cgroup once this returns, so the
		// cgroup must exist before then even when no desktop runs yet.
		s.ensureCgroup(ctx)
		return prepareChromiumLaunch(ctx, s.meta.SessionID, r.BrowserArgs)
	}
	if r.Op == "browser.navigate" {
		return s.openBrowser(ctx, r)
	}
	if r.Op == "act" {
		return s.act(ctx, r)
	}
	if r.Op == "shot" {
		return s.capture(r.Fresh, false)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	switch r.Op {
	case "browser.open":
		if err := openBrowserURLs(ctx, s.meta.SessionID, r.BrowserArgs); err != nil {
			return nil, err
		}
		return map[string]bool{"opened": true}, nil
	case "browser.stop":
		stopProfileBrowsers(s.meta.SessionID)
		return map[string]bool{"stopped": true}, nil
	case "status":
		return s.status(), nil
	case "view.open":
		if !s.compositor.alive() {
			return nil, fmt.Errorf("no windows yet")
		}
		if err := s.setRefresh(60); err != nil {
			return nil, err
		}
		if err := s.startStream(); err != nil {
			if len(s.viewers) == 0 {
				_ = s.setRefresh(10)
			}
			return nil, err
		}
		s.viewSeq++
		id := strconv.FormatUint(s.viewSeq, 10)
		s.viewers[id] = ConnectionID(ctx)
		go func() { <-ctx.Done(); s.releaseViewer(id) }()
		s.state.Viewers = len(s.viewers)
		s.lastActivity = time.Now()
		s.record("viewers", nil)
		return View{ViewerID: id, RFBSocket: filepath.Join(RuntimeDir(s.meta.SessionID), "rfb.sock")}, nil
	case "view.close":
		s.releaseViewerLocked(r.ViewerID)
		s.state.Viewers = len(s.viewers)
		if len(s.viewers) == 0 {
			s.lastView = time.Now()
		}
		s.record("viewers", nil)
		return Viewers{s.state.Viewers}, nil
	case "control":
		if r.Mode != ControlAgent && r.Mode != ControlHuman {
			return nil, fmt.Errorf("invalid control mode")
		}
		if r.ViewerID == "" {
			// The agent asks the person to take over; no viewer lease is taken or changed.
			if r.Mode != ControlHuman {
				return nil, fmt.Errorf("only a viewer can return control")
			}
			reason, err := RequiredReason(r.Status)
			if err != nil {
				return nil, err
			}
			// Without a desktop nobody can watch, and agent input would wait for ever.
			if !s.compositor.alive() {
				return nil, fmt.Errorf("no desktop yet: open an app first, then ask for control")
			}
			s.state.Control = ControlHuman
			s.interruptActLocked()
			s.state.ControlReason = &ActionIntent{Text: reason, At: time.Now()}
			s.lastActivity = time.Now()
			s.record("control", nil)
			return Control{r.Mode}, nil
		}
		if _, ok := s.viewers[r.ViewerID]; !ok {
			return nil, fmt.Errorf("viewer lease is closed")
		}
		if r.Mode == ControlHuman && s.humanViewer != "" && s.humanViewer != r.ViewerID {
			return nil, fmt.Errorf("another viewer has control")
		}
		s.humanViewer = ""
		if r.Mode == ControlHuman {
			s.humanViewer = r.ViewerID
		}
		s.state.Control = r.Mode
		if r.Mode == ControlHuman {
			s.interruptActLocked()
		}
		if r.Mode == ControlAgent {
			s.state.ControlReason = nil
		}
		s.lastActivity = time.Now()
		s.record("control", nil)
		return Control{r.Mode}, nil
	case "keep":
		s.state.Keep = r.Keep
		s.lastActivity = time.Now()
		s.record("status", nil)
		return s.status(), nil
	case "stop":
		if r.Status != "" {
			intent, err := RequiredStatus(r.Status)
			if err != nil {
				return nil, err
			}
			s.recordIntent(intent)
		}
		s.state.Reason = "explicit stop"
		err := s.stop()
		return Stopped{s.state.State}, err
	default:
		return nil, fmt.Errorf("unknown computer operation %q", r.Op)
	}
}
func (s *supervisor) startStream() error {
	if s.stream.alive() {
		return nil
	}
	socket := filepath.Join(RuntimeDir(s.meta.SessionID), "rfb.sock")
	_ = os.Remove(socket)
	args, offer, err := s.streamBackend.Command(s.ctx, StreamOptions{Socket: socket, ConfigDir: filepath.Join(Dir(s.meta.SessionID), "config"), RenderNode: s.desktopGPU.Node})
	if err != nil {
		return err
	}
	var p *process
	for attempt := 0; attempt < 2; attempt++ {
		p, err = startProcess(s.desktopEnv(), filepath.Join(Dir(s.meta.SessionID), "wayvnc.log"), args...)
		if err == nil {
			s.stream = p
			s.attachCgroup(p.cmd.Process.Pid)
			err = waitPath(s.ctx, socket, p)
		}
		if err == nil {
			break
		}
		p.stop()
		s.stream = nil
		gpuIndex := -1
		for i, arg := range args {
			if arg == "--gpu" {
				gpuIndex = i
				break
			}
		}
		if attempt != 0 || gpuIndex < 0 {
			return err
		}
		args = append(args[:gpuIndex], args[gpuIndex+1:]...)
		offer.Encoder = "software (GPU startup failed)"
		_ = os.Remove(socket)
	}
	s.state.Stream = offer
	return os.Chmod(socket, 0600)
}
func (s *supervisor) stopStream() {
	s.stream.stop()
	s.stream = nil
	s.state.Stream = Stream{}
	_ = os.Remove(filepath.Join(RuntimeDir(s.meta.SessionID), "rfb.sock"))
}
func vaapiWorks(ctx context.Context, node string) bool {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if node == "" {
		return false
	}
	cmd := exec.CommandContext(ctx, "ffmpeg", "-nostdin", "-v", "error", "-vaapi_device", node, "-f", "lavfi", "-i", "color=size=64x64:rate=1", "-vf", "format=nv12,hwupload", "-frames:v", "1", "-c:v", "h264_vaapi", "-f", "null", "-")
	return runOwned(cmd) == nil
}

func statusCopy(s Status) *Status { s.Apps = append([]App{}, s.Apps...); return &s }
func (s *supervisor) releaseViewer(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.releaseViewerLocked(id)
}
func (s *supervisor) releaseViewerLocked(id string) {
	if _, ok := s.viewers[id]; !ok {
		return
	}
	delete(s.viewers, id)
	s.state.Viewers = len(s.viewers)
	if len(s.viewers) == 0 {
		if err := s.setRefresh(idleRefreshHz); err != nil {
			fmt.Fprintln(os.Stderr, err)
		}
	}
	s.lastView = time.Now()
	if s.humanViewer == id {
		s.humanViewer = ""
		s.state.Control = ControlAgent
		s.state.ControlReason = nil
		s.record("control", nil)
	}
	s.record("viewers", nil)
}
func (s *supervisor) setRefresh(hz int) error {
	if !s.compositor.alive() || s.state.RefreshHz == hz {
		return nil
	}
	if err := setOutputSize(s.ctx, s.desktopEnv(), s.state.Width, s.state.Height, hz, liveOutputTimeout); err != nil {
		return err
	}
	s.state.RefreshHz = hz
	return nil
}

// A live resize or refresh change runs while callers wait, so it stays short.
// A new desktop on a loaded host can take longer to answer its first request.
const (
	liveOutputTimeout         = 3 * time.Second
	defaultStartOutputTimeout = 15 * time.Second
)

// sizeNewDesktop sets a new desktop's output with the start budget, not the
// live one: a loaded host can answer the first request late.
func (s *supervisor) sizeNewDesktop(ctx context.Context, env []string, width, height int) error {
	return setOutputSize(ctx, env, width, height, 10, s.startOutputBudget())
}

// startOutputBudget is the injected start budget, or the default when unset.
func (s *supervisor) startOutputBudget() time.Duration {
	if s.startOutput > 0 {
		return s.startOutput
	}
	return defaultStartOutputTimeout
}

func setOutputSize(parent context.Context, env []string, width, height, hz int, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "wlr-randr", "--output", "HEADLESS-1", "--custom-mode", fmt.Sprintf("%dx%d@%dHz", width, height, hz), "--scale", "1")
	cmd.Env = env
	if output, err := combinedOwned(cmd); err != nil {
		return fmt.Errorf("set computer size: %w: %s", err, output)
	}
	return nil
}

func sameApps(a, b []App) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// stopIfIdle requires the caller to hold mu. Open apps do not keep a computer
// alive: each holds a relay connection for as long as it runs, so a browser
// left open would never stop. A running turn or shell job of the owning
// session does, because an agent drives its browser over CDP, which this
// supervisor never sees. A computer with keep on never stops here, for an app
// that no session turn launched, such as a game client run by a service.
func (s *supervisor) stopIfIdle() {
	if s.state.Keep {
		return
	}
	if s.compositor.alive() && len(s.viewers) == 0 && time.Since(s.lastActivity) >= s.idleAfter {
		if sessionTurnRunning(s.meta.SessionID) || sessionShellRunning(s.meta.SessionID) {
			s.lastActivity = time.Now()
			return
		}
		s.state.Reason = "idle timeout with no viewer or running turn"
		s.stop()
	}
}

// sessionShellRunning reports a running Dyna shell job of the session, such
// as a QA script that drives the browser after the turn ended.
func sessionShellRunning(id string) bool {
	jobs, err := shells.DefaultStore().List(false)
	if err != nil {
		return false
	}
	for _, job := range jobs {
		if job.Owner == id {
			return true
		}
	}
	return false
}

// sessionTurnRunning reports whether a turn of the session runs. Computers
// without a session never match.
func sessionTurnRunning(id string) bool {
	return turnlock.Held(filepath.Join(store.DataDir(), "sessions", id), id)
}
