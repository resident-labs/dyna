package api

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"github.com/resident-labs/dyna/contract"
)

// PeerUID reads credentials from an accepted owner-socket connection.
type PeerUID func(net.Conn) (uint32, error)

// UnixPeerUID uses Linux SO_PEERCRED. This boundary is also where the
// host-exec scope restriction can be added when host exec is implemented.
func UnixPeerUID(conn net.Conn) (uint32, error) {
	unix, ok := conn.(*net.UnixConn)
	if !ok {
		return 0, fmt.Errorf("not a Unix connection")
	}
	raw, err := unix.SyscallConn()
	if err != nil {
		return 0, err
	}
	var cred *syscall.Ucred
	var socketErr error
	err = raw.Control(func(fd uintptr) {
		cred, socketErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	})
	if err != nil {
		return 0, err
	}
	if socketErr != nil {
		return 0, socketErr
	}
	return cred.Uid, nil
}

type checkedListener struct {
	net.Listener
	uid  uint32
	peer PeerUID
}

func (l *checkedListener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		uid, err := l.peer(c)
		if err == nil && uid == l.uid {
			return c, nil
		}
		_ = c.Close()
	}
}

type socket struct {
	server   *http.Server
	listener net.Listener
	path     string
}

// Sockets owns listener lifetimes and revokes existing connections on removal.
type Sockets struct {
	mu      sync.Mutex
	api     *API
	runtime string
	uid     uint32
	peer    PeerUID
	owner   *socket
	boxes   map[contract.BoxID]*socket
}

func NewSockets(api *API, runtime string, ownerUID uint32, peer PeerUID) *Sockets {
	if peer == nil {
		peer = UnixPeerUID
	}
	return &Sockets{api: api, runtime: runtime, uid: ownerUID, peer: peer, boxes: make(map[contract.BoxID]*socket)}
}

func (s *Sockets) OpenOwner() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.owner != nil {
		return fmt.Errorf("owner socket already open")
	}
	path := filepath.Join(s.runtime, "owner.sock")
	l, err := listen(path)
	if err != nil {
		return err
	}
	checked := &checkedListener{Listener: l, uid: s.uid, peer: s.peer}
	server := &http.Server{Handler: s.api.handler(Caller{Principal: contract.Principal{Kind: contract.PrincipalOwner}})}
	s.owner = &socket{server: server, listener: checked, path: path}
	go server.Serve(checked)
	return nil
}

// AddBox binds a fresh box to its immutable session and room.
func (s *Sockets) AddBox(id contract.BoxID, session contract.SessionID, room contract.RoomID) error {
	if id == "" || session == "" || room == "" || string(id) == "." || string(id) == ".." || strings.ContainsAny(string(id), "/\\") {
		return fmt.Errorf("invalid box binding")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.boxes[id]; ok {
		return fmt.Errorf("box socket already open")
	}
	path := filepath.Join(s.runtime, "boxes", string(id)+".sock")
	l, err := listen(path)
	if err != nil {
		return err
	}
	caller := Caller{Principal: contract.Principal{Kind: contract.PrincipalSession, Session: session}, Box: id, Room: room}
	server := &http.Server{Handler: s.api.handler(caller)}
	s.boxes[id] = &socket{server: server, listener: l, path: path}
	go server.Serve(l)
	return nil
}

func listen(path string) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	l, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0600); err != nil {
		_ = l.Close()
		return nil, err
	}
	return l, nil
}

func closeSocket(s *socket) error {
	// Close the listener even if Serve has not started yet.
	err := s.listener.Close()
	serverErr := s.server.Close()
	if err != nil {
		return err
	}
	return serverErr
}

func (s *Sockets) RemoveBox(id contract.BoxID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.boxes[id]
	if !ok {
		return fmt.Errorf("unknown box")
	}
	delete(s.boxes, id)
	return closeSocket(entry)
}

func (s *Sockets) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var first error
	if s.owner != nil {
		first = closeSocket(s.owner)
		s.owner = nil
	}
	for id, entry := range s.boxes {
		if err := closeSocket(entry); first == nil {
			first = err
		}
		delete(s.boxes, id)
	}
	return first
}
