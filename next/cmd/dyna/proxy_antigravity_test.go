package main

import (
	"net/http"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/model"
	sdkapi "github.com/router-for-me/CLIProxyAPI/v8/sdk/api"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestAntigravityConnectReachesGoogleConsent(t *testing.T) {
	dir := t.TempDir()
	p, err := model.NewProxy(dir, filepath.Join(dir, "auth"), "invented")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Listener.Close() })
	cfg, err := config.LoadConfig(p.Config)
	if err != nil {
		t.Fatal(err)
	}
	h := sdkapi.NewHandlerWithoutConfigFilePath(cfg, nil)
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/v8/management/oauth/auth-url", h.StartOAuthV8)
	server := &http.Server{Handler: router}
	go func() { _ = server.Serve(p.Listener) }()
	t.Cleanup(func() { _ = server.Close() })

	connections := p.Connections("invented", nil, time.Now)
	start, err := connections.Start(t.Context(), contract.AccountsConnectRequest{Provider: contract.ProviderAntigravity, Label: "Invented"})
	if err != nil {
		t.Fatal(err)
	}
	consent, err := url.Parse(start.Link)
	if err != nil {
		t.Fatal("proxy returned an invalid consent URL")
	}
	q := consent.Query()
	defer sdkapi.SetOAuthSessionError(q.Get("state"), "test finished")
	if start.Attempt == "" || start.Flow != contract.ConnectFlowCallback || start.Code != "" || !start.ExpiresAt.After(time.Now()) {
		t.Fatal("Antigravity did not start the callback flow")
	}
	if consent.Scheme != "https" || consent.Host != "accounts.google.com" || consent.Path != "/o/oauth2/v2/auth" || q.Get("response_type") != "code" || q.Get("client_id") == "" || q.Get("state") == "" || q.Get("redirect_uri") != "http://localhost:51121/oauth-callback" {
		t.Fatal("Antigravity did not produce a Google consent URL with the expected loopback callback")
	}
}
