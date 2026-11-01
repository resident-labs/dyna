package model

import (
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
	pools      map[string]pool
	keyOrder   []string
	Listener   *net.UnixListener
	Config     string
	Socket     string
	clientKey  string
	client     *http.Client
	generation atomic.Uint64
}

// NewProxy creates a private proxy configuration. managementKey is held by the engine.
func NewProxy(dataDir, managementKey string, catalog Catalog) (*Proxy, error) {
	if managementKey == "" {
		return nil, fmt.Errorf("model: management key is required")
	}
	p := &Proxy{}
	if err := p.initPools(catalog); err != nil {
		return nil, err
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
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return nil, err
	}
	// M1 file credentials are not imported; the owner reconnects accounts.
	authDir := filepath.Join(dir, "auth-v2")
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
	cfg, err := config.ParseConfigBytes([]byte("{}"))
	if err != nil {
		return nil, err
	}
	cfg.AuthDir = authDir
	for _, provider := range catalog.Keys {
		catalog := config.OpenAICompatibility{Name: string(provider.Provider), Prefix: string(provider.Provider), BaseURL: provider.URL, Headers: provider.Headers, DynaCatalogOnly: true}
		for _, model := range provider.Models {
			catalog.Models = append(catalog.Models, config.OpenAICompatibilityModel{Name: model.ID, Alias: model.ID})
		}
		cfg.OpenAICompatibility = append(cfg.OpenAICompatibility, catalog)
	}
	cfg.ForceModelPrefix = true
	cfg.APIKeys = []string{key}
	// Unix clients have no loopback IP; the private socket limits access.
	cfg.RemoteManagement.AllowRemote = true
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
	owned = false
	p.Listener, p.Config, p.Socket, p.clientKey, p.client = listener, path, socket, key, unixClient(socket)
	return p, nil
}

// Handler binds a route to a session and its permitted models.
func (p *Proxy) Handler(sessionID, namespace string, allowedModels, eligibleIDs []string) http.Handler {
	return handler(p.client, p.clientKey, sessionID, namespace, allowedModels, eligibleIDs, p.pools[namespace])
}
