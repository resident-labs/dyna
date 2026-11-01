// Package api serves the engine's local HTTP API.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strconv"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/contract/tools"
	"github.com/resident-labs/dyna/engine/connect"
	"github.com/resident-labs/dyna/engine/store"
)

type API struct {
	service Service
	Build   string
	model   func(Caller) http.Handler
	git     func(Caller) http.Handler
	web     *Web
	files   func(Caller) http.Handler
	connect ConnectOperations
}

type ConnectOperations interface {
	Start(context.Context, contract.ConnectStartRequest) error
	Status(context.Context) (contract.ConnectStatusResponse, error)
	Cancel(context.Context) error
	Unlink(context.Context) error
}

func (a *API) SetConnectOperations(ops ConnectOperations) { a.connect = ops }

func New(service Service, model func(Caller) http.Handler) *API {
	return &API{service: service, model: model}
}

func (a *API) SetFilesRoute(route func(Caller) http.Handler) { a.files = route }

// SetGitRoute configures the per-box git route before opening sockets.
func (a *API) SetGitRoute(route func(Caller) http.Handler) { a.git = route }

type callerKey struct{}

func requestCaller(ctx context.Context, fallback Caller) Caller {
	if c, ok := ctx.Value(callerKey{}).(Caller); ok {
		return c
	}
	return fallback
}

func (a *API) handler(c Caller) http.Handler {
	mux := http.NewServeMux()
	call(mux, contract.MethodWebSearch, c, a.service.WebSearch)
	call(mux, contract.MethodWebsearchSetKey, c, a.service.WebsearchSetKey)
	call(mux, contract.MethodWebsearchRemoveKey, c, a.service.WebsearchRemoveKey)
	call(mux, contract.MethodConnectStart, c, func(ctx context.Context, _ contract.Principal, req contract.ConnectStartRequest) (contract.ConnectStartResponse, *contract.Error) {
		if a.connect == nil {
			return contract.ConnectStartResponse{}, &contract.Error{Code: contract.ErrUnavailable, Message: "Connect unavailable"}
		}
		if err := a.connect.Start(ctx, req); err != nil {
			return contract.ConnectStartResponse{}, &contract.Error{Code: contract.ErrUnavailable, Message: "Connect start failed"}
		}
		return contract.ConnectStartResponse{}, nil
	})
	call(mux, contract.MethodConnectStatus, c, func(ctx context.Context, _ contract.Principal, _ contract.ConnectStatusRequest) (contract.ConnectStatusResponse, *contract.Error) {
		if a.connect == nil {
			return contract.ConnectStatusResponse{}, &contract.Error{Code: contract.ErrUnavailable, Message: "Connect unavailable"}
		}
		out, err := a.connect.Status(ctx)
		if err != nil {
			return contract.ConnectStatusResponse{}, &contract.Error{Code: contract.ErrUnavailable, Message: "Connect status unavailable"}
		}
		if a.web != nil {
			routes, tunnel := a.web.routeOrigins(ctx)
			out.Routes = connect.RankedRoutes(routes, tunnel)
			out.Tunnel = tunnel
		}
		return out, nil
	})
	call(mux, contract.MethodConnectCancel, c, func(ctx context.Context, _ contract.Principal, _ contract.ConnectCancelRequest) (contract.ConnectCancelResponse, *contract.Error) {
		if a.connect == nil || a.connect.Cancel(ctx) != nil {
			return contract.ConnectCancelResponse{}, &contract.Error{Code: contract.ErrUnavailable, Message: "Connect cancel failed"}
		}
		return contract.ConnectCancelResponse{}, nil
	})
	call(mux, contract.MethodConnectUnlink, c, func(ctx context.Context, _ contract.Principal, _ contract.ConnectUnlinkRequest) (contract.ConnectUnlinkResponse, *contract.Error) {
		if a.connect == nil || a.connect.Unlink(ctx) != nil {
			return contract.ConnectUnlinkResponse{}, &contract.Error{Code: contract.ErrUnavailable, Message: "Connect unlink failed"}
		}
		return contract.ConnectUnlinkResponse{}, nil
	})
	call(mux, contract.MethodWebLogin, c, func(ctx context.Context, p contract.Principal, req contract.WebLoginRequest) (contract.WebLoginResponse, *contract.Error) {
		if a.web == nil {
			return contract.WebLoginResponse{}, &contract.Error{Code: contract.ErrUnavailable, Message: "the engine was started without --web.listen"}
		}
		return a.web.login(ctx, p, req)
	})
	call(mux, contract.MethodContextSnapshot, c, a.service.ContextSnapshot)
	call(mux, contract.MethodInstructionsGet, c, a.service.InstructionsGet)
	call(mux, contract.MethodInstructionsSet, c, a.service.InstructionsSet)
	call(mux, contract.MethodDevicePair, c, func(ctx context.Context, p contract.Principal, req contract.DevicePairRequest) (contract.DevicePairResponse, *contract.Error) {
		out, problem := a.service.DevicePair(ctx, p, req)
		if a.web != nil {
			out.Routes = a.web.PublicRoutes()
		}
		return out, problem
	})
	call(mux, contract.MethodDeviceList, c, a.service.DeviceList)
	call(mux, contract.MethodDeviceScopes, c, func(ctx context.Context, _ contract.Principal, _ contract.DeviceScopesRequest) (contract.DeviceScopesResponse, *contract.Error) {
		c := requestCaller(ctx, c)
		if c.Web {
			return contract.DeviceScopesResponse{Scopes: c.Scopes}, nil
		}
		return contract.DeviceScopesResponse{Scopes: store.FullDeviceScopes}, nil
	})
	call(mux, contract.MethodDeviceRevoke, c, a.service.DeviceRevoke)
	call(mux, contract.MethodDeviceSetScopes, c, a.service.DeviceSetScopes)
	call(mux, contract.MethodConversationRead, c, a.service.ConversationRead)
	call(mux, contract.MethodRoomSetLimits, c, a.service.RoomSetLimits)
	call(mux, contract.MethodRoomPause, c, a.service.RoomPause)
	call(mux, contract.MethodSessionStop, c, a.service.SessionStop)
	call(mux, contract.MethodRoomList, c, a.service.RoomList)
	call(mux, contract.MethodSessionList, c, a.service.SessionList)
	call(mux, contract.MethodHostexecRun, c, a.service.HostexecRun)
	call(mux, contract.MethodHostexecList, c, a.service.HostexecList)
	call(mux, contract.MethodOpSubmit, c, func(ctx context.Context, p contract.Principal, req contract.OpSubmitRequest) (contract.OpSubmitResponse, *contract.Error) {
		c := requestCaller(ctx, c)
		if c.Web {
			scope := a.service.OperationScope(req.Operation.Kind)
			if scope == "" || !hasScope(c, scope) {
				return contract.OpSubmitResponse{}, forbidden()
			}
		}
		if req.Operation.Kind == (contract.HostexecRunArgs{}).OpKind() {
			return contract.OpSubmitResponse{}, forbidden()
		}
		if box(c) {
			if req.Operation.Room == "" {
				req.Operation.Room = c.Room
			} else if req.Operation.Room != c.Room {
				return contract.OpSubmitResponse{}, forbidden()
			}
			if grant, ok := req.Operation.Args.(contract.HostexecGrantArgs); ok {
				grant.Room = c.Room
				if grant.To.Kind == "" {
					grant.To = c.Principal
				}
				req.Operation.Args = grant
			}
		}
		return a.service.OpSubmit(ctx, p, req)
	})
	call(mux, contract.MethodOpDecide, c, a.service.OpDecide)
	call(mux, contract.MethodOpWithdraw, c, a.service.OpWithdraw)
	call(mux, contract.MethodOpList, c, a.service.OpList)
	call(mux, contract.MethodHostAdd, c, a.service.HostAdd)
	call(mux, contract.MethodHostList, c, a.service.HostList)
	call(mux, contract.MethodProfileList, c, a.service.ProfileList)
	call(mux, contract.MethodProfileSet, c, a.service.ProfileSet)
	call(mux, contract.MethodProfileRemove, c, a.service.ProfileRemove)
	call(mux, contract.MethodAccountsList, c, a.service.AccountsList)
	call(mux, contract.MethodAccountsSetLimits, c, a.service.AccountsSetLimits)
	call(mux, contract.MethodAccountsRemove, c, a.service.AccountsRemove)
	call(mux, contract.MethodAccountsRename, c, a.service.AccountsRename)
	call(mux, contract.MethodAccountsConnect, c, a.service.AccountsConnect)
	call(mux, contract.MethodAccountsReconnect, c, a.service.AccountsReconnect)
	call(mux, contract.MethodAccountsConnectCallback, c, a.service.AccountsConnectCallback)
	call(mux, contract.MethodAccountsConnectStatus, c, a.service.AccountsConnectStatus)
	call(mux, contract.MethodAccountsSetKey, c, a.service.AccountsSetKey)
	call(mux, contract.MethodAccountsImportOAuth, c, a.service.AccountsImportOAuth)
	call(mux, contract.MethodAccountsQuota, c, a.service.AccountsQuota)
	call(mux, contract.MethodRoomCreate, c, a.service.RoomCreate)
	call(mux, contract.MethodRoomDelete, c, a.service.RoomDelete)
	call(mux, contract.MethodHostSetLimits, c, a.service.HostSetLimits)
	call(mux, contract.MethodRoomRepoAdd, c, a.service.RoomRepoAdd)
	call(mux, contract.MethodRoomRepos, c, a.service.RoomRepos)
	call(mux, contract.MethodRepoCISet, c, a.service.RepoCISet)
	call(mux, contract.MethodSessionStart, c, a.service.SessionStart)
	call(mux, contract.MethodSessionMove, c, a.service.SessionMove)
	call(mux, contract.MethodSessionClose, c, a.service.SessionClose)
	call(mux, contract.MethodSessionSetProfile, c, a.service.SessionSetProfile)
	call(mux, contract.MethodSessionRestart, c, a.service.SessionRestart)
	call(mux, contract.MethodSessionSearch, c, a.service.SessionSearch)
	call(mux, contract.MethodPostSearch, c, a.service.PostSearch)
	call(mux, contract.MethodPost, c, func(ctx context.Context, p contract.Principal, req contract.PostRequest) (contract.PostResponse, *contract.Error) {
		c := requestCaller(ctx, c)
		if box(c) {
			if req.Room == "" {
				req.Room = c.Room
			} else if req.Room != c.Room {
				return contract.PostResponse{}, forbidden()
			}
		}
		return a.service.Post(ctx, p, req)
	})
	call(mux, contract.MethodEventsList, c, func(ctx context.Context, p contract.Principal, req contract.EventsListRequest) (contract.EventsListResponse, *contract.Error) {
		c := requestCaller(ctx, c)
		if box(c) && req.Room != c.Room {
			return contract.EventsListResponse{}, forbidden()
		}
		return a.service.EventsList(ctx, p, req)
	})
	call(mux, contract.MethodShellOutput, c, a.service.ShellOutput)
	mux.HandleFunc("GET /v1/shell/watch", func(w http.ResponseWriter, r *http.Request) {
		c := requestCaller(r.Context(), c)
		w.Header().Set("Cache-Control", "no-store")
		if !owner(c) || !hasScope(c, contract.ScopeTerminal) {
			writeError(w, forbidden())
			return
		}
		id := contract.SessionID(r.URL.Query().Get("session"))
		if id == "" {
			writeError(w, invalid())
			return
		}
		ch, problem := a.service.ShellWatch(r.Context(), id)
		if problem != nil {
			writeError(w, problem)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		f, ok := w.(http.Flusher)
		if !ok {
			return
		}
		f.Flush()
		for job := range ch {
			data, err := json.Marshal(job)
			if err != nil {
				return
			}
			if _, err = fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
				return
			}
			f.Flush()
		}
	})
	call(mux, contract.MethodComputerChanged, c, a.service.ComputerChanged)
	// Input is a batched POST, not a WebSocket: the watch stream is one-way HTTP, a batch keeps a
	// mouse-move flood to one request per frame, and POST needs no new socket handling in the web listener.
	call(mux, contract.MethodComputerInput, c, func(ctx context.Context, p contract.Principal, req contract.ComputerInputRequest) (contract.ComputerInputResponse, *contract.Error) {
		if req.Session == "" || req.Frame.Width < 1 || req.Frame.Width > 16384 || req.Frame.Height < 1 || req.Frame.Height > 16384 || len(req.Events) == 0 || len(req.Events) > 256 {
			return contract.ComputerInputResponse{}, invalid()
		}
		for _, e := range req.Events {
			ok := false
			switch e.Kind {
			case contract.InputKeyDown, contract.InputKeyUp:
				ok = e.Key != "" && len(e.Key) <= 32
			case contract.InputChar:
				ok = e.Text != "" && len(e.Text) <= 1024
			case contract.InputMouseMove, contract.InputWheel:
				ok = true
			case contract.InputMouseDown, contract.InputMouseUp:
				ok = e.Button == contract.ButtonLeft || e.Button == contract.ButtonMiddle || e.Button == contract.ButtonRight
			}
			if !ok || e.X < 0 || e.X > float64(req.Frame.Width) || e.Y < 0 || e.Y > float64(req.Frame.Height) || e.Modifiers < 0 || e.Modifiers > 15 {
				return contract.ComputerInputResponse{}, invalid()
			}
		}
		return a.service.ComputerInput(ctx, p, req)
	})
	mux.HandleFunc("POST /v1/computers/frames", func(w http.ResponseWriter, r *http.Request) {
		c := requestCaller(r.Context(), c)
		if !box(c) {
			writeError(w, forbidden())
			return
		}
		generation, err := strconv.ParseUint(r.URL.Query().Get("generation"), 10, 64)
		if err != nil || generation == 0 {
			writeError(w, invalid())
			return
		}
		if err := a.service.ComputerFrames(r.Context(), c.Principal, generation, r.Body); err != nil {
			writeError(w, err)
		}
	})
	mux.HandleFunc("GET /v1/terminal", func(w http.ResponseWriter, r *http.Request) {
		c := requestCaller(r.Context(), c)
		if !owner(c) || !hasScope(c, contract.ScopeTerminal) {
			writeError(w, forbidden())
			return
		}
		q := r.URL.Query()
		session, host := contract.SessionID(q.Get("box")), contract.HostName(q.Get("host"))
		cols, e1 := strconv.Atoi(q.Get("cols"))
		rows, e2 := strconv.Atoi(q.Get("rows"))
		if (session == "") == (host == "") || e1 != nil || e2 != nil || cols < 1 || cols > 1000 || rows < 1 || rows > 1000 {
			writeError(w, invalid())
			return
		}
		argv, release, err := a.service.Terminal(r.Context(), session, host)
		if err != nil {
			writeError(w, err)
			return
		}
		defer release()
		terminal(w, r, argv, uint16(cols), uint16(rows))
	})
	mux.HandleFunc("GET /v1/computers/watch", func(w http.ResponseWriter, r *http.Request) {
		c := requestCaller(r.Context(), c)
		w.Header().Set("Cache-Control", "no-store")
		if !owner(c) || !c.Web || !hasScope(c, contract.ScopeComputers) {
			writeError(w, forbidden())
			return
		}
		session := contract.SessionID(r.URL.Query().Get("session"))
		if session == "" {
			writeError(w, invalid())
			return
		}
		frames, err := a.service.ComputerWatch(r.Context(), session)
		if err != nil {
			writeError(w, err)
			return
		}
		multi := multipart.NewWriter(w)
		_ = multi.SetBoundary("frame")
		w.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary=frame")
		w.WriteHeader(http.StatusOK)
		f, ok := w.(http.Flusher)
		if !ok {
			return
		}
		f.Flush()
		header := textproto.MIMEHeader{"Content-Type": []string{"image/jpeg"}}
		for frame := range frames {
			part, err := multi.CreatePart(header)
			if err != nil {
				return
			}
			if _, err = part.Write(frame); err != nil {
				return
			}
			f.Flush()
		}
	})
	call(mux, contract.MethodBoxHello, c, a.service.BoxHello)
	call(mux, contract.MethodTurnOutput, c, a.service.TurnOutput)
	call(mux, contract.MethodTurnActivity, c, a.service.TurnActivity)
	call(mux, contract.MethodDeliveryDone, c, a.service.DeliveryDone)
	call(mux, contract.MethodArchiveDone, c, a.service.ArchiveDone)
	call(mux, contract.MethodTranscriptAppend, c, a.service.TranscriptAppend)
	call(mux, contract.MethodTranscriptFetch, c, a.service.TranscriptFetch)
	call(mux, contract.MethodTranscriptRead, c, a.service.TranscriptRead)
	call(mux, contract.MethodSessionWork, c, a.service.SessionWork)
	call(mux, contract.MethodShellJobEnded, c, a.service.ShellJobEnded)
	mux.HandleFunc("GET /v1/events", func(w http.ResponseWriter, r *http.Request) {
		c := requestCaller(r.Context(), c)
		if !user(c) || !hasScope(c, contract.ScopeRead) {
			writeError(w, forbidden())
			return
		}
		room := contract.RoomID(r.URL.Query().Get("room"))
		if c.Principal.Kind == contract.PrincipalSession && (room == "" || room != c.Room) {
			writeError(w, forbidden())
			return
		}
		var after int64
		s := r.URL.Query().Get("after")
		if r.Header.Get("Last-Event-ID") != "" {
			s = r.Header.Get("Last-Event-ID")
		}
		if s != "" {
			n, err := strconv.ParseInt(s, 10, 64)
			if err != nil || n < 0 {
				writeError(w, invalid())
				return
			}
			after = n
		}
		ch, err := a.service.Events(r.Context(), c.Principal, room, after)
		if err != nil {
			writeError(w, err)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		f, ok := w.(http.Flusher)
		if !ok {
			return
		}
		data, marshalErr := json.Marshal(contract.StreamHello{Build: a.Build})
		if marshalErr != nil {
			return
		}
		if _, writeErr := fmt.Fprintf(w, "event: hello\ndata: %s\n\n", data); writeErr != nil {
			return
		}
		f.Flush()
		ticker := time.NewTicker(25 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
					return
				}
				f.Flush()
			case <-r.Context().Done():
				return
			case event, ok := <-ch:
				if !ok {
					return
				}
				data, err := json.Marshal(event)
				if err != nil {
					return
				}
				if event.Seq != 0 {
					if _, err = fmt.Fprintf(w, "id: %d\n", event.Seq); err != nil {
						return
					}
				}
				if _, err = fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
					return
				}
				f.Flush()
			}
		}
	})
	mux.HandleFunc("GET /v1/box/channel", func(w http.ResponseWriter, r *http.Request) {
		c := requestCaller(r.Context(), c)
		if !box(c) {
			writeError(w, forbidden())
			return
		}
		ch, err := a.service.BoxChannel(r.Context(), c.Principal)
		if err != nil {
			writeError(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.WriteHeader(http.StatusOK)
		f, ok := w.(http.Flusher)
		if !ok {
			return
		}
		f.Flush()
		enc := json.NewEncoder(w)
		for {
			select {
			case <-r.Context().Done():
				return
			case message, ok := <-ch:
				if !ok {
					return
				}
				if enc.Encode(message) != nil {
					return
				}
				f.Flush()
			}
		}
	})
	if user(c) && a.files != nil {
		files := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c := requestCaller(r.Context(), c)
			scope := contract.ScopeFiles
			switch r.Method {
			case http.MethodGet, http.MethodHead:
				scope = contract.ScopeRead
			case http.MethodPost:
				scope = contract.ScopeOperate
			}
			if !hasScope(c, scope) {
				writeError(w, forbidden())
				return
			}
			a.files(c).ServeHTTP(w, r)
		})
		mux.Handle("/v1/files", files)
		mux.Handle("/v1/files/", files)
	}
	if box(c) && a.git != nil {
		mux.Handle("/git/", a.git(c))
	}
	if box(c) && a.model != nil {
		mux.HandleFunc("/model/", func(w http.ResponseWriter, r *http.Request) { a.model(c).ServeHTTP(w, r) })
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, &contract.Error{Code: contract.ErrNotFound, Message: "route not found"})
	})
	return mux
}

func hasScope(c Caller, scope contract.Scope) bool {
	if !c.Web {
		return true
	}
	for _, held := range c.Scopes {
		if held == scope {
			return true
		}
	}
	return false
}

func owner(c Caller) bool { return c.Principal.Kind == contract.PrincipalOwner }
func box(c Caller) bool {
	return c.Principal.Kind == contract.PrincipalSession && c.Box != "" && c.Principal.Session != ""
}
func user(c Caller) bool { return owner(c) || box(c) }

// allows checks a method's access rule from the registry table.
func allows(access tools.Access, c Caller) bool {
	switch access {
	case tools.OwnerAccess:
		return owner(c)
	case tools.UserAccess:
		return user(c)
	case tools.BoxAccess:
		return box(c)
	case tools.OwnerLocalAccess:
		return owner(c) && !c.Web
	}
	return false
}

// call registers one method route. The registry's method table is the one
// authorization source; op.submit and device.scopes name no scope because
// their handlers check it themselves, by operation kind or for any owner device.
func call[Req, Resp any](mux *http.ServeMux, m contract.Method, c Caller, fn func(context.Context, contract.Principal, Req) (Resp, *contract.Error)) {
	rule, ok := tools.MethodRule(m)
	if !ok {
		panic("no method table entry for " + string(m))
	}
	mux.HandleFunc("POST /v1/"+string(m), func(w http.ResponseWriter, r *http.Request) {
		c := requestCaller(r.Context(), c)
		if !allows(rule.Access, c) || rule.Scope != "" && !hasScope(c, rule.Scope) {
			writeError(w, forbidden())
			return
		}
		var req *Req
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil || req == nil {
			writeError(w, invalid())
			return
		}
		var extra json.RawMessage
		if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
			writeError(w, invalid())
			return
		}
		result, err := fn(r.Context(), c.Principal, *req)
		if err != nil {
			writeError(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if m == contract.MethodConnectStatus {
			w.Header().Set("Cache-Control", "no-store")
		}
		_ = json.NewEncoder(w).Encode(result)
	})
}

func invalid() *contract.Error {
	return &contract.Error{Code: contract.ErrInvalid, Message: "invalid request"}
}
func forbidden() *contract.Error {
	return &contract.Error{Code: contract.ErrForbidden, Message: "forbidden"}
}
func writeError(w http.ResponseWriter, err *contract.Error) {
	status := http.StatusInternalServerError
	switch err.Code {
	case contract.ErrInvalid:
		status = http.StatusBadRequest
	case contract.ErrForbidden, contract.ErrNoGrant:
		status = http.StatusForbidden
	case contract.ErrNotFound:
		status = http.StatusNotFound
	case contract.ErrConflict, contract.ErrRoomLimited, contract.ErrProfileExists, contract.ErrStale, contract.ErrVisualSuperseded, contract.ErrMoveBusy, contract.ErrHasChildren, contract.ErrParentClosing, contract.ErrSessionClosing:
		status = http.StatusConflict
	case contract.ErrUnavailable, contract.ErrReconnectUnavailable:
		status = http.StatusServiceUnavailable
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(err)
}
