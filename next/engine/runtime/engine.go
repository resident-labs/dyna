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
	"path/filepath"
	"runtime/debug"
	"slices"
	"strings"
	"time"

	"filippo.io/age"
	"github.com/resident-labs/dyna/box/podman"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine"
	"github.com/resident-labs/dyna/engine/api"
	"github.com/resident-labs/dyna/engine/approve"
	"github.com/resident-labs/dyna/engine/connect"
	enginegit "github.com/resident-labs/dyna/engine/git"
	"github.com/resident-labs/dyna/engine/images"
	"github.com/resident-labs/dyna/engine/model"
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
	backupEnabled bool
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
	result.Hosts[0].BackupCopyEnabled = s.backupEnabled
	return result, nil
}

func (s *serviceWithHosts) HostAdd(ctx context.Context, caller contract.Principal, req contract.HostAddRequest) (contract.HostAddResponse, *contract.Error) {
	if req.Name == "" || !req.OwnerOnlyAdmin || strings.Count(req.SSH, "@") != 1 || strings.HasPrefix(req.SSH, "@") || strings.HasSuffix(req.SSH, "@") {
		return contract.HostAddResponse{}, &contract.Error{Code: contract.ErrInvalid, Message: "name, user@host, and owner-only administration are required"}
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
		return contract.HostAddResponse{}, &contract.Error{Code: contract.ErrUnavailable, Message: err.Error()}
	}
	return contract.HostAddResponse{NoSwap: noSwap}, nil
}

func loadPi(executable string) ([]byte, error) {
	path := filepath.Join(filepath.Dir(executable), "pi.tgz")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read Pi package %s: %w", path, err)
	}
	return data, nil
}

type Config struct {
	Data, Runtime, HostData, Profiles, Version string
	ApprovalsExpiry                            time.Duration
	SessionMaxDepth                            int
	WebListen                                  string
	WebRoutes                                  []string
	BackupDest, BackupRecipient                string
	GitName, GitEmail                          string
}

func Run(ctx context.Context, cfg Config) error {
	if (cfg.BackupDest == "") != (cfg.BackupRecipient == "") {
		return fmt.Errorf("backup.dest and backup.recipient must be set together")
	}
	var recipient age.Recipient
	if cfg.BackupRecipient != "" {
		parsed, err := age.ParseX25519Recipient(cfg.BackupRecipient)
		if err != nil {
			return fmt.Errorf("backup.recipient: %w", err)
		}
		recipient = parsed
	}
	token := os.Getenv("GH_TOKEN")
	if err := os.Unsetenv("GH_TOKEN"); err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	pi, err := loadPi(executable)
	if err != nil {
		return err
	}
	catalog, err := model.LoadCatalog(pi)
	if err != nil {
		return err
	}
	st, err := store.Open(cfg.Data)
	if err != nil {
		return err
	}
	defer st.Close()
	backupCtx, stopBackup := context.WithCancel(ctx)
	backupDone := make(chan struct{})
	go func() {
		defer close(backupDone)
		(&backupLoop{store: st, dir: filepath.Join(cfg.Data, "backups"), dest: cfg.BackupDest, recipient: recipient, now: time.Now, run: sshBackup}).start(backupCtx)
	}()
	defer func() { stopBackup(); <-backupDone }()
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
	proxy, err := model.NewProxy(cfg.Data, managementKey, catalog)
	if err != nil {
		return err
	}
	defer proxy.Listener.Close()
	proxyCtx, stopProxy := context.WithCancel(context.WithoutCancel(ctx))
	defer stopProxy()
	done := make(chan struct{})
	drain := model.NewDrain()
	boxCtx, stopBoxes := context.WithCancel(ctx)
	defer stopBoxes()
	boxes := &Boxes{Store: st, HeartbeatStore: st, Runtime: cfg.Runtime, HostData: cfg.HostData, InstructionsDir: filepath.Join(cfg.Data, "files", "instructions"), Version: cfg.Version, PiTarball: pi, Context: boxCtx}
	go boxes.controlPlane(cfg.Data)
	service := engine.New(st, boxes, profiles, time.Now)
	routes := make(map[contract.ModelProvider]string)
	for _, provider := range proxy.AccountProviders() {
		suffix := "/v1"
		if provider.ID == contract.ProviderAnthropic {
			suffix = ""
		}
		routes[contract.ModelProvider(provider.ID)] = suffix
	}
	service.SetModelRoutes(routes)
	service.SetModelNamespace(proxy.Namespace)
	service.SetMaxDepth(cfg.SessionMaxDepth)
	service.Reconciler().SetHostMemory(boxes.hostMemory)
	service.SetGitIdentity(cfg.GitName, cfg.GitEmail)
	service.SetHostexecRunner(boxes)
	boxes.Pressure = service
	hosts, err := st.Hosts(ctx)
	if err != nil {
		return fmt.Errorf("list hosts: %w", err)
	}
	for _, host := range hosts {
		if _, err := boxes.host(ctx, host.Name); err != nil {
			log.Printf("host %s: start heartbeat: %v", host.Name, err)
		}
	}
	service.SetTranscriptDir(filepath.Join(cfg.Data, "transcripts"))
	service.SetFilesDir(filepath.Join(cfg.Data, "files"))
	service.SetInstructionsPublisher(boxes.PublishInstructions)
	mirror := &enginegit.Mirror{Data: cfg.Data, Token: token, Runner: enginegit.GitRunner{}}
	service.SetRepositories(&enginegit.Repositories{Rooms: st, Hosts: boxes, Mirror: mirror})
	if err := st.RecoverHostexec(ctx, time.Now()); err != nil {
		return fmt.Errorf("recover hostexec runs: %w", err)
	}
	quotas := proxy.Quotas(managementKey, st, time.Now)
	connections := proxy.Connections(managementKey, st, time.Now)
	accounts := engine.NewAccountActions(service, connections, quotas)
	if err := service.ConfigureOperations(ctx, map[contract.OpKind]approve.Entry{"accounts.action": accounts.Entry(), "pr.open": service.PrOpen(token, &url.URL{Scheme: "https", Host: "api.github.com"}, http.DefaultTransport), "hostexec.grant": service.HostexecGrant(), "hostexec.run": service.HostexecRunEntry()}, cfg.ApprovalsExpiry); err != nil {
		return err
	}
	go func() {
		defer close(done)
		proxy.Supervise(proxyCtx, executable, st, accounts.CompleteOAuth, connections)
	}()
	defer func() { stopProxy(); <-done }()
	if err := service.ExpireHostexecGrantRequests(ctx); err != nil {
		return fmt.Errorf("expire host grant requests: %w", err)
	}
	boxes.Mirror = mirror
	boxes.Images = &images.Builder{Recorder: service, Now: time.Now}
	apiServer := api.New(&serviceWithHosts{Service: service, boxes: boxes, quotas: quotas, AccountActions: accounts, backupEnabled: recipient != nil}, func(c api.Caller) http.Handler {
		return drain.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			session, err := st.Session(r.Context(), c.Principal.Session)
			if err != nil {
				http.Error(w, "session unavailable", http.StatusServiceUnavailable)
				return
			}
			choice, allowed, err := profiles.Model(session.Profile)
			if err != nil {
				http.Error(w, "profile unavailable", http.StatusServiceUnavailable)
				return
			}
			eligible, err := st.EligibleAccountProxyIDs(r.Context(), session.Room)
			if err != nil {
				http.Error(w, "account policy unavailable", http.StatusServiceUnavailable)
				return
			}
			namespace, err := proxy.Namespace(choice, allowed)
			if err != nil {
				http.Error(w, "profile unavailable", http.StatusForbidden)
				return
			}
			proxy.Handler(string(session.ID), namespace, allowed, eligible).ServeHTTP(w, r)
		}))
	})
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
	defer card.Close()
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
		defer listener.Close()
		appPort = listener.Addr().(*net.TCPAddr).Port
	}
	connectOps := connect.NewOperations(ctx, files, st, appPort, card.Addr().(*net.TCPAddr).Port)
	defer connectOps.Close()
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
	defer func() { stopTunnel(); <-tunnelDone }()
	sockets := api.NewSockets(apiServer, cfg.Runtime, uint32(os.Getuid()), nil)
	boxes.Sockets = sockets
	if err := sockets.OpenOwner(); err != nil {
		return err
	}
	defer sockets.Close()
	if err := boxes.Restore(ctx); err != nil {
		return err
	}

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	reconcilerTicks := time.NewTicker(10 * time.Second)
	defer reconcilerTicks.Stop()
	reconcilerDone := make(chan struct{})
	go func() {
		defer close(reconcilerDone)
		service.Reconciler().Run(ctx, reconcilerTicks.C)
	}()
	defer func() {
		<-reconcilerDone
		service.Reconciler().Wait()
	}()
	archiveDone := make(chan struct{})
	go func() {
		defer close(archiveDone)
		ticks := time.NewTicker(time.Hour)
		defer ticks.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticks.C:
				passCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
				err := service.ExpireArchives(passCtx)
				cancel()
				if err != nil && ctx.Err() == nil {
					log.Printf("archive expiry sweep: %v", err)
				}
			}
		}
	}()
	defer func() { <-archiveDone }()
	lastDormant := time.Now()
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
			if err := service.ExpireOperations(ctx); err != nil {
				log.Printf("operation expiry sweep: %v", err)
			}
			if err := service.ExpireHostexecGrants(ctx); err != nil {
				log.Printf("grant expiry sweep: %v", err)
			}
			if err := service.StopHostexecs(ctx); err != nil {
				log.Printf("hostexec stop sweep: %v", err)
			}
		}
	}
}
