package handlers

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func runPinned(t *testing.T, pin string, execute func(string) error) ([]string, *coreauth.Manager, error) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	manager := coreauth.NewManager(nil, coreauth.NewSessionAffinitySelector(&coreauth.SoonestResetSelector{}), nil)
	var tried []string
	manager.RegisterExecutor(&modelExecutionCaptureExecutor{provider: "dyna-pin", execute: func(_ context.Context, a *coreauth.Auth, _ coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
		tried = append(tried, a.ID)
		if opts.Headers.Get("X-Dyna-Pinned-Auth") != "" {
			t.Error("private header reached upstream")
		}
		if err := execute(a.ID); err != nil {
			return coreexecutor.Response{}, err
		}
		return coreexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
	}})
	for _, id := range []string{"dyna-pin-a", "dyna-pin-b"} {
		if _, err := manager.Register(context.Background(), &coreauth.Auth{ID: id, Provider: "dyna-pin", Status: coreauth.StatusActive}); err != nil {
			t.Fatal(err)
		}
		registry.GetGlobalRegistry().RegisterClient(id, "dyna-pin", []*registry.ModelInfo{{ID: "dyna-pin-model"}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
	}
	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ginCtx.Request.Header.Set("X-Dyna-Pinned-Auth", base64.RawURLEncoding.EncodeToString([]byte(pin)))
	ctx, cancel := handler.GetContextWithCancel(nil, ginCtx, context.Background())
	defer cancel()
	_, problem := handler.ExecuteModel(ctx, ModelExecutionRequest{EntryProtocol: "openai", ExitProtocol: "openai", Model: "dyna-pin-model", Body: []byte(`{"model":"dyna-pin-model"}`)})
	if problem != nil {
		return tried, manager, problem.Error
	}
	return tried, manager, nil
}

func TestDynaPinHandlerSendsTheTurnToTheChosenAccount(t *testing.T) {
	tried, _, err := runPinned(t, "dyna-pin-b", func(string) error { return nil })
	if err != nil || len(tried) != 1 || tried[0] != "dyna-pin-b" {
		t.Fatalf("tried %v, %v", tried, err)
	}
}

func TestDynaPinRefusedRequestFallsBackAndTheTurnSucceeds(t *testing.T) {
	tried, _, err := runPinned(t, "dyna-pin-b", func(id string) error {
		if id == "dyna-pin-b" {
			return &coreauth.Error{Code: "usage_limit_reached", Message: "invented", HTTPStatus: http.StatusTooManyRequests}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("a refused pin failed the turn: %v", err)
	}
	if len(tried) != 2 || tried[0] != "dyna-pin-b" || tried[1] != "dyna-pin-a" {
		t.Fatalf("tried %v", tried)
	}
}

func TestDynaPinOnARemovedAccountRunsOnAutomatic(t *testing.T) {
	tried, _, err := runPinned(t, "dyna-pin-gone", func(string) error { return nil })
	if err != nil || len(tried) != 1 {
		t.Fatalf("tried %v, %v", tried, err)
	}
}
