package model

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"golang.org/x/crypto/bcrypt"
	"gopkg.in/yaml.v3"
)

// Proxy owns the in-process CLIProxyAPI service and its private client key.
type Proxy struct {
	service   *cliproxy.Service
	upstream  *url.URL
	clientKey string
}

// NewProxy creates a private proxy configuration. managementKey is held by the engine.
func NewProxy(dataDir, managementKey string) (*Proxy, error) {
	if managementKey == "" {
		return nil, fmt.Errorf("model: management key is required")
	}
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return nil, err
	}
	key := hex.EncodeToString(secret[:])
	managementHash, err := bcrypt.GenerateFromPassword([]byte(managementKey), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(dataDir, "cliproxy")
	authDir := filepath.Join(dir, "auth")
	if err := os.MkdirAll(authDir, 0700); err != nil {
		return nil, err
	}
	if err := os.Chmod(authDir, 0700); err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		return nil, err
	}
	cfg, err := config.ParseConfigBytes([]byte("{}"))
	if err != nil {
		return nil, err
	}
	cfg.Host = "127.0.0.1"
	cfg.Port = port
	cfg.AuthDir = authDir
	cfg.APIKeys = []string{key}
	cfg.RemoteManagement.SecretKey = string(managementHash)
	cfg.RemoteManagement.DisableControlPanel = true
	cfg.Routing.SessionAffinity = true
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "config.yaml")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return nil, err
	}
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	closeErr := f.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	service, err := cliproxy.NewBuilder().WithConfig(cfg).WithConfigPath(path).Build()
	if err != nil {
		return nil, err
	}
	return &Proxy{service: service, upstream: &url.URL{Scheme: "http", Host: net.JoinHostPort("127.0.0.1", strconv.Itoa(port))}, clientKey: key}, nil
}

// Run runs until ctx is cancelled or the proxy stops.
func (p *Proxy) Run(ctx context.Context) error { return p.service.Run(ctx) }

// Shutdown stops the proxy.
func (p *Proxy) Shutdown(ctx context.Context) error { return p.service.Shutdown(ctx) }

// Handler binds a route to a session and its permitted models.
func (p *Proxy) Handler(sessionID string, allowedModels []string) http.Handler {
	return Handler(p.upstream, p.clientKey, sessionID, allowedModels)
}
