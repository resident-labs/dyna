package runtime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

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
}

type serviceWithHosts struct {
	*engine.Service
	boxes hostRegistrar
}

func (s *serviceWithHosts) HostAdd(ctx context.Context, caller contract.Principal, req contract.HostAddRequest) (contract.HostAddResponse, *contract.Error) {
	if caller.Kind != contract.PrincipalOwner {
		return contract.HostAddResponse{}, &contract.Error{Code: contract.ErrForbidden, Message: "owner only"}
	}
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
}

// dormancyKicks drains events while reconciliation waits on a host.
func dormancyKicks(ctx context.Context, subscribe func() (<-chan contract.Event, func())) <-chan struct{} {
	updates, unsubscribe := subscribe()
	kicks := make(chan struct{}, 1)
	go func() {
		defer func() { unsubscribe() }()
		for {
			select {
			case _, ok := <-updates:
				if !ok {
					unsubscribe()
					updates, unsubscribe = subscribe()
				}
				select {
				case kicks <- struct{}{}:
				default:
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	return kicks
}

func Run(ctx context.Context, cfg Config) error {
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
	profiles, err := engine.LoadProfiles(cfg.Profiles)
	if err != nil {
		return err
	}
	var key [32]byte
	if _, err := rand.Read(key[:]); err != nil {
		return err
	}
	proxy, err := model.NewProxy(cfg.Data, hex.EncodeToString(key[:]))
	if err != nil {
		return err
	}
	defer proxy.Shutdown(context.Background())
	boxCtx, stopBoxes := context.WithCancel(ctx)
	defer stopBoxes()
	boxes := &Boxes{Store: st, Runtime: cfg.Runtime, HostData: cfg.HostData, Version: cfg.Version, PiTarball: pi, Context: boxCtx}
	service := engine.New(st, boxes, profiles, time.Now)
	boxes.Pressure = service
	service.SetTranscriptDir(filepath.Join(cfg.Data, "transcripts"))
	service.SetFilesDir(filepath.Join(cfg.Data, "files"))
	mirror := &enginegit.Mirror{Data: cfg.Data, Token: token, Runner: enginegit.GitRunner{}}
	service.SetRepositories(&enginegit.Repositories{Rooms: st, Hosts: boxes, Mirror: mirror})
	if err := service.ConfigureOperations(ctx, map[contract.OpKind]approve.Entry{"pr.open": service.PrOpen(token, &url.URL{Scheme: "https", Host: "api.github.com"}, http.DefaultTransport)}, cfg.ApprovalsExpiry); err != nil {
		return err
	}
	boxes.Mirror = mirror
	boxes.Images = &images.Builder{Recorder: service, Now: time.Now}
	apiServer := api.New(&serviceWithHosts{Service: service, boxes: boxes}, func(c api.Caller) http.Handler {
		session, err := st.Session(ctx, c.Principal.Session)
		if err != nil {
			return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "session unavailable", http.StatusServiceUnavailable)
			})
		}
		_, allowed, err := profiles.Model(session.Profile)
		if err != nil {
			return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "profile unavailable", http.StatusServiceUnavailable)
			})
		}
		return proxy.Handler(string(session.ID), allowed)
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
		webServer, err := api.NewWeb(apiServer, cfg.WebListen, app, time.Now)
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
	if err := service.Recover(ctx); err != nil {
		return err
	}
	if err := boxes.Restore(ctx); err != nil {
		return err
	}
	watchCtx, stopWatch := context.WithCancel(ctx)
	defer stopWatch()
	kicks := dormancyKicks(watchCtx, st.Subscribe)
	service.ReconcileDormancy(ctx, true, true)
	if problem := service.ResumeClosures(ctx); problem != nil {
		return fmt.Errorf("resume close: %s", problem.Message)
	}
	done := make(chan error, 1)
	go func() { done <- proxy.Run(ctx) }()
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-kicks:
			service.ReconcileDormancy(ctx, false, false)
		case <-ticker.C:
			service.ReconcileDormancy(ctx, false, true)
			if err := service.ExpireOperations(ctx); err != nil {
				return fmt.Errorf("operation expiry sweep: %w", err)
			}
			if err := service.FailStaleStarts(ctx); err != nil {
				return fmt.Errorf("hello timeout sweep: %w", err)
			}
		case err := <-done:
			if err != nil {
				return fmt.Errorf("model proxy: %w", err)
			}
			return fmt.Errorf("model proxy stopped")
		}
	}
}
