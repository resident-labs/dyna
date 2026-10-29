package roommcp

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"regexp"
	"sort"
	"sync"
	"time"
	"unicode/utf8"

	accountstore "dyna-agent/internal/account/store"
	"dyna-agent/internal/authority"
	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/room"
	"dyna-agent/internal/state"
	"dyna-agent/internal/subagent"
)

const (
	saveTokenFirst = "save the token first: dyna room mcp token set <name> --url <url>"
	// argumentsLimit caps the arguments an audit event records.
	argumentsLimit = 2 << 10
)

// errChanged answers a call or attach that raced with a change to the server.
var errChanged = errors.New("the MCP server changed while connecting; try again")

var serverName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// Sessions finds the sessions a grant names. *subagent.Store satisfies it.
type Sessions interface {
	Resolve(ref string) (subagent.Info, error)
}

type serverKey struct{ room, name string }

// serverRuntime is one live connection. Cancelling lifetime ends every call in
// flight, so detach and token changes act at once.
type serverRuntime struct {
	client   *Client
	lifetime context.Context
	cancel   context.CancelFunc
	// gen is the server's generation when this connection was dialed. Every
	// change to a server's stored state ends the old generation (see drop).
	gen uint64
	// initialize runs the MCP handshake once, for every caller of this runtime.
	initialize func() error
}

// Service owns the room-scoped server state and the live connections.
type Service struct {
	db       *sql.DB
	accounts *accountstore.Store
	rooms    *room.Store
	sessions Sessions
	now      func() time.Time
	// inChange lets a test look at the service while a change holds the lock.
	inChange func()
	// beforeChange lets a test run another operation just before a change takes the lock.
	beforeChange func()
	// afterDial lets a test pause a connection between dialing and registering.
	afterDial func()
	mu        sync.Mutex
	active    map[serverKey]serverRuntime
	gen       map[serverKey]uint64
}

// NewService opens the server registry. The engine migrates its tables first (Migrate). Tokens stay in the accounts store.
func NewService(dataDir string, accounts *accountstore.Store, rooms *room.Store, sessions Sessions) (*Service, error) {
	db, err := database.Open(dataDir)
	if err != nil {
		return nil, err
	}
	return &Service{db: db, accounts: accounts, rooms: rooms, sessions: sessions, now: time.Now, active: make(map[serverKey]serverRuntime), gen: make(map[serverKey]uint64)}, nil
}

// manage resolves the room and checks the caller. The owner may do anything;
// root may do what ownerOnly leaves open. Any other caller is refused.
func (s *Service) manage(ctx context.Context, roomRef string, ownerOnly bool) (room.Record, error) {
	rec, err := s.rooms.Resolve(roomRef)
	if err != nil {
		return room.Record{}, err
	}
	actor, err := authority.ActorIn(ctx, rec.ID)
	if err != nil {
		return room.Record{}, err
	}
	switch {
	case actor.Role == authority.Owner, actor.Role == authority.Root && !ownerOnly:
		return rec, nil
	case ownerOnly:
		return room.Record{}, errors.New("only the owner can do this")
	}
	return room.Record{}, errors.New("only the owner or the room's root can do this")
}

// SetToken saves a room-bound token and the server URL without contacting the server.
func (s *Service) SetToken(ctx context.Context, roomRef, name, serverURL, token string) error {
	rec, err := s.manage(ctx, roomRef, true)
	if err != nil {
		return err
	}
	if !serverName.MatchString(name) {
		return errors.New("a server name has letters, digits, dot, dash or underscore, and is at most 64 characters")
	}
	if _, err := NewClient(serverURL, token); err != nil {
		return err
	}
	return s.change(serverKey{rec.ID, name}, func() error {
		var credential accountstore.Metadata
		if err := s.accounts.Update(func(st *accountstore.State) error {
			var saveErr error
			credential, saveErr = st.SaveMCPBearer(rec.ID, name, token)
			return saveErr
		}); err != nil {
			return err
		}
		return state.For(s.db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
			// A new URL is a new server: its tools start off and nobody holds a grant.
			for _, table := range []string{"room_mcp_tools", "room_mcp_grants"} {
				if _, err := conn.ExecContext(ctx, "DELETE FROM "+table+" WHERE room=? AND server=? AND EXISTS (SELECT 1 FROM room_mcp_servers WHERE room=? AND name=? AND url<>?)", rec.ID, name, rec.ID, name, serverURL); err != nil {
					return err
				}
			}
			_, err := conn.ExecContext(ctx, `INSERT INTO room_mcp_servers(room,name,url,credential_id,attached) VALUES(?,?,?,?,0)
   ON CONFLICT(room,name) DO UPDATE SET url=excluded.url, credential_id=excluded.credential_id, attached=0`, rec.ID, name, serverURL, credential.ID)
			return err
		})
	})
}

// RemoveToken deletes the saved token and detaches the server. The pinned tools stay.
func (s *Service) RemoveToken(ctx context.Context, roomRef, name string) error {
	rec, err := s.manage(ctx, roomRef, true)
	if err != nil {
		return err
	}
	return s.change(serverKey{rec.ID, name}, func() error {
		// Read the credential under the lock: the server may have been replaced since the call began.
		id, err := s.credentialID(ctx, rec.ID, name)
		if err != nil {
			return err
		}
		if err := s.setAttached(ctx, rec.ID, name, false); err != nil {
			return err
		}
		return s.accounts.Update(func(st *accountstore.State) error {
			st.Remove(id)
			return nil
		})
	})
}

// Remove deletes the server, its tools, its grants and its token.
func (s *Service) Remove(ctx context.Context, roomRef, name string) error {
	rec, err := s.manage(ctx, roomRef, true)
	if err != nil {
		return err
	}
	return s.change(serverKey{rec.ID, name}, func() error {
		id, err := s.credentialID(ctx, rec.ID, name)
		if err != nil {
			return err
		}
		if err := state.For(s.db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
			_, err := conn.ExecContext(ctx, "DELETE FROM room_mcp_servers WHERE room=? AND name=?", rec.ID, name)
			return err
		}); err != nil {
			return err
		}
		return s.accounts.Update(func(st *accountstore.State) error {
			st.Remove(id)
			return nil
		})
	})
}

func (s *Service) credentialID(ctx context.Context, roomID, name string) (string, error) {
	var id string
	err := s.db.QueryRowContext(ctx, "SELECT credential_id FROM room_mcp_servers WHERE room=? AND name=?", roomID, name).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", errors.New("no such MCP server")
	}
	return id, err
}

// change runs a change to one server's stored state with the service lock held.
// It ends the live connection and every call in flight first, and starts a new
// generation. Registering a connection and marking a server attached take the
// same lock and check the generation, so none that began earlier can finish
// during or after the change. Detach, token removal, a new token and removal
// all go through here.
func (s *Service) change(key serverKey, write func() error) error {
	if s.beforeChange != nil {
		s.beforeChange()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gen[key]++
	if active, ok := s.active[key]; ok {
		active.cancel()
		delete(s.active, key)
	}
	if s.inChange != nil {
		s.inChange()
	}
	return write()
}

func (s *Service) setAttached(ctx context.Context, roomID, name string, attached bool) error {
	return state.For(s.db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
		result, err := conn.ExecContext(ctx, "UPDATE room_mcp_servers SET attached=? WHERE room=? AND name=?", attached, roomID, name)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return errors.New("no such MCP server")
		}
		return nil
	})
}

// dial builds a connection from the saved token. Nothing can use it until the
// caller registers it.
func (s *Service) dial(ctx context.Context, roomID, name string) (serverRuntime, error) {
	// Read the generation before any stored state, so a change after this point
	// is a later generation and the registration check rejects this connection.
	s.mu.Lock()
	gen := s.gen[serverKey{roomID, name}]
	s.mu.Unlock()
	var rawURL, id string
	err := s.db.QueryRowContext(ctx, "SELECT url,credential_id FROM room_mcp_servers WHERE room=? AND name=?", roomID, name).Scan(&rawURL, &id)
	if errors.Is(err, sql.ErrNoRows) {
		return serverRuntime{}, errors.New(saveTokenFirst)
	}
	if err != nil {
		return serverRuntime{}, err
	}
	st, err := s.accounts.Read()
	if err != nil {
		return serverRuntime{}, err
	}
	entry := st.Find(id)
	if entry == nil || entry.Provider != accountstore.ProviderMCPBearer || entry.RoomID != roomID {
		return serverRuntime{}, errors.New(saveTokenFirst)
	}
	var payload struct {
		Token string `json:"token"`
	}
	if json.Unmarshal(entry.Credential.Payload, &payload) != nil || payload.Token == "" {
		return serverRuntime{}, errors.New(saveTokenFirst)
	}
	client, err := NewClient(rawURL, payload.Token)
	if err != nil {
		return serverRuntime{}, err
	}
	lifetime, cancel := context.WithCancel(context.Background())
	rt := serverRuntime{client: client, lifetime: lifetime, cancel: cancel, gen: gen}
	rt.initialize = sync.OnceValue(func() error { return client.Initialize(lifetime) })
	return rt, nil
}

// open starts a fresh connection and replaces any live one. Attach uses it.
func (s *Service) open(ctx context.Context, roomID, name string) (serverRuntime, error) {
	rt, err := s.dial(ctx, roomID, name)
	if err != nil {
		return serverRuntime{}, err
	}
	key := serverKey{roomID, name}
	if s.afterDial != nil {
		s.afterDial()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.gen[key] != rt.gen {
		rt.cancel()
		return serverRuntime{}, errChanged
	}
	if previous, ok := s.active[key]; ok {
		previous.cancel()
	}
	s.active[key] = rt
	return rt, nil
}

// forget ends rt and removes it from the live set, unless a newer connection
// has replaced it.
func (s *Service) forget(key serverKey, rt serverRuntime) {
	rt.cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	if active, ok := s.active[key]; ok && active.lifetime == rt.lifetime {
		delete(s.active, key)
	}
}

// connection returns the live connection. After an engine restart it dials a
// new one and registers it only if the server is still attached. Detach clears
// the flag, then drops the live connection under the same lock, so a stale call
// either sees the cleared flag or registers a connection that detach then ends.
// No call can use a connection that was not checked.
func (s *Service) connection(ctx context.Context, roomID, name string) (serverRuntime, error) {
	key := serverKey{roomID, name}
	s.mu.Lock()
	rt, ok := s.active[key]
	s.mu.Unlock()
	if ok {
		return rt, nil
	}
	fresh, err := s.dial(ctx, roomID, name)
	if err != nil {
		return serverRuntime{}, err
	}
	if s.afterDial != nil {
		s.afterDial()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if rt, ok := s.active[key]; ok {
		fresh.cancel()
		return rt, nil
	}
	if s.gen[key] != fresh.gen {
		fresh.cancel()
		return serverRuntime{}, errChanged
	}
	var attached bool
	if err := s.db.QueryRowContext(ctx, "SELECT attached FROM room_mcp_servers WHERE room=? AND name=?", roomID, name).Scan(&attached); err != nil || !attached {
		fresh.cancel()
		return serverRuntime{}, errors.New("MCP server is detached")
	}
	s.active[key] = fresh
	return fresh, nil
}

// pin reads the server's tool list and stores it. A new tool starts off, and a
// tool the server no longer lists is forgotten.
func (s *Service) pin(ctx context.Context, rec room.Record, name string, rt serverRuntime, attach bool) error {
	// A detach ends the handshake and the listing as well as calls.
	callCtx, stop := context.WithCancel(ctx)
	defer stop()
	defer context.AfterFunc(rt.lifetime, stop)()
	if err := rt.initialize(); err != nil {
		s.forget(serverKey{rec.ID, name}, rt)
		return err
	}
	tools, err := rt.client.ListTools(callCtx)
	if err != nil {
		s.forget(serverKey{rec.ID, name}, rt)
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if rt.lifetime.Err() != nil {
		return errors.New("MCP server is detached")
	}
	if s.gen[serverKey{rec.ID, name}] != rt.gen {
		return errChanged
	}
	return state.For(s.db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
		if attach {
			if _, err := conn.ExecContext(ctx, "UPDATE room_mcp_servers SET attached=1 WHERE room=? AND name=?", rec.ID, name); err != nil {
				return err
			}
		}
		listed := make(map[string]bool, len(tools))
		for _, tool := range tools {
			if tool.Name == "" {
				return errors.New("invalid MCP tool name")
			}
			listed[tool.Name] = true
			if _, err := conn.ExecContext(ctx, "INSERT OR IGNORE INTO room_mcp_tools(room,server,tool) VALUES(?,?,?)", rec.ID, name, tool.Name); err != nil {
				return err
			}
		}
		rows, err := conn.QueryContext(ctx, "SELECT tool FROM room_mcp_tools WHERE room=? AND server=?", rec.ID, name)
		if err != nil {
			return err
		}
		var gone []string
		for rows.Next() {
			var tool string
			if err := rows.Scan(&tool); err != nil {
				rows.Close()
				return err
			}
			if !listed[tool] {
				gone = append(gone, tool)
			}
		}
		if err := rows.Close(); err != nil {
			return err
		}
		for _, tool := range gone {
			if _, err := conn.ExecContext(ctx, "DELETE FROM room_mcp_tools WHERE room=? AND server=? AND tool=?", rec.ID, name, tool); err != nil {
				return err
			}
		}
		return nil
	})
}

// Attach connects with the saved token and pins the server's tool list, every tool off.
func (s *Service) Attach(ctx context.Context, roomRef, name string) error {
	rec, err := s.manage(ctx, roomRef, true)
	if err != nil {
		return err
	}
	rt, err := s.open(ctx, rec.ID, name)
	if err != nil {
		return err
	}
	return s.pin(ctx, rec, name, rt, true)
}

// Detach refuses new calls and cancels calls already in flight.
func (s *Service) Detach(ctx context.Context, roomRef, name string) error {
	rec, err := s.manage(ctx, roomRef, false)
	if err != nil {
		return err
	}
	return s.change(serverKey{rec.ID, name}, func() error { return s.setAttached(ctx, rec.ID, name, false) })
}

// ToolView is one pinned tool. Allowed is the owner's allow list entry.
type ToolView struct {
	Name    string `json:"name"`
	Allowed bool   `json:"allowed"`
}

// Tools lists the pinned tools. With refresh it reads the live list first.
func (s *Service) Tools(ctx context.Context, roomRef, name string, refresh bool) ([]ToolView, error) {
	// Reading the pinned list is open to root. Refresh contacts the server, so
	// it is owner only.
	rec, err := s.manage(ctx, roomRef, refresh)
	if err != nil {
		return nil, err
	}
	if refresh {
		var attached bool
		if err := s.db.QueryRowContext(ctx, "SELECT attached FROM room_mcp_servers WHERE room=? AND name=?", rec.ID, name).Scan(&attached); err != nil || !attached {
			return nil, errors.New("attach the server first: dyna room mcp attach <name>")
		}
		rt, err := s.connection(ctx, rec.ID, name)
		if err != nil {
			return nil, err
		}
		if err := s.pin(ctx, rec, name, rt, false); err != nil {
			return nil, err
		}
	}
	rows, err := s.db.QueryContext(ctx, "SELECT tool,allowed FROM room_mcp_tools WHERE room=? AND server=? ORDER BY tool", rec.ID, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tools []ToolView
	for rows.Next() {
		var t ToolView
		if err := rows.Scan(&t.Name, &t.Allowed); err != nil {
			return nil, err
		}
		tools = append(tools, t)
	}
	return tools, rows.Err()
}

// Allow turns read access on or off for pinned tools. Write access does not exist yet.
func (s *Service) Allow(ctx context.Context, roomRef, name string, tools []string, off bool) error {
	rec, err := s.manage(ctx, roomRef, true)
	if err != nil {
		return err
	}
	return state.For(s.db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
		for _, tool := range tools {
			result, err := conn.ExecContext(ctx, "UPDATE room_mcp_tools SET allowed=? WHERE room=? AND server=? AND tool=?", !off, rec.ID, name, tool)
			if err != nil {
				return err
			}
			if n, err := result.RowsAffected(); err != nil || n == 0 {
				return fmt.Errorf("%q is not a pinned tool of %s; run dyna room mcp tools %s --refresh", tool, name, name)
			}
		}
		return nil
	})
}

// GrantKind names what a grant applies to.
type GrantKind string

const (
	GrantSession GrantKind = "session"
	GrantProfile GrantKind = "profile"
)

// Grant lets one session, or every session on a profile, call the allowed tools.
// Children do not inherit a grant. A session subject is resolved to its ID.
func (s *Service) Grant(ctx context.Context, roomRef, name string, kind GrantKind, subject string, revoke bool) error {
	rec, err := s.manage(ctx, roomRef, false)
	if err != nil {
		return err
	}
	switch kind {
	case GrantSession:
		info, err := s.sessions.Resolve(subject)
		if err != nil {
			return err
		}
		if info.RoomID != rec.ID {
			return errors.New("that session is not in this room")
		}
		subject = info.ID
	case GrantProfile:
		if subject == "" {
			return errors.New("a profile is required")
		}
	default:
		return errors.New("a grant names a session or a profile")
	}
	by := string(authority.Of(ctx).Role)
	return state.For(s.db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
		if revoke {
			_, err := conn.ExecContext(ctx, "DELETE FROM room_mcp_grants WHERE room=? AND server=? AND kind=? AND subject=?", rec.ID, name, kind, subject)
			return err
		}
		var exists int
		if err := conn.QueryRowContext(ctx, "SELECT 1 FROM room_mcp_servers WHERE room=? AND name=?", rec.ID, name).Scan(&exists); err != nil {
			return errors.New("no such MCP server")
		}
		_, err := conn.ExecContext(ctx, "INSERT OR IGNORE INTO room_mcp_grants(room,server,kind,subject,granted_by) VALUES(?,?,?,?,?)", rec.ID, name, kind, subject, by)
		return err
	})
}

// ServerView is what any member of the room may see about a server. It holds no secret.
type ServerView struct {
	Name     string   `json:"name"`
	URL      string   `json:"url"`
	Attached bool     `json:"attached"`
	Tools    int      `json:"tools"`
	Allowed  int      `json:"allowed"`
	Grants   []string `json:"grants,omitempty"`
}

// List returns the room's servers.
func (s *Service) List(ctx context.Context, roomRef string) ([]ServerView, error) {
	rec, err := s.rooms.Resolve(roomRef)
	if err != nil {
		return nil, err
	}
	actor, err := authority.ActorIn(ctx, rec.ID)
	if err != nil {
		return nil, err
	}
	if actor.Role != authority.Owner && actor.Role != authority.Root && actor.Role != authority.Session {
		return nil, errors.New("this caller cannot list MCP servers")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT name,url,attached,
 (SELECT count(*) FROM room_mcp_tools t WHERE t.room=s.room AND t.server=s.name),
 (SELECT count(*) FROM room_mcp_tools t WHERE t.room=s.room AND t.server=s.name AND t.allowed=1)
 FROM room_mcp_servers s WHERE room=? ORDER BY name`, rec.ID)
	if err != nil {
		return nil, err
	}
	var views []ServerView
	for rows.Next() {
		var v ServerView
		if err := rows.Scan(&v.Name, &v.URL, &v.Attached, &v.Tools, &v.Allowed); err != nil {
			rows.Close()
			return nil, err
		}
		views = append(views, v)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for i := range views {
		grants, err := s.db.QueryContext(ctx, "SELECT kind,subject FROM room_mcp_grants WHERE room=? AND server=?", rec.ID, views[i].Name)
		if err != nil {
			return nil, err
		}
		for grants.Next() {
			var kind, subject string
			if err := grants.Scan(&kind, &subject); err != nil {
				grants.Close()
				return nil, err
			}
			views[i].Grants = append(views[i].Grants, kind+":"+subject)
		}
		if err := grants.Close(); err != nil {
			return nil, err
		}
		sort.Strings(views[i].Grants)
	}
	return views, nil
}

// granted reports whether the session, or its profile, holds a grant.
func (s *Service) granted(ctx context.Context, roomID, name string, actor authority.Actor) (bool, error) {
	info, err := s.sessions.Resolve(actor.Session)
	if err != nil {
		return false, err
	}
	var n int
	err = s.db.QueryRowContext(ctx, `SELECT count(*) FROM room_mcp_grants WHERE room=? AND server=?
 AND ((kind='session' AND subject=?) OR (kind='profile' AND subject=?))`, roomID, name, actor.Session, info.Profile).Scan(&n)
	return n > 0, err
}

// Call runs one allowed tool for the calling session. The caller, its room and
// its session come from the engine's authenticated actor and nothing else. The
// answer is wrapped as untrusted data. Every call writes one audit event.
func (s *Service) Call(ctx context.Context, name, tool string, arguments json.RawMessage) (string, error) {
	actor := authority.Of(ctx)
	if actor.Room == "" || actor.Session == "" || (actor.Role != authority.Session && actor.Role != authority.Root) {
		return "", errors.New("this action requires a room session")
	}
	rec, err := s.rooms.Resolve(actor.Room)
	if err != nil {
		return "", err
	}
	var attached bool
	err = s.db.QueryRowContext(ctx, "SELECT attached FROM room_mcp_servers WHERE room=? AND name=?", rec.ID, name).Scan(&attached)
	if errors.Is(err, sql.ErrNoRows) {
		return "", errors.New("no such server")
	}
	if err != nil {
		return "", err
	}
	started := s.now()
	audit := room.MCPCallEvent{Server: name, Tool: tool, Session: actor.Session}
	finish := func(outcome room.MCPOutcome, resultBytes int, secrets []string) {
		audit.Outcome = outcome
		audit.ResultBytes = resultBytes
		audit.DurationMs = s.now().Sub(started).Milliseconds()
		audit.Arguments = capText(Scrub(string(arguments), secrets), argumentsLimit)
		if _, err := s.rooms.PostEventInput(room.PostEventInput{RoomID: rec.ID, Event: audit, To: []string{rec.Name}, FreshID: true}); err != nil {
			log.Printf("room MCP call audit was not written: %v", err)
		}
	}
	ok, err := s.granted(ctx, actor.Room, name, actor)
	if err != nil {
		return "", err
	}
	if !ok {
		finish(room.MCPNotGranted, 0, nil)
		return "", errors.New("not granted")
	}
	if !attached {
		finish(room.MCPDetached, 0, nil)
		return "", errors.New("MCP server is detached")
	}
	var allowed bool
	if err := s.db.QueryRowContext(ctx, "SELECT allowed FROM room_mcp_tools WHERE room=? AND server=? AND tool=?", actor.Room, name, tool).Scan(&allowed); err != nil || !allowed {
		finish(room.MCPNotAllowed, 0, nil)
		return "", errors.New("tool is not allowed")
	}
	rt, err := s.connection(ctx, actor.Room, name)
	if err != nil {
		finish(room.MCPServerError, 0, nil)
		return "", err
	}
	secrets := []string{rt.client.token}
	callCtx, stop := context.WithCancel(ctx)
	defer stop()
	defer context.AfterFunc(rt.lifetime, stop)()
	var result Result
	err = rt.initialize()
	handshakeFailed := err != nil
	if !handshakeFailed {
		result, err = rt.client.CallTool(callCtx, tool, arguments)
	}
	detached := rt.lifetime.Err() != nil
	if handshakeFailed {
		// The handshake result is cached for the connection's life. Forget the
		// connection so the next call starts a new one.
		s.forget(serverKey{actor.Room, name}, rt)
	}
	var serverErr *ServerError
	switch {
	case errors.As(err, &serverErr):
		// The server's error text is untrusted like any other answer.
		result = Result{Content: []Content{{Type: "text", Text: serverErr.Message}}, IsError: true}
	case err != nil && detached:
		finish(room.MCPDetached, 0, secrets)
		return "", errors.New("MCP server is detached")
	case err != nil:
		finish(room.MCPServerError, 0, secrets)
		return "", errors.New(Scrub(err.Error(), secrets))
	}
	wrapped, err := WrapResult(name+"/"+tool, result, secrets)
	if err != nil {
		finish(room.MCPServerError, 0, secrets)
		return "", err
	}
	outcome := room.MCPOk
	if result.IsError {
		outcome = room.MCPServerError
	}
	finish(outcome, len(wrapped), secrets)
	return wrapped, nil
}

// capText cuts text to at most n bytes without splitting a character.
func capText(text string, n int) string {
	if len(text) <= n {
		return text
	}
	for n > 0 && !utf8.RuneStart(text[n]) {
		n--
	}
	return text[:n]
}
