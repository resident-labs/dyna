package runtime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine"
	"github.com/resident-labs/dyna/engine/api"
	"github.com/resident-labs/dyna/engine/model"
	"github.com/resident-labs/dyna/engine/store"
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

type Config struct {
	Data, Runtime, HostData, Profiles, Version string
}

func Run(ctx context.Context, cfg Config) error {
	st, err := store.Open(cfg.Data, time.Now)
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
	boxes := &Boxes{Store: st, Runtime: cfg.Runtime, HostData: cfg.HostData, Version: cfg.Version}
	service := engine.New(st, boxes, profiles, time.Now)
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
	sockets := api.NewSockets(apiServer, cfg.Runtime, uint32(os.Getuid()), nil)
	boxes.Sockets = sockets
	if err := sockets.OpenOwner(); err != nil {
		return err
	}
	defer sockets.Close()
	if err := boxes.Restore(ctx); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- proxy.Run(ctx) }()
	select {
	case <-ctx.Done():
		return nil
	case err := <-done:
		if err != nil {
			return fmt.Errorf("model proxy: %w", err)
		}
		return fmt.Errorf("model proxy stopped")
	}
}
