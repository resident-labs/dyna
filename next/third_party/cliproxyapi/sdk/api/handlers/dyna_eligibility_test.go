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

func TestDynaEligibilityHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)
	manager := coreauth.NewManager(nil, nil, nil)
	selected := ""
	executor := &modelExecutionCaptureExecutor{provider: "dyna-handler", execute: func(_ context.Context, a *coreauth.Auth, _ coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
		selected = a.ID
		if opts.Headers.Get("X-Dyna-Eligible-Auth") != "" {
			t.Error("private header reached upstream")
		}
		return coreexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
	}}
	manager.RegisterExecutor(executor)
	for _, id := range []string{"dyna-handler-a", "dyna-handler-b"} {
		if _, err := manager.Register(context.Background(), &coreauth.Auth{ID: id, Provider: "dyna-handler", Status: coreauth.StatusActive}); err != nil {
			t.Fatal(err)
		}
		registry.GetGlobalRegistry().RegisterClient(id, "dyna-handler", []*registry.ModelInfo{{ID: "dyna-handler-model"}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
	}
	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ginCtx.Request.Header.Set("X-Dyna-Eligible-Auth", base64.RawURLEncoding.EncodeToString([]byte("dyna-handler-b")))
	ctx, cancel := handler.GetContextWithCancel(nil, ginCtx, context.Background())
	defer cancel()
	_, problem := handler.ExecuteModel(ctx, ModelExecutionRequest{EntryProtocol: "openai", ExitProtocol: "openai", Model: "dyna-handler-model", Body: []byte(`{"model":"dyna-handler-model"}`)})
	if problem != nil || selected != "dyna-handler-b" {
		t.Fatalf("selected %q problem %v", selected, problem)
	}
}
