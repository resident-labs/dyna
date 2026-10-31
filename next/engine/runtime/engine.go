package runtime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"filippo.io/age"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine"
	"github.com/resident-labs/dyna/engine/api"
	"github.com/resident-labs/dyna/engine/approve"
	enginegit "github.com/resident-labs/dyna/engine/git"
	"github.com/resident-labs/dyna/engine/images"
	"github.com/resident-labs/dyna/engine/model"
	"github.com/resident-labs/dyna/engine/store"
	"github.com/resident-labs/dyna/web"
)

type hostRegistrar interface {
	AddHost(context.Context, contract.HostAddRequest) error
	HostLoad(context.Context, []contract.HostSummary) error
}

type serviceWithHosts struct {
	*engine.Service
	boxes         hostRegistrar
	quotas        *model.Quotas
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
	if err := s.boxes.AddHost(ctx, req); err != nil {
		return contract.HostAddResponse{}, &contract.Error{Code: contract.ErrUnavailable, Message: err.Error()}
	}
	return contract.HostAddResponse{}, nil
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
	WebListen                                  string
	BackupDest, BackupRecipient                string
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
	proxy, err := model.NewProxy(cfg.Data, managementKey)
	if err != nil {
		return err
	}
	defer proxy.Listener.Close()
	proxyCtx, stopProxy := context.WithCancel(context.WithoutCancel(ctx))
	done := make(chan struct{})
	go func() { defer close(done); proxy.Supervise(proxyCtx, executable) }()
	defer func() { stopProxy(); <-done }()
	drain := model.NewDrain()
	boxCtx, stopBoxes := context.WithCancel(ctx)
	defer stopBoxes()
	boxes := &Boxes{Store: st, HeartbeatStore: st, Runtime: cfg.Runtime, HostData: cfg.HostData, Version: cfg.Version, PiTarball: pi, Context: boxCtx}
	go boxes.controlPlane(cfg.Data)
	service := engine.New(st, boxes, profiles, time.Now)
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
	mirror := &enginegit.Mirror{Data: cfg.Data, Token: token, Runner: enginegit.GitRunner{}}
	service.SetRepositories(&enginegit.Repositories{Rooms: st, Hosts: boxes, Mirror: mirror})
	if err := st.RecoverHostexec(ctx, time.Now()); err != nil {
		return fmt.Errorf("recover hostexec runs: %w", err)
	}
	if err := service.ConfigureOperations(ctx, map[contract.OpKind]approve.Entry{"pr.open": service.PrOpen(token, &url.URL{Scheme: "https", Host: "api.github.com"}, http.DefaultTransport), "hostexec.grant": service.HostexecGrant(), "hostexec.run": service.HostexecRunEntry()}, cfg.ApprovalsExpiry); err != nil {
		return err
	}
	if err := service.ExpireHostexecGrantRequests(ctx); err != nil {
		return fmt.Errorf("expire host grant requests: %w", err)
	}
	boxes.Mirror = mirror
	boxes.Images = &images.Builder{Recorder: service, Now: time.Now}
	apiServer := api.New(&serviceWithHosts{Service: service, boxes: boxes, quotas: proxy.Quotas(managementKey, time.Now), backupEnabled: recipient != nil}, func(c api.Caller) http.Handler {
		return drain.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			session, err := st.Session(r.Context(), c.Principal.Session)
			if err != nil {
				http.Error(w, "session unavailable", http.StatusServiceUnavailable)
				return
			}
			_, allowed, err := profiles.Model(session.Profile)
			if err != nil {
				http.Error(w, "profile unavailable", http.StatusServiceUnavailable)
				return
			}
			proxy.Handler(string(session.ID), allowed).ServeHTTP(w, r)
		}))
	})
	apiServer.SetFilesRoute(func(c api.Caller) http.Handler { return service.Files(c.Principal, c.Room) })
	apiServer.SetGitRoute(func(c api.Caller) http.Handler {
		return enginegit.Handler(&url.URL{Scheme: "https", Host: "github.com"}, http.DefaultTransport, token, c.Principal.Session, c.Room, st)
	})
	if cfg.WebListen != "" {
		app, err := fs.Sub(web.Dist, "dist")
		if err != nil {
			return err
		}
		webServer, err := api.NewWeb(apiServer, cfg.WebListen, app, time.Now, st)
		if err != nil {
			return err
		}
		listener, err := webServer.Listen()
		if err != nil {
			return err
		}
		defer listener.Close()
	}
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
	for {
		select {
		case <-ctx.Done():
			st.DrainDispatch()
			waitForDrain(drain.Begin(), time.After(60*time.Second))
			return nil
		case <-ticker.C:
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
