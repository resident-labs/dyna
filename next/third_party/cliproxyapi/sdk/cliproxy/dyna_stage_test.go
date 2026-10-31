package cliproxy

import (
	"context"
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestDynaStagedCredentialCannotRouteBeforeBinding(t *testing.T) {
	ctx := context.Background()
	manager := coreauth.NewManager(&dynaTestStore{}, nil, nil)
	manager.RegisterExecutor(serviceTestSDKExecutor{})
	service := &Service{coreManager: manager, cfg: &config.Config{}}
	ready, bind, done := make(chan error, 1), make(chan struct{}), make(chan error, 1)
	go func() {
		err := service.RegisterDynaCredential(ctx, &coreauth.Auth{ID: "invented-staged", Provider: "sdk-provider"})
		ready <- err
		if err != nil {
			return
		}
		<-bind
		done <- service.ActivateDynaCredential(ctx, "invented-staged", "", "plan_test")
	}()
	if err := <-ready; err != nil {
		t.Fatal(err)
	}
	registry := GlobalModelRegistry()
	registry.RegisterClient("invented-staged", "sdk-provider", []*ModelInfo{{ID: "invented-model"}})
	defer registry.UnregisterClient("invented-staged")
	manager.RefreshSchedulerEntry("invented-staged")
	_, err := manager.Execute(ctx, []string{"sdk-provider"}, executor.Request{Model: "invented-model"}, executor.Options{})
	if err == nil {
		t.Error("unbound credential served a model request")
	}
	close(bind)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	registry.RegisterClient("invented-staged", "sdk-provider", []*ModelInfo{{ID: "invented-model"}})
	manager.RefreshSchedulerEntry("invented-staged")
	if _, err := manager.Execute(ctx, []string{"sdk-provider"}, executor.Request{Model: "invented-model"}, executor.Options{}); err != nil {
		t.Fatal("bound credential could not route")
	}
}
