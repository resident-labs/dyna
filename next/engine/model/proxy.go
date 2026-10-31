package model

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"syscall"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"golang.org/x/crypto/bcrypt"
	"gopkg.in/yaml.v3"
)

// Proxy owns the proxy listener and its private client key.
type Proxy struct {
	Listener  *net.UnixListener
	Config    string
	Socket    string
	lock      *os.File
	clientKey string
}

// NewProxy creates a private proxy configuration. managementKey is held by the engine.
func NewProxy(dataDir, managementKey string) (*Proxy, error) {
	if managementKey == "" {
		return nil, fmt.Errorf("model: management key is required")
	}
	dir := filepath.Join(dataDir, "cliproxy")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(filepath.Join(dir, "proxy.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	owned := false
	defer func() {
		if !owned {
			_ = lock.Close()
		}
	}()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return nil, fmt.Errorf("model proxy data is in use: %w", err)
	}
	if err := os.Chmod(dir, 0700); err != nil {
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
	authDir := filepath.Join(dir, "auth")
	if err := os.MkdirAll(authDir, 0700); err != nil {
		return nil, err
	}
	if err := os.Chmod(authDir, 0700); err != nil {
		return nil, err
	}
	socket := filepath.Join(dir, "proxy.sock")
	info, err := os.Lstat(socket)
	if err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("proxy path is not a socket: %s", socket)
		}
		conn, err := net.Dial("unix", socket)
		if err == nil {
			_ = conn.Close()
			return nil, fmt.Errorf("proxy socket is in use: %s", socket)
		}
		if !errors.Is(err, syscall.ECONNREFUSED) && !errors.Is(err, syscall.ENOENT) {
			return nil, err
		}
		current, err := os.Lstat(socket)
		if err == nil && !os.SameFile(info, current) {
			return nil, fmt.Errorf("proxy socket changed: %s", socket)
		}
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		if err := os.Remove(socket); err != nil && !os.IsNotExist(err) {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		return nil, err
	}
	defer func() {
		if !owned {
			_ = listener.Close()
		}
	}()
	cfg, err := config.ParseConfigBytes([]byte("{}"))
	if err != nil {
		return nil, err
	}
	cfg.AuthDir = authDir
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
	owned = true
	return &Proxy{Listener: listener, Config: path, Socket: socket, lock: lock, clientKey: key}, nil
}

// Close releases the listener and data lock.
func (p *Proxy) Close() { _ = p.Listener.Close(); _ = p.lock.Close() }

// Handler binds a route to a session and its permitted models.
func (p *Proxy) Handler(sessionID string, allowedModels []string) http.Handler {
	return Handler(p.Socket, p.clientKey, sessionID, allowedModels)
}
