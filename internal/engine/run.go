package engine

import (
	"context"
	"dyna-agent/internal/authority"
	"dyna-agent/internal/database"
	"dyna-agent/internal/filesvc"
	"dyna-agent/internal/gitsvc"
	"embed"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"text/tabwriter"
	"time"

	"dyna-agent/internal/account/service"
	accountstore "dyna-agent/internal/account/store"
	"dyna-agent/internal/automations"
	"dyna-agent/internal/callerid"
	"dyna-agent/internal/callspec"
	"dyna-agent/internal/computerrestart"
	"dyna-agent/internal/computerweb"
	"dyna-agent/internal/configsvc"
	"dyna-agent/internal/connect"
	"dyna-agent/internal/devicesvc"
	"dyna-agent/internal/doctor"
	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/fsutil"
	"dyna-agent/internal/hooks"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/jobs"
	"dyna-agent/internal/lanesync"
	"dyna-agent/internal/localci"
	"dyna-agent/internal/machines"
	"dyna-agent/internal/maintenance"
	"dyna-agent/internal/mode"
	prstate "dyna-agent/internal/pr/state"
	"dyna-agent/internal/room"
	"dyna-agent/internal/roomsvc"
	"dyna-agent/internal/scope"
	"dyna-agent/internal/sessionlife"
	"dyna-agent/internal/shells"
	"dyna-agent/internal/state"
	"dyna-agent/internal/store"
	"dyna-agent/internal/subagent"
	"dyna-agent/internal/sysload"
	"dyna-agent/internal/terminalsvc"
	"dyna-agent/internal/userservices"
	"dyna-agent/internal/voice"
	"dyna-agent/internal/websvc"
	"dyna-agent/internal/websvc/devicestore"
	"dyna-agent/web"
)

//go:embed notbuilt
var notBuilt embed.FS

// agentStamps mark a process that a Dyna agent session started. Engine and
// its pairing commands refuse them without --allow-agent, so an agent does
// not mint itself an owner device by habit. This stops accidents only: a
// process that runs as the owner's OS user can unset them.
var agentStamps = mode.AgentStampKeys()

const serveHelp = `dyna engine run runs the Dyna desktop app as a web app for a browser
or a phone. It listens on loopback only. Reach it from a phone through
tailscale serve or a cloudflared tunnel; see docs/desktop/WEB.md.

Usage:
  dyna engine run [flags]          run the server (prints a pairing code)
  dyna engine run --pair [--scope S]  make a one-time pairing code and exit
  dyna engine run --devices           list paired devices, sessions and scopes
  dyna engine run --grant ID --scope S  replace the scopes of one device or session
  dyna engine run --revoke ID|all  remove paired devices or Connect sessions at once

When this computer is linked to Resident Labs Connect (dyna engine
connect link), serve also runs the managed tunnel, accepts passkey
sign-ins through the relay, and sends Web Push. See docs/desktop/CONNECT.md.

Flags:
`

const defaultServeAddr = "127.0.0.1:7457"

type Options struct {
	Addr, CardAddr string
	Routes         []string
	ReadOnly       bool
	ConnectGrace   time.Duration
	NoTunnel       bool
	NoTailscale    bool
	NoPush         bool
	Cloudflared    string
	ConnectAccess  time.Duration
	TerminalIdle   time.Duration
}

type commandFlags struct {
	Options
	Pair, Devices, AllowAgent bool
	Revoke, Grant             string
	Scopes                    []string
}

func parseServe(args []string, out io.Writer) (commandFlags, error) {
	var f commandFlags
	fs := flag.NewFlagSet("dyna engine run", flag.ContinueOnError)
	fs.SetOutput(out)
	fs.StringVar(&f.Addr, "addr", defaultServeAddr, "app listener, loopback only")
	fs.StringVar(&f.CardAddr, "card-addr", "127.0.0.1:7458", "HTML card listener, loopback only")
	fs.Func("route", "one more way to reach the server: `APP_ORIGIN,CARD_ORIGIN`, for example\nhttps://dyna.example.ts.net,https://dyna.example.ts.net:8443 (repeatable)", func(s string) error {
		f.Routes = append(f.Routes, s)
		return nil
	})
	fs.BoolVar(&f.ReadOnly, "read-only", false, "never write: no posts, pins, attachments or read marks")
	fs.BoolVar(&f.Pair, "pair", false, "print a new one-time pairing code and exit")
	fs.Func("scope", "grant `SCOPE` on the pairing code (repeatable). With no --scope, this computer's browser gets every scope and other devices get read and operate. Known scopes: read, operate, files, terminal, computers, admin", func(s string) error {
		f.Scopes = append(f.Scopes, s)
		return nil
	})
	fs.BoolVar(&f.Devices, "devices", false, "list paired devices and exit")
	fs.StringVar(&f.Grant, "grant", "", "replace the scopes of paired device or Connect session `ID` and exit")
	fs.StringVar(&f.Revoke, "revoke", "", "remove a paired device by `ID`, or all of them, and exit")
	fs.BoolVar(&f.AllowAgent, "allow-agent", false, "run even inside a Dyna agent session")
	fs.DurationVar(&f.ConnectGrace, "connect-grace", connect.DefaultGrace, "how long Connect sessions renew while the relay cannot be reached (0 to 1h)")
	fs.BoolVar(&f.NoTunnel, "no-tunnel", false, "do not run cloudflared for the managed tunnel")
	fs.BoolVar(&f.NoTailscale, "no-tailscale", false, "do not add a route found in tailscale serve")
	fs.BoolVar(&f.NoPush, "no-push", false, "do not send Web Push notifications")
	fs.StringVar(&f.Cloudflared, "cloudflared", "cloudflared", "the cloudflared binary for the managed tunnel")
	fs.DurationVar(&f.TerminalIdle, "terminal-idle", time.Hour, "close terminals after no input or output for this duration (negative disables)")
	fs.DurationVar(&f.ConnectAccess, "connect-access", connect.AccessLife, "the Connect access window between renewals (30s to 15m; shorter is for tests)")
	fs.Usage = func() {
		fmt.Fprint(out, serveHelp)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return f, err
	}
	if fs.NArg() > 0 {
		return f, fmt.Errorf("dyna engine run takes no arguments, got %q", fs.Arg(0))
	}
	if f.Grant != "" && (f.Pair || f.Devices || f.Revoke != "") {
		return f, errors.New("--grant cannot be combined with --pair, --devices, or --revoke")
	}
	if len(f.Scopes) > 0 && !f.Pair && f.Grant == "" {
		return f, errors.New("--scope is only valid with --pair or --grant")
	}
	if f.Grant != "" && len(f.Scopes) == 0 {
		return f, errors.New("--grant needs at least one --scope")
	}
	if f.Pair {
		// No --scope leaves the code unscoped: the route where pairing
		// happens picks the grant in the web server.
		if len(f.Scopes) > 0 {
			var err error
			if f.Scopes, err = scope.Normalize(f.Scopes); err != nil {
				return f, err
			}
		}
	}
	if f.Grant != "" {
		var err error
		if f.Scopes, err = scope.Normalize(f.Scopes); err != nil {
			return f, err
		}
	}
	return f, nil
}

// Main runs "dyna engine run". It returns the exit code.
func Main(ctx context.Context, args []string) int {
	f, err := parseServe(args, os.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if !f.AllowAgent {
		for _, k := range agentStamps {
			if os.Getenv(k) != "" {
				fmt.Fprintf(os.Stderr, "dyna engine run: %s is set, so a Dyna agent session started this. Run it from your own terminal, or pass --allow-agent.\n", k)
				return 2
			}
		}
	}
	switch {
	case f.Pair:
		store, err := devicestore.DefaultStore()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return printCode(store, f.Scopes)
	case f.Devices:
		return printDevices(devicesvc.New(subagent.DefaultStore().Root), os.Stdout)
	case f.Grant != "":
		dev, err := devicesvc.New(subagent.DefaultStore().Root).SetScopes(f.Grant, f.Scopes)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Printf("%s\t%s\t%s\n", dev.ID, dev.Kind, dev.ScopeLabel)
		return 0
	case f.Revoke != "":
		n, err := devicesvc.New(subagent.DefaultStore().Root).RevokeDevice("all", f.Revoke)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Printf("removed %d device(s) or session(s); they lose access on their next request\n", n)
		return 0
	}
	if err := Run(ctx, f.Options); err != nil {
		fmt.Fprintln(os.Stderr, "dyna engine run:", err)
		return 1
	}
	return 0
}

func printCode(store *devicestore.Store, scopes []string) int {
	code, err := store.NewScopedCode("", scopes)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	grant := scope.Label(scopes, false)
	if len(scopes) == 0 {
		grant = "every scope from this computer, read and operate elsewhere"
	}
	fmt.Printf("Pairing code: %s\nScopes: %s\nIt works once, for %d minutes. Open the web app on the device and type it.\n", code, grant, int(devicestore.CodeLife/time.Minute))
	return 0
}

func printDevices(svc *devicesvc.Service, out io.Writer) int {
	list, err := svc.Devices()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tTYPE\tNAME\tSCOPES\tSIGNED IN\tLAST SEEN\tEXPIRES")
	for _, d := range list {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", d.ID, d.Kind, d.Name, d.ScopeLabel, d.Created.Format(time.DateTime), d.LastSeen.Format(time.DateTime), d.Expires.Format(time.DateOnly))
	}
	_ = tw.Flush()
	return 0
}

// publishState sends committed state changes to clients through the hub. A
// PR state change goes to the room service instead, which turns it into the
// PR rows that clients read.
func publishState(events *hub.Hub, rooms *roomsvc.Service, signals *workerSignals) func(hub.Event) {
	return func(ev hub.Event) {
		switch ev.Topic {
		case localci.Topic:
			key, _ := localci.ParseEventKey(ev.Key)
			if head, ok := key.ResultSHA(); ok {
				roomIDs, err := rooms.PRs.LocalCIResultRooms(head)
				if err != nil {
					log.Printf("local CI room refresh: %v", err)
				}
				for _, roomID := range roomIDs {
					change := roomsvc.Event{Kind: "prs", RoomID: roomID}
					events.Publish(hub.Event{Topic: "prs", Room: roomID, Body: change})
				}
			}
			if key.WakesCI() {
				wakeWorker(signals.channels["prs"])
				wakeWorker(signals.channels["merges"])
			}
			return
		case "call-requests":
			wakeWorker(signals.channels["call-requests"])
			rooms.CallRequestsChanged()
			return
		case "callers":
			if ev.Key != "" {
				rooms.CoordinatorChanged(ev.Key)
			}
		case "room-post":
			rooms.PostsChanged(ev.Room)
			return
		case "goals", "questions", "notices", "sync":
			rooms.StoreChanged(ev.Topic)
			return
		case machines.Topic:
			// A machine change can add, move or stop CI work, and the new or
			// edited machine needs a probe. A probe result changes no work,
			// unless it starts or ends a hold on CI.
			if machines.IsHoldKey(ev.Key) {
				wakeWorker(signals.channels["prs"])
				wakeWorker(signals.channels["merges"])
				break
			}
			if machines.IsProbeKey(ev.Key) {
				break
			}
			wakeWorker(signals.channels["prs"])
			wakeWorker(signals.channels["merges"])
			wakeWorker(signals.channels["probe"])
			// A new or enabled CI machine needs its hourly cache cleanup, even
			// when the engine started with no CI machine at all.
			wakeWorker(signals.channels["caches"])
		case jobs.Topic, jobs.AdmissionTopic:
			// Work starting, waiting or ending changes a machine's Work
			// section, so the app reads the machine view again.
			events.Publish(ev)
			events.Publish(hub.Event{Topic: machines.Topic, Key: "work"})
			return
		case prstate.Topic:
			rooms.PRsChanged()
			return
		case sessionlife.Topic:
			// Clients get member changes from the room watch, once each.
			rooms.SessionChanged(ev.Key)
			return
		}
		events.Publish(ev)
	}
}

var errRetryMigration = errors.New("retry engine migration")

// fullAPIReady runs after the private socket swaps to its complete handler.
// Tests use it to observe readiness without polling.
var fullAPIReady = func() {}

// Run owns the engine services until ctx is cancelled.
func Run(ctx context.Context, f Options) error {
	noUpdate := os.Getenv("DYNA_NO_AUTO_UPDATE") == "1"
	allowDevMigration := os.Getenv("DYNA_ALLOW_DEV_MIGRATION") == "1"
	for {
		// run drops DYNA_* before workers start. Restore the owner's recovery
		// choice only for a migration retry, never in a worker environment.
		if allowDevMigration {
			_ = os.Setenv("DYNA_ALLOW_DEV_MIGRATION", "1")
		}
		err := run(ctx, f, noUpdate)
		if !errors.Is(err, errRetryMigration) {
			return err
		}
		if ctx.Err() != nil {
			return nil
		}
	}
}

// startupListeners names the listeners one engine lifetime owns.
type startupListeners struct {
	socket, app, card net.Listener
}

// close releases every claimed listener.
func (l startupListeners) close() {
	l.socket.Close()
	l.app.Close()
	l.card.Close()
}

// claimEngineListeners claims the engine socket, then the app and card
// addresses, and republishes every native scope link at this build only after
// all three are owned. The order matters: a second build whose startup the
// socket rejects must leave the running engine's scope links unchanged, or
// sessions follow the rejected build's CLI against the engine that kept the
// socket (D-161). refresh gates the republish to writable engine lifetimes.
func claimEngineListeners(socketPath, dataDir string, f Options, refresh bool) (startupListeners, error) {
	var l startupListeners
	var err error
	if l.socket, err = listenSocket(socketPath); err != nil {
		return l, err
	}
	if err = writeSocketOwner(socketPath, dataDir); err != nil {
		l.socket.Close()
		return l, err
	}
	defer func() {
		if err != nil {
			l.socket.Close()
		}
	}()
	if l.app, err = net.Listen("tcp", f.Addr); err != nil {
		return l, err
	}
	defer func() {
		if err != nil {
			l.app.Close()
		}
	}()
	if l.card, err = net.Listen("tcp", f.CardAddr); err != nil {
		return l, err
	}
	if refresh {
		// Fail soft: sessions keep the build they run until the next start.
		if err = fsutil.RefreshScopeLinks(); err != nil {
			log.Printf("scope link refresh: %v", err)
			err = nil
		}
	}
	return l, nil
}

// run owns one complete engine lifetime. Recovery drains its read-only server
// before Run starts the writable handlers and workers with current stores.
func run(signalCtx context.Context, f Options, noUpdate bool) (runErr error) {
	ctx, cancelRun, stopSignal := runContext(signalCtx)
	defer stopSignal()
	defer cancelRun()
	// The engine's environment is the owner's, never a caller's, so a check
	// without an actor refuses (requirements item 3). A request acts for its
	// caller, and the engine's own work states that it acts for the owner.
	authority.InEngine()
	requests := ctx
	ctx = authority.WithActor(ctx, authority.Actor{Role: authority.Owner})
	dataDir, ownerData := store.DataDir(), store.OwnerDataDir()
	group, _ := callerid.Cgroup("/proc", os.Getpid())
	if f.Addr == "" {
		f.Addr = defaultServeAddr
	}
	if f.CardAddr == "" {
		f.CardAddr = "127.0.0.1:7458"
	}
	socketPath, err := engineclient.SocketPath()
	if err != nil {
		return err
	}
	if err := checkPrivateRuntime(launchedBySession.Load(), os.Getenv("XDG_RUNTIME_DIR")); err != nil {
		return err
	}
	if err := checkSocketOwner(socketPath, dataDir); err != nil {
		return err
	}
	listeners, err := claimEngineListeners(socketPath, dataDir, f, false)
	if err != nil {
		return err
	}
	defer listeners.close()
	started := time.Now()
	var servers []*http.Server
	defer func() {
		for _, server := range servers {
			_ = server.Close()
		}
	}()
	errs := make(chan error, 3)
	var socketHandler, socketConnContext atomic.Value
	if !f.ReadOnly {
		// Own and serve the private socket before a pre-migration backup. Only
		// Health answers until every store is ready; all other calls still wait.
		socketHandler.Store(http.HandlerFunc(startupHealthHandler(started).ServeHTTP))
		socketConnContext.Store(connContextFunc(func(ctx context.Context, _ net.Conn) context.Context { return ctx }))
		socketServer := &http.Server{
			Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				socketHandler.Load().(http.HandlerFunc).ServeHTTP(w, r)
			}),
			ReadHeaderTimeout: 10 * time.Second,
			BaseContext:       func(net.Listener) context.Context { return requests },
			ConnContext: func(ctx context.Context, conn net.Conn) context.Context {
				return socketConnContext.Load().(connContextFunc)(ctx, conn)
			},
		}
		servers = append(servers, socketServer)
		go func() { errs <- socketServer.Serve(listeners.socket) }()
	}
	readOnly, migrateStatus, connectRefused, migrateCause := startMode(dataDir, f.ReadOnly, group, ownerData)
	if !readOnly {
		go dailyBackups(ctx, store.DataDir())
		go keepCheckpointed(ctx, store.DataDir())
	}

	store, err := devicestore.DefaultStore()
	if err != nil {
		return err
	}
	if !readOnly {
		if err := shells.DefaultStore().Sweep(); err != nil {
			log.Printf("shell owner sweep: %v", err)
		}
	}
	appPort, err := websvc.LoopbackPort(f.Addr)
	if err != nil {
		return err
	}
	cardPort, err := websvc.LoopbackPort(f.CardAddr)
	if err != nil {
		return err
	}
	var kinded []connect.Route
	for _, r := range websvc.LoopbackRoutes(appPort, cardPort) {
		kinded = append(kinded, connect.Route{Kind: "loopback", Route: r})
	}
	for _, spec := range f.Routes {
		r, err := websvc.ParseRoute(spec)
		if err != nil {
			return err
		}
		kinded = append(kinded, connect.Route{Kind: "custom", Route: r})
	}
	ap, _ := strconv.Atoi(appPort)
	cp, _ := strconv.Atoi(cardPort)
	if !f.NoTailscale {
		if tr, ok := connect.TailscaleRoute(ctx, ap, cp); ok && !slices.ContainsFunc(kinded, func(r connect.Route) bool { return r.App == tr.App }) {
			kinded = append(kinded, tr)
		}
	}
	cstore, err := connectStore()
	if err != nil {
		return err
	}
	// Import pre-scope devices before the server accepts a new pairing.
	if _, err := devicesvc.New(subagent.DefaultStore().Root).Devices(); err != nil {
		return err
	}
	link, linked, err := cstore.Link()
	if err != nil {
		return err
	}
	linked = linked && !link.Unlinked
	connectNote := ""
	if why := connectRefused; linked && why != "" {
		// Joining would report this engine's ports to the relay and take the
		// owner's remote access away from the live engine.
		connectNote = "not joining Resident Labs Connect: " + why
		log.Print(connectNote)
		linked = false
	}
	if linked {
		tr := connect.Route{Kind: "tunnel"}
		tr.App, tr.Cards = link.App, link.Cards
		kinded = append(kinded, tr)
	}
	var routes []websvc.Route
	for _, r := range kinded {
		routes = append(routes, r.Route)
	}
	if f.ConnectGrace < 0 || f.ConnectGrace > connect.MaxGrace {
		return fmt.Errorf("--connect-grace must be between 0 and %s", connect.MaxGrace)
	}
	var push *websvc.Push
	if !f.NoPush {
		if push, err = websvc.OpenPush(dataDir, cstore.Audit, log.Printf); err != nil {
			return err
		}
	}
	grace := f.ConnectGrace
	if grace == 0 {
		grace = -1 // New reads 0 as the default; -1 means none
	}
	var csvc *connect.Service
	var ext websvc.Extension
	if linked {
		csvc, err = connect.New(connect.Options{Store: cstore, Routes: kinded, Grace: grace, AccessLife: f.ConnectAccess, SessionsEnded: func() { pruneRelayPush(push, cstore) }, Logf: log.Printf})
		if err != nil {
			return err
		}
		ext = csvc
	}
	shell, err := fs.Sub(web.Assets, "dist")
	if err == nil {
		if _, statErr := fs.Stat(shell, "index.html"); errors.Is(statErr, fs.ErrNotExist) {
			shell, err = fs.Sub(notBuilt, "notbuilt")
		}
	}
	if err != nil {
		return err
	}
	cli, err := os.Executable()
	if err != nil {
		return err
	}
	dropDynaEnv()
	rooms := roomsvc.New(nil)
	files := &filesvc.Service{Rooms: rooms.Notify.Rooms, SessionInRoom: rooms.SessionInRoom, AttachmentBytes: rooms.AttachmentBytes}
	rooms.Notify.Sessions.Executable, err = fsutil.RunningExecutable()
	if err != nil {
		return err
	}
	terminals := terminalsvc.New(terminalOptions(rooms, f.TerminalIdle))
	var workerDone <-chan struct{}
	var background sync.WaitGroup
	startService := func(fn func()) {
		background.Add(1)
		go func() { defer background.Done(); fn() }()
	}
	cleanup := []func(){terminals.Shutdown, background.Wait}
	defer func() {
		if err := stopEngine(cancelRun, workerDone, servers, cleanup...); err != nil && (runErr == nil || errors.Is(runErr, errRetryMigration)) {
			runErr = fmt.Errorf("engine shutdown: %w", err)
		}
	}()
	if !readOnly {
		startService(func() {
			if err := computerrestart.Wait(ctx, computerrestart.Live(userservices.Dir())); err != nil && ctx.Err() == nil {
				log.Printf("deferred computer restart: %v", err)
			}
		})
	}
	runtime := newRuntime(ctx)
	if !readOnly {
		startService(func() { runtime.SplitPiAccounts(ctx) })
	}
	cleanup = append(cleanup, runtime.CancelPrivateLogins)
	config := &configsvc.Service{CLI: cli, NoUpdate: noUpdate, ReadOnly: readOnly, Sessions: rooms.Notify.Sessions}
	accounts := service.New(service.Options{Store: accountstore.DefaultStore()})
	computers := newComputerHost(rooms)
	voiceCalls := &voice.Calls{Ctx: ctx, Open: func(ref string) (*voice.RoomLink, error) { return voice.LinkRoom(rooms.Notify, ref) }}
	cleanup = append(cleanup, voiceCalls.Stop)
	status := migrateStatus
	if readOnly && status == "" {
		status = "read-only mode"
	}
	// The epoch is the engine start: events of one run share it.
	events := hub.New(started.UnixMilli())
	signals := newWorkerSignals(rooms.Notify.Rooms)
	callRing := make(chan struct{}, 1)
	signals.channels["call-requests"] = callRing
	rooms.Notify.Sessions.InputEvents = events
	// writer is the one state write path (internal/state). Stores move onto
	// it one per PR; until then it only reports write health. A read-only
	// engine writes nothing, so it has none.
	var writer *state.Writer
	if !readOnly {
		db, err := database.Open(filepath.Dir(cstore.Dir()))
		if err != nil {
			return err
		}
		writer = state.For(db)
		writer.SetPublish(publishState(events, rooms, signals))
	}
	callers, err := identifyCallers(ctx, dataDir, readOnly, rooms.Notify.Sessions.Store)
	if err != nil {
		return err
	}
	health := engineHealth(status, connectNote, started, writer, events, callers, signals)
	load := sysload.New()
	if !readOnly {
		load.Record = doctor.Samples{DataDir: filepath.Dir(cstore.Dir())}.Recorder(log.Printf)
	}
	// The built-in hooks are seeded before any schedule or caller can run one.
	hookService := hooks.New(automations.Hooks(rooms.Notify, accounts)...)
	if !readOnly {
		if err := hookService.Seed(ctx); err != nil {
			log.Printf("seed built-in hooks: %v", err)
		}
	}
	jobsSvc := newJobsService(rooms)
	calls, callScopes, err := callsFor(services{git: &gitsvc.Service{WorkspaceRoot: files.WorkspaceRoot, MaxBytes: filesvc.MaxWorkspaceBytes}, files: files, devices: devicesvc.New(rooms.Notify.Sessions.Store.Root), rooms: rooms, terminals: terminals, runtime: runtime, accounts: accounts, config: config, computers: computers, voice: voiceCalls, say: voice.NewSayer(), hooks: hookService, sync: lanesync.New(), health: health, load: load, jobs: jobsSvc}, readOnly)
	if err != nil {
		return err
	}
	callers.coordinatorChanged = rooms.CoordinatorChanged
	var runnerSpoolRecovery *runnerSpool
	if writer != nil {
		spool, err := newRunnerSpool(dataDir, func() *subagent.Manager { return rooms.Notify.Sessions }, writer, log.Printf, rooms.Notify)
		if err != nil {
			return err
		}
		runnerSpoolRecovery = spool
		rooms.CoordinatorExited = func(id string) {
			if err := spool.drain(ctx, id); err != nil {
				log.Printf("workflow runner spool of %s: %v", id, err)
			}
		}
		spool.guard(calls)
		rooms.RunnerExited = func(id string) {
			if err := spool.drain(ctx, id); err != nil {
				log.Printf("runner spool of %s: %v", id, err)
				return
			}
			if err := resumeTurnReports(ctx, rooms.Notify, id); err != nil {
				log.Printf("runner completion of %s: %v", id, err)
			}
		}
	}
	callers.actForCallers(calls)
	var agentVoiceHost websvc.AgentVoiceHost
	if !readOnly {
		agentVoiceHost = callers.agentVoice(rooms)
	}
	if writer != nil {
		applyOnce(calls, writer, log.Printf)
	}
	notify := push.Notifier(pushMayRead(cstore))
	// The engine's one timer set: one-shot deadlines in place of clock ticks.
	engineTimers := newTimers(ctx, time.Now, afterFunc)
	retryMigration := make(chan struct{})
	recoverStores(engineTimers, migrateCause, callerid.ProcessOf, func() { close(retryMigration) }, func(cause error) {
		migrationAlert(rooms.Notify.Rooms, cause, func(roomID string) {
			root, open, err := rooms.Notify.Sessions.RootSession(roomID)
			if err != nil {
				log.Printf("dyna engine: find root for read-only alert in room %s: %v", roomID, err)
				return
			}
			if open {
				if err := rooms.Notify.Sessions.TickSessionTargets(ctx, []string{root.ID}); err != nil {
					log.Printf("dyna engine: wake root for read-only alert in room %s: %v", roomID, err)
				}
			}
		})
	})
	if writer != nil {
		pruneRequests(ctx, engineTimers, writer)
	}
	if !readOnly {
		var sender pushNotifier
		if notify != nil {
			sender = notify
		}
		startService(func() { runCallRequests(ctx, rooms, sender, callRing, engineTimers) })
	}
	watch := ctx
	if !readOnly {
		wakeInstructionNotes(rooms.Notify.Sessions, signals)
		// Session workers run on the engine's context, so they stop with it.
		rooms.Notify.Sessions.Workers = subagent.NewWorkers(watch, log.Printf)
		configureTurnReports(watch, rooms.Notify)
		rooms.Notify.Sessions.Workers.Go("completion recovery", func(ctx context.Context) error {
			if err := runnerSpoolRecovery.drainAll(ctx); err != nil {
				return err
			}
			if err := reportUpdateNotices(ctx, writer, rooms.Notify); err != nil {
				return err
			}
			return resumeTurnReports(ctx, rooms.Notify, "")
		})
		rooms.Notify.Sessions.Workers.Go("workflow update notice retry", func(ctx context.Context) error {
			ticker := time.NewTicker(time.Minute)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return nil
				case <-ticker.C:
					if err := reportUpdateNotices(ctx, writer, rooms.Notify); err != nil {
						log.Printf("workflow update notice retry: %v", err)
					}
				}
			}
		})
	}
	workerDone = begin(watch, rooms, accounts, load, readOnly, signals, engineTimers, events, hookService, started, jobsSvc, func(ev roomsvc.Event) {
		if ev.Kind == "call-requests" {
			wakeWorker(callRing)
		}
		events.Publish(hub.Event{Topic: ev.Kind, Room: ev.RoomID, Body: ev})
		if notify != nil && ev.Kind == "posts" {
			go pushPosts(watch, rooms, notify, ev)
		}
	})
	startService(func() {
		computers.Watch(watch, func(ev computerweb.Event) {
			events.Publish(hub.Event{Topic: ev.Kind, Room: ev.RoomID, Key: ev.Session, Body: ev})
		})
	})
	if !readOnly {
		startService(func() { runComputerReminders(ctx, rooms.Notify) })
	}
	srv, err := websvc.New(websvc.Options{
		Callers:    callers.identify,
		Routes:     routes,
		Store:      store,
		Shell:      shell,
		Calls:      calls,
		Files:      files.FilesHandler(),
		Cards:      files.FilesHandlerFor(websvc.FrameAncestors(routes)),
		Hub:        events,
		Terminals:  terminals,
		Computers:  computers,
		Voice:      voiceCalls,
		AgentVoice: agentVoiceHost,
		Ext:        ext,
		Version:    callspec.Build,
		Push:       push,
		CallScopes: callScopes,
	})
	if err != nil {
		return err
	}
	fullServers := httpServers(requests, srv)
	if !f.ReadOnly {
		if !readOnly {
			// Fail soft: sessions keep the build they run until the next start.
			if err := fsutil.RefreshScopeLinks(); err != nil {
				log.Printf("scope link refresh: %v", err)
			}
		}
		// Publish identity before the full handler. Health clients use short-lived
		// connections; every new connection now gets normal caller verification.
		socketConnContext.Store(connContextFunc(srv.UnixConnContext))
		socketHandler.Store(http.HandlerFunc(fullServers[2].Handler.ServeHTTP))
		fullAPIReady()
		servers = append(servers, fullServers[:2]...)
		go func() { errs <- fullServers[0].Serve(listeners.app) }()
		go func() { errs <- fullServers[1].Serve(listeners.card) }()
	} else {
		servers = append(servers, fullServers...)
		go func() { errs <- fullServers[0].Serve(listeners.app) }()
		go func() { errs <- fullServers[1].Serve(listeners.card) }()
		go func() { errs <- fullServers[2].Serve(listeners.socket) }()
	}
	if !readOnly {
		startService(func() { maintenance.Loop(ctx, log.Printf, rooms.Notify.Sessions.RetainClosedSession) })
	}

	fmt.Printf("Dyna web app on http://%s (cards on http://%s)\n", f.Addr, f.CardAddr)
	for _, r := range routes[2:] {
		fmt.Printf("  route %s (cards %s)\n", r.App, r.Cards)
	}
	if status != "" {
		fmt.Printf("  note: %s\n", status)
	}
	if printCode(store, nil) != 0 {
		return errors.New("could not make a pairing code")
	}
	if linked {
		fmt.Printf("Resident Labs Connect: %s (%s), owner %s\n  open %s and sign in with your passkey\n", link.Name, link.Machine, link.Owner.Name, link.App)
		startService(func() {
			// Retry until the relay has the ports and routes and the tunnel
			// token is here. A relay that is down at start must not leave
			// the routes unreported.
			wait := 30 * time.Second
			for {
				err := csvc.Sync(ctx, ap, cp)
				if err == nil || errors.Is(err, connect.ErrUnlinked) {
					if err != nil {
						log.Printf("connect: %v", err)
					}
					return
				}
				log.Printf("connect: %v (trying again in %s)", err, wait)
				select {
				case <-ctx.Done():
					return
				case <-time.After(wait):
				}
				wait = min(wait*2, 10*time.Minute)
			}
		})
		if !f.NoTunnel && !userservices.TunnelActive(ctx) {
			startService(func() { connect.RunTunnel(ctx, cstore, f.Cloudflared, log.Printf) })
		} else if !f.NoTunnel {
			log.Printf("connect: the tunnel runs as %s, so it stays up when this engine restarts", userservices.Tunnel)
		}
	} else {
		fmt.Println("For a phone, see docs/desktop/WEB.md (tailscale serve, a cloudflared tunnel) or docs/desktop/CONNECT.md (dyna engine connect link).")
	}
	startService(func() {
		// Drop push subscriptions of paired devices that were revoked.
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				push.Prune(func(id string) bool {
					list, err := store.Devices()
					return err != nil || slices.ContainsFunc(list, func(d devicestore.Device) bool { return d.ID == id })
				}, nil)
				pruneRelayPush(push, cstore)
			}
		}
	})

	select {
	case <-ctx.Done():
	case <-retryMigration:
		return errRetryMigration
	case err := <-errs:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	return nil
}

// pushMayRead lets a subscription receive room content only when its grant
// includes read. A missing row or a store error refuses the device.
func pushMayRead(cstore *connect.Store) func(string) bool {
	dataDir := filepath.Dir(cstore.Dir())
	return func(dev string) bool {
		list, err := cstore.Sessions()
		if err != nil {
			return websvc.PushAllowsRead(dataDir, dev, nil)
		}
		sessions := make([]websvc.PushSession, len(list))
		for i, sess := range list {
			sessions[i] = websvc.PushSession{ID: sess.ID, Device: sess.Device}
		}
		return websvc.PushAllowsRead(dataDir, dev, sessions)
	}
}

// pushNotifier sends one push event; *websvc.Notifier is the real one.
type pushNotifier interface {
	Notify(context.Context, websvc.Event)
}

// pushPosts sends a push for each post that asks for the owner: a post to
// the owner from someone else, a turn completion, or a failed turn or
// workflow run. Questions and failures always send, including a worker or
// subagent failure and a run whose first target is its parent session. At
// most one ordinary post goes out per burst. The notifier lets ordinary
// posts and completions share one push per room per 20 seconds. None for
// the owner's own posts, and none for the voice call: the owner is on the
// call, and Voice's requests to root are not for the owner.
func pushPosts(ctx context.Context, rooms *roomsvc.Service, n pushNotifier, ev roomsvc.Event) {
	name := ""
	if rooms, err := rooms.Rooms(); err == nil {
		for _, r := range rooms {
			if r.ID == ev.RoomID {
				name = r.Name
			}
		}
	}
	sentPost := false
	for _, p := range ev.Posts {
		kind := pushKind(p, name)
		if kind == "" {
			continue
		}
		if kind == "post" {
			if sentPost {
				continue
			}
			sentPost = true
		}
		n.Notify(ctx, websvc.Event{Kind: kind, Room: ev.RoomID, Post: p.ID, RoomNm: name, From: p.Speaker, Text: p.Text})
	}
}

// pushKind is the push a post should send. Empty means do not send.
func pushKind(p roomsvc.Post, roomName string) string {
	if p.Owner || p.Human || roomsvc.IsShellResult(p.Message) || p.Source == room.SourceVoice {
		return ""
	}
	switch {
	case p.QuestionID() != "" && roomsvc.ForOwner(p.Message, roomName):
		return "question"
	case p.Kind == "failed":
		return "failure"
	case p.Completion:
		return "turn"
	case roomsvc.ForOwner(p.Message, roomName):
		return "post"
	default:
		return ""
	}
}

// dropDynaEnv makes this process the human room owner: a Dyna stamp
// inherited from a launching session must not change who posts.
func dropDynaEnv() {
	// Remember the launch stamp before it is gone. A later bind check must know
	// whether a Dyna session started this engine, because such an engine must not
	// take the owner's socket.
	_, stamped := mode.StampFromEnv()
	launchedBySession.Store(stamped)
	for _, kv := range os.Environ() {
		if k, _, _ := strings.Cut(kv, "="); strings.HasPrefix(k, "DYNA_") {
			os.Unsetenv(k)
		}
	}
}

// pruneRelayPush preserves subscriptions if the session store is unavailable.
func pruneRelayPush(push *websvc.Push, store *connect.Store) {
	if push == nil {
		return
	}
	list, err := store.Sessions()
	if err != nil {
		return
	}
	live := map[string]bool{}
	for _, s := range list {
		live[s.Device] = true
	}
	push.Prune(nil, func(id string) bool { return live[id] })
}

// httpServers keeps the public listeners separate from the private socket.
// Never use DefaultServeMux here: net/http/pprof registers profiles on it.
func httpServers(ctx context.Context, srv *websvc.Server) []*http.Server {
	base := func(net.Listener) context.Context { return ctx }
	// Only a header timeout: the event stream and a 60 s stop must live.
	return []*http.Server{
		{Handler: srv.AppHandler(), ReadHeaderTimeout: 10 * time.Second, BaseContext: base},
		{Handler: srv.CardHandler(), ReadHeaderTimeout: 10 * time.Second, BaseContext: base},
		{Handler: srv.UnixHandler(), ReadHeaderTimeout: 10 * time.Second, BaseContext: base, ConnContext: srv.UnixConnContext},
	}
}
