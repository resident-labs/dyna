package model

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"golang.org/x/crypto/bcrypt"
	"gopkg.in/yaml.v3"
)

// Proxy owns the proxy listener and its private client key.
type Proxy struct {
	Listener   *net.UnixListener
	Config     string
	Socket     string
	clientKey  string
	client     *http.Client
	generation atomic.Uint64
}

// NewProxy creates a private proxy configuration in dir, with its sign-in folder at authDir. managementKey is held by the engine.
func NewProxy(dir, authDir, managementKey string) (*Proxy, error) {
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
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return nil, err
	}
	// M1 file credentials are not imported; the owner reconnects accounts.
	if err := os.MkdirAll(authDir, 0700); err != nil {
		return nil, err
	}
	if err := os.Chmod(authDir, 0700); err != nil {
		return nil, err
	}
	socket := filepath.Join(dir, "proxy.sock")
	// store.Open holds the data folder lock. A socket here is stale, or belongs to a dying child that no client can reach once it is unlinked.
	if info, err := os.Lstat(socket); err == nil && info.Mode()&os.ModeSocket == 0 {
		return nil, fmt.Errorf("proxy path is not a socket: %s", socket)
	} else if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if err := os.Remove(socket); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		return nil, err
	}
	owned := true
	defer func() {
		if owned {
			_ = listener.Close()
		}
	}()
	// Codex requests get no injected image_generation tool: a session's tools are Dyna's own, and the injected tool's
	// settings can fail a turn at the model call ("Complexity 'medium' is not supported"). The Image API endpoints stay.
	cfg, err := config.ParseConfigBytes([]byte("disable-image-generation: chat\n"))
	if err != nil {
		return nil, err
	}
	cfg.AuthDir = authDir
	// A transient upstream failure is not evidence that the account is unusable.
	// Keep auth and quota cooling, but let PiG retry on the same sole credential.
	// Pinned limitation (accepted): a late 5xx can clear a concurrent 401/403 cooldown; the next 401/403 sets it again.
	cfg.TransientErrorCooldownSeconds = -1
	// An owner-added provider's models keep its prefix, so its name for a model never
	// routes a built-in model's requests to the owner's address.
	cfg.ForceModelPrefix = true
	cfg.APIKeys = []string{key}
	// Unix clients have no loopback IP; the private socket limits access.
	cfg.RemoteManagement.AllowRemote = true
	cfg.RemoteManagement.SecretKey = string(managementHash)
	cfg.RemoteManagement.DisableControlPanel = true
	cfg.Routing.SessionAffinity = true
	cfg.Routing.Strategy = "soonest-reset"
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
	owned = false
	return &Proxy{Listener: listener, Config: path, Socket: socket, clientKey: key, client: unixClient(socket)}, nil
}

// Handler binds a route to a session and its permitted models.
func (p *Proxy) Handler(sessionID string, allowedModels, eligibleIDs []string, pinnedID string, crew bool, sent ...func(context.Context) error) http.Handler {
	return handler(p.client, p.clientKey, sessionID, allowedModels, eligibleIDs, pinnedID, crew, sent...)
}
