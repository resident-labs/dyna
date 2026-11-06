package runtime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"runtime/debug"
	"slices"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/box/podman"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine"
	"github.com/resident-labs/dyna/engine/api"
	"github.com/resident-labs/dyna/engine/approve"
	"github.com/resident-labs/dyna/engine/ci"
	"github.com/resident-labs/dyna/engine/compose"
	"github.com/resident-labs/dyna/engine/connect"
	enginegit "github.com/resident-labs/dyna/engine/git"
	"github.com/resident-labs/dyna/engine/githubmcp"
	"github.com/resident-labs/dyna/engine/images"
	"github.com/resident-labs/dyna/engine/lifecycle"
	"github.com/resident-labs/dyna/engine/model"
	"github.com/resident-labs/dyna/engine/prs"
	"github.com/resident-labs/dyna/engine/store"
	"github.com/resident-labs/dyna/web"
)

type hostRegistrar interface {
	AddHost(context.Context, contract.HostAddRequest) (bool, error)
	HostLoad(context.Context, []contract.HostSummary) error
}

type serviceWithHosts struct {
	*engine.Service
	boxes  hostRegistrar
	quotas *model.Quotas
	*engine.AccountActions
}

func (s *serviceWithHosts) AccountsQuota(ctx context.Context, caller contract.Principal, _ contract.AccountsQuotaRequest) (contract.AccountsQuotaResponse, *contract.Error) {
	result, err := s.quotas.Read(ctx)
	if err != nil {
		return result, &contract.Error{Code: contract.ErrUnavailable, Message: "account usage unavailable"}
	}
	return result, nil
}

func (s *serviceWithHosts) HostList(ctx context.Context, caller contract.Principal, req contract.HostListRequest) (contract.HostListResponse, *contract.Error) {
	result, problem := s.Service.HostList(ctx, caller, req)
	if problem != nil {
		return result, problem
	}
	if err := s.boxes.HostLoad(ctx, result.Hosts); err != nil {
		return contract.HostListResponse{}, &contract.Error{Code: contract.ErrUnavailable, Message: err.Error()}
	}
	return result, nil
}

func (s *serviceWithHosts) HostAdd(ctx context.Context, caller contract.Principal, req contract.HostAddRequest) (contract.HostAddResponse, *contract.Error) {
	// The name reaches prompts and file names, and the address reaches ssh, so both are checked here and nowhere inward.
	if !hostNamePattern.MatchString(string(req.Name)) || !sshPattern.MatchString(req.SSH) {
		return contract.HostAddResponse{}, &contract.Error{Code: contract.ErrInvalid, Message: "a host name is letters, digits, hyphens and underscores, and its ssh is user@host"}
	}
	if !req.OwnerOnlyAdmin && !req.ExecOnly() {
		return contract.HostAddResponse{}, &contract.Error{Code: contract.ErrInvalid, Message: "--owner-only-admin is required: it confirms that only the owner administers the machine, which a host that runs boxes or CI runners needs; a host added with --agents off --ci off (exec only) does not"}
	}
	for i, room := range req.Rooms {
		if room == "" || slices.Contains(req.Rooms[:i], room) {
			return contract.HostAddResponse{}, &contract.Error{Code: contract.ErrInvalid, Message: "room IDs must be nonempty and unique"}
		}
	}
	noSwap, err := s.boxes.AddHost(ctx, req)
	if err != nil {
		var required podman.RequiredError
		if errors.As(err, &required) {
			return contract.HostAddResponse{}, &contract.Error{Code: contract.ErrPodmanRequired, Message: err.Error()}
		}
		if errors.Is(err, store.ErrRoomDeleting) {
			return contract.HostAddResponse{}, &contract.Error{Code: contract.ErrConflict, Message: err.Error()}
		}
		return contract.HostAddResponse{}, &contract.Error{Code: contract.ErrUnavailable, Message: err.Error()}
	}
	return contract.HostAddResponse{NoSwap: noSwap}, nil
}

type Config struct {
	Data, Runtime, HostData, Profiles, Version string
	ApprovalsExpiry                            time.Duration
	SessionMaxDepth                            int
	WebListen                                  string
	WebRoutes                                  []string
	GitName, GitEmail                          string
}

func Run(ctx context.Context, cfg Config) error {
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	shutdown := engineShutdown{stop: stop}
	defer shutdown.run()
	token := os.Getenv("GH_TOKEN")
	if err := os.Unsetenv("GH_TOKEN"); err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	st, err := store.Open(cfg.Data)
	if err != nil {
		return err
	}
	shutdown.cleanup("store", func() { _ = st.Close() })
	layout := box.Layout{Data: cfg.Data, HostData: cfg.HostData}
	if err := st.ResetEngineHosts(ctx); err != nil {
		return fmt.Errorf("reset host identity: %w", err)
	}
	profiles, err := engine.LoadProfiles(cfg.Profiles)
	if err != nil {
		return err
	}
	var key [32]byte
	if _, err := rand.Read(key[:]); err != nil {
		return err
	}
	managementKey := hex.EncodeToString(key[:])
	proxy, err := model.NewProxy(layout.Proxy(), layout.ProxyAuth(), managementKey)
	if err != nil {
		return err
	}
	shutdown.cleanup("proxy listener", func() { _ = proxy.Listener.Close() })
	proxyCtx, stopProxy := context.WithCancel(context.WithoutCancel(ctx))
	shutdown.cleanup("proxy context", stopProxy)
	done := make(chan struct{})
	drain := model.NewDrain()
	boxCtx, stopBoxes := context.WithCancel(ctx)
	defer stopBoxes()
	boxes := &Boxes{Store: st, HeartbeatStore: st, Runtime: cfg.Runtime, Layout: layout, Version: cfg.Version, Context: boxCtx}
	service := engine.New(st, boxes, profiles, time.Now)
	service.SetLayout(layout)
	binary, err := boxes.binarySum()
	if err != nil {
		return fmt.Errorf("hash binary: %w", err)
	}
	service.SetBinary(binary)
	service.SetMaxDepth(cfg.SessionMaxDepth)
	service.Reconciler().SetHostMemory(boxes.hostMemory)
	service.Reconciler().SetHostHealth(boxes.HostHealth)
	service.SetStartStatus(boxes.StartStatus)
	service.Reconciler().SetHostRestoring(boxes.restoring)
	service.Reconciler().SetRoomHosts(boxes)
	service.Reconciler().SetServices(boxes, lifecycle.DefaultServiceTimings)
	service.Reconciler().SetNodes(poolHosts{Script: lifecycle.Script{Layout: layout}, boxes: boxes})
	runners := &ci.Pool{Store: st, Hosts: boxes.ciDriver, Sets: &ci.Sets{Token: token}, Memory: boxes.hostMemory, Health: boxes.HostHealth, Now: time.Now, Kick: service.Reconciler().Kick, LayerID: service.Images().RunnerLayerID, Layer: service.Images().RunnerLayer,
		GitHub: &ci.GitHub{Token: token, Endpoint: &url.URL{Scheme: "https", Host: "api.github.com"}, Transport: http.DefaultTransport, Now: time.Now}}
	service.Reconciler().SetRunners(runners)
	service.SetRunnerImageProblems(runners.Problems)
	service.SetGitIdentity(cfg.GitName, cfg.GitEmail)
	service.SetHostexecRunner(boxes)
	service.SetHostPromoter(boxes.PromoteHost)
	service.SetHookRunner(boxes, boxes.hostMemory)
	boxes.Pressure = service
	go boxes.controlPlane(cfg.Data)
	go boxes.measureShared(boxCtx)
	go boxes.serveShare(boxCtx, executable)
	hosts, err := st.Hosts(ctx)
	if err != nil {
		return fmt.Errorf("list hosts: %w", err)
	}
	for _, host := range hosts {
		if _, err := boxes.host(ctx, host.Name); err != nil {
			log.Printf("host %s: start heartbeat: %v", host.Name, err)
		}
	}
	service.SetInstructionsPublisher(boxes.PublishInstructions)
	boxes.Addons = service.Addons()
	go boxes.watchInstructions(boxCtx)
	mirror := &enginegit.Mirror{Layout: layout, Token: token, Runner: enginegit.GitRunner{}}
	service.SetRepositories(&enginegit.Repositories{Rooms: st, Hosts: boxes, Mirror: mirror})
	pulls := &prs.Client{Token: token, Endpoint: &url.URL{Scheme: "https", Host: "api.github.com"}, Transport: http.DefaultTransport, Now: time.Now}
	prPoll := &prs.Poller{Store: st, GitHub: pulls, Now: time.Now}
	service.SetPRTracking(pulls)
	if token != "" {
		// Agents' GitHub reads spend the feeds' quota through pulls, which keeps the feeds' share.
		// Without them boxes list no github server, and the engine still starts.
		if tools, err := githubmcp.Handler(ctx, token, pulls.Reads(http.DefaultTransport)); err != nil {
			log.Printf("GitHub tools for boxes: %v", err)
		} else {
			service.SetGitHubTools(tools)
		}
	}
	boxes.RoomImages = service.Images()
	boxes.Migrator = service.SetImageMigration(boxCtx, mirror)
	boxes.Compose = &compose.Files{Layout: layout, Repo: mirror, Images: service.Images()}
	service.SetCompose(boxes.Compose)
	quotas := proxy.Quotas(managementKey, st, time.Now)
	service.SetWebSearchProxy(proxy, quotas)
	service.SetUsage(quotas)
	connections := proxy.Connections(managementKey, st, time.Now)
	accounts := engine.NewAccountActions(service, connections, quotas)
	if err := service.ConfigureOperations(ctx, map[contract.OpKind]approve.Entry{"accounts.action": accounts.Entry(), "addon.global": service.AddonGlobal(), "pr.open": service.PrOpen(token, &url.URL{Scheme: "https", Host: "api.github.com"}, http.DefaultTransport), "hostexec.grant": service.HostexecGrant(), "hostexec.run": service.HostexecRunEntry(), "pool.add": service.PoolAddEntry(), "room.hosts": service.RoomHostsEntry()}, cfg.ApprovalsExpiry); err != nil {
		return err
	}
	go func() {
		defer close(done)
		proxy.Supervise(proxyCtx, executable, st, accounts.CompleteOAuth, connections)
	}()
	shutdown.cleanup("model proxy", func() { stopProxy(); <-done })
	boxes.Mirror = mirror
	boxes.Images = &images.Builder{Recorder: service, Now: time.Now}
	apiServer := api.New(&serviceWithHosts{Service: service, boxes: boxes, quotas: quotas, AccountActions: accounts}, func(c api.Caller) http.Handler {
		return drain.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			session, err := st.Session(r.Context(), c.Principal.Session)
			if err != nil {
				http.Error(w, "session unavailable", http.StatusServiceUnavailable)
				return
			}
			allowed, crew, err := service.AllowedModels(r.Context(), session)
			if err != nil {
				http.Error(w, "profile unavailable", http.StatusServiceUnavailable)
				return
			}
			eligible, err := st.EligibleAccountProxyIDs(r.Context(), session.Room)
			if err != nil {
				http.Error(w, "account policy unavailable", http.StatusServiceUnavailable)
				return
			}
			// A choice that cannot be read is Automatic: it must never fail the turn.
			pinned, err := st.SessionPinnedProxyID(r.Context(), session.ID)
			if err != nil {
				pinned = ""
			}
			proxy.Handler(string(session.ID), allowed, eligible, pinned, crew).ServeHTTP(w, r)
		}))
	})
	apiServer.SetLocalModelRoute(drain.Wrap(proxy.Local(st)))
	apiServer.Build = cfg.Version
	if cfg.Version == "dev" {
		if info, ok := debug.ReadBuildInfo(); ok {
			for _, setting := range info.Settings {
				if setting.Key == "vcs.revision" {
					apiServer.Build = setting.Value
				}
			}
		}
	}
	apiServer.SetGitHubRead(enginegit.ReadOnly(&url.URL{Scheme: "https", Host: "api.github.com"}, http.DefaultTransport, token))
	apiServer.SetFilesRoute(func(c api.Caller) http.Handler { return service.Files(c.Principal, c.Room) })
	apiServer.SetGitRoute(func(c api.Caller) http.Handler {
		return enginegit.Handler(&url.URL{Scheme: "https", Host: "github.com"}, http.DefaultTransport, token, c.Principal.Session, c.Room, st)
	})
	files, err := connect.OpenFiles(cfg.Data)
	if err != nil {
		return err
	}
	sessions := connect.NewSessions(files, st, time.Now, nil)
	card, cardServer, err := connect.CardListener()
	if err != nil {
		return err
	}
	go cardServer.Serve(card)
	shutdown.cleanup("connect card", func() { _ = card.Close() })
	appPort := 0
	webRoutes := func(context.Context) []string { return nil }
	var webServer *api.Web
	if cfg.WebListen != "" {
		app, err := fs.Sub(web.Dist, "dist")
		if err != nil {
			return err
		}
		webServer, err = api.NewWeb(apiServer, cfg.WebListen, app, time.Now, st)
		if err != nil {
			return err
		}
		webServer.SetConnect(sessions)
		if err := webServer.SetRoutes(cfg.WebRoutes); err != nil {
			return err
		}
		listener, err := webServer.Listen()
		if err != nil {
			return err
		}
		shutdown.cleanup("web listener", func() { _ = listener.Close() })
		appPort = listener.Addr().(*net.TCPAddr).Port
	}
	connectOps := connect.NewOperations(ctx, files, st, appPort, card.Addr().(*net.TCPAddr).Port)
	shutdown.cleanup("connect operations", connectOps.Close)
	if webServer != nil {
		webServer.SetDiscovery(connect.TailscaleRoute, func() {
			select {
			case connectOps.Wake() <- struct{}{}:
			default:
			}
		})
		webRoutes = webServer.RefreshRoutes
	}

	if err := connectOps.Resume(ctx); err != nil {
		return err
	}
	apiServer.SetConnectOperations(connectOps)
	tunnelCtx, stopTunnel := context.WithCancel(ctx)
	tunnelDone := make(chan struct{})
	go func() {
		defer close(tunnelDone)
		connect.SuperviseTunnel(tunnelCtx, files, st, appPort, card.Addr().(*net.TCPAddr).Port, webRoutes, connectOps.Wake())
	}()
	shutdown.cleanup("connect tunnel", func() { stopTunnel(); <-tunnelDone })
	sockets := api.NewSockets(apiServer, cfg.Runtime, uint32(os.Getuid()), nil)
	boxes.Sockets = sockets
	if err := sockets.OpenOwner(); err != nil {
		return err
	}
	shutdown.cleanup("sockets", func() { _ = sockets.Close() })
	if err := service.Reconciler().FailStaleCreates(ctx); err != nil {
		return err
	}
	if err := boxes.Restore(ctx); err != nil {
		return err
	}
	shutdown.workers = append(shutdown.workers, shutdownStep{name: "host recovery", run: boxes.WaitRecovered})

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	reconcilerTicks := time.NewTicker(10 * time.Second)
	defer reconcilerTicks.Stop()
	reconcilerDone := make(chan struct{})
	go func() {
		defer close(reconcilerDone)
		service.Reconciler().Run(ctx, reconcilerTicks.C)
		service.Reconciler().Wait()
	}()
	shutdown.join("reconciler", reconcilerDone)
	subscriptionsDone := make(chan struct{})
	go func() {
		defer close(subscriptionsDone)
		service.RunSubscriptions(ctx)
	}()
	shutdown.join("subscriptions", subscriptionsDone)
	buildDone := make(chan struct{})
	go func() {
		defer close(buildDone)
		boxes.BuildAhead(ctx)
	}()
	shutdown.join("image builds", buildDone)
	replayDone := make(chan struct{})
	go func() {
		defer close(replayDone)
		ticks := time.NewTicker(time.Hour)
		defer ticks.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticks.C:
				passCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
				err := service.ExpireReplayKeys(passCtx)
				cancel()
				if err != nil && ctx.Err() == nil {
					log.Printf("replay key sweep: %v", err)
				}
			}
		}
	}()
	shutdown.join("replay key expiry", replayDone)
	ciDone := make(chan struct{})
	go func() {
		defer close(ciDone)
		ticks := time.NewTicker(30 * time.Second)
		defer ticks.Stop()
		runners.Run(ctx, ticks.C)
	}()
	shutdown.join("CI pool", ciDone)
	prDone := make(chan struct{})
	go func() {
		defer close(prDone)
		ticks := time.NewTicker(prs.PollEvery)
		defer ticks.Stop()
		prPoll.Run(ctx, ticks.C)
	}()
	shutdown.join("PR poller", prDone)
	lastDormant, lastCheckins := time.Now(), time.Now()
	for {
		select {
		case <-ctx.Done():
			st.DrainDispatch()
			waitForDrain(drain.Begin(), time.After(60*time.Second))
			return nil
		case now := <-ticker.C:
			if !now.Before(lastDormant.Add(time.Hour)) {
				if err := service.CloseDormant(ctx); err != nil {
					log.Printf("dormant close sweep: %v", err)
				}
				lastDormant = now
			}
			if err := service.FailureNotices(ctx); err != nil {
				log.Printf("failure notices: %v", err)
			}
			if !now.Before(lastCheckins.Add(time.Minute)) {
				if err := service.Checkins(ctx); err != nil {
					log.Printf("check-in pass: %v", err)
				}
				lastCheckins = now
			}
			if err := service.EvictDisk(ctx); err != nil {
				log.Printf("disk eviction: %v", err)
			}
			if err := service.ExpireOperations(ctx); err != nil {
				log.Printf("operation expiry sweep: %v", err)
			}
			if err := service.StopHostexecs(ctx); err != nil {
				log.Printf("hostexec stop sweep: %v", err)
			}
		}
	}
}

var (
	hostNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)
	sshPattern      = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*@[A-Za-z0-9][A-Za-z0-9_.-]*$`)
)
