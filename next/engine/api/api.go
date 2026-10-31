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
	"github.com/resident-labs/dyna/engine/store"
)

type API struct {
	service Service
	Build   string
	model   func(Caller) http.Handler
	git     func(Caller) http.Handler
	web     *Web
	files   func(Caller) http.Handler
}

func New(service Service, model func(Caller) http.Handler) *API {
	return &API{service: service, model: model}
}

func (a *API) SetFilesRoute(route func(Caller) http.Handler) { a.files = route }

// SetGitRoute configures the per-box git route before opening sockets.
func (a *API) SetGitRoute(route func(Caller) http.Handler) { a.git = route }

func (a *API) handler(c Caller) http.Handler {
	mux := http.NewServeMux()
	call(mux, contract.MethodWebLogin, c, contract.ScopeAdmin, func(c Caller) bool { return owner(c) && !c.Web }, func(ctx context.Context, p contract.Principal, req contract.WebLoginRequest) (contract.WebLoginResponse, *contract.Error) {
		if a.web == nil {
			return contract.WebLoginResponse{}, &contract.Error{Code: contract.ErrUnavailable, Message: "the engine was started without --web.listen"}
		}
		return a.web.login(ctx, p, req)
	})
	call(mux, contract.MethodDevicePair, c, contract.ScopeAdmin, owner, func(ctx context.Context, p contract.Principal, req contract.DevicePairRequest) (contract.DevicePairResponse, *contract.Error) {
		out, problem := a.service.DevicePair(ctx, p, req)
		if a.web != nil {
			out.Routes = a.web.publicRoutes()
		}
		return out, problem
	})
	call(mux, contract.MethodDeviceList, c, contract.ScopeAdmin, owner, a.service.DeviceList)
	call(mux, contract.MethodDeviceScopes, c, contract.ScopeRead, owner, func(context.Context, contract.Principal, contract.DeviceScopesRequest) (contract.DeviceScopesResponse, *contract.Error) {
		if c.Web {
			return contract.DeviceScopesResponse{Scopes: c.Scopes}, nil
		}
		return contract.DeviceScopesResponse{Scopes: store.FullDeviceScopes}, nil
	})
	call(mux, contract.MethodDeviceRevoke, c, contract.ScopeAdmin, owner, a.service.DeviceRevoke)
	call(mux, contract.MethodDeviceSetScopes, c, contract.ScopeAdmin, owner, a.service.DeviceSetScopes)
	call(mux, contract.MethodRuleAdd, c, contract.ScopeAdmin, user, func(ctx context.Context, p contract.Principal, req contract.RuleAddRequest) (contract.RuleAddResponse, *contract.Error) {
		if box(c) && req.Room == "" {
			req.Room = c.Room
		}
		return a.service.RuleAdd(ctx, p, req)
	})
	call(mux, contract.MethodRuleRemove, c, contract.ScopeAdmin, user, func(ctx context.Context, p contract.Principal, req contract.RuleRemoveRequest) (contract.RuleRemoveResponse, *contract.Error) {
		if box(c) && req.Room == "" {
			req.Room = c.Room
		}
		return a.service.RuleRemove(ctx, p, req)
	})
	call(mux, contract.MethodRuleEdit, c, contract.ScopeAdmin, user, func(ctx context.Context, p contract.Principal, req contract.RuleEditRequest) (contract.RuleEditResponse, *contract.Error) {
		if box(c) && req.Room == "" {
			req.Room = c.Room
		}
		return a.service.RuleEdit(ctx, p, req)
	})
	call(mux, contract.MethodRuleList, c, contract.ScopeRead, user, func(ctx context.Context, p contract.Principal, req contract.RuleListRequest) (contract.RuleListResponse, *contract.Error) {
		if box(c) && req.Room == "" {
			req.Room = c.Room
		}
		return a.service.RuleList(ctx, p, req)
	})
	call(mux, contract.MethodConversationRead, c, contract.ScopeOperate, owner, a.service.ConversationRead)
	call(mux, contract.MethodRoomSetLimits, c, contract.ScopeAdmin, owner, a.service.RoomSetLimits)
	call(mux, contract.MethodRoomPause, c, contract.ScopeOperate, user, a.service.RoomPause)
	call(mux, contract.MethodSessionStop, c, contract.ScopeOperate, user, a.service.SessionStop)
	call(mux, contract.MethodRoomList, c, contract.ScopeRead, owner, a.service.RoomList)
	call(mux, contract.MethodSessionList, c, contract.ScopeRead, user, a.service.SessionList)
	call(mux, contract.MethodHostexecRun, c, contract.ScopeRead, box, a.service.HostexecRun)
	call(mux, contract.MethodHostexecRevoke, c, contract.ScopeAdmin, owner, a.service.HostexecRevoke)
	call(mux, contract.MethodOpSubmit, c, contract.ScopeOperate, user, func(ctx context.Context, p contract.Principal, req contract.OpSubmitRequest) (contract.OpSubmitResponse, *contract.Error) {
		if c.Web {
			scope := a.service.OperationScope(req.Operation.Kind)
			if scope == "" || !hasScope(c, scope) {
				return contract.OpSubmitResponse{}, forbidden()
			}
		}
		if req.Operation.Kind == "hostexec.run" {
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
	call(mux, contract.MethodOpDecide, c, contract.ScopeAdmin, user, a.service.OpDecide)
	call(mux, contract.MethodOpWithdraw, c, contract.ScopeOperate, user, a.service.OpWithdraw)
	call(mux, contract.MethodOpList, c, contract.ScopeRead, user, a.service.OpList)
	call(mux, contract.MethodHostAdd, c, contract.ScopeAdmin, owner, a.service.HostAdd)
	call(mux, contract.MethodHostList, c, contract.ScopeRead, owner, a.service.HostList)
	call(mux, contract.MethodProfileList, c, contract.ScopeRead, owner, a.service.ProfileList)
	call(mux, contract.MethodAccountsList, c, contract.ScopeRead, owner, a.service.AccountsList)
	call(mux, contract.MethodAccountsRemove, c, contract.ScopeAdmin, owner, a.service.AccountsRemove)
	call(mux, contract.MethodAccountsConnect, c, contract.ScopeAdmin, owner, a.service.AccountsConnect)
	call(mux, contract.MethodAccountsConnectCallback, c, contract.ScopeAdmin, owner, a.service.AccountsConnectCallback)
	call(mux, contract.MethodAccountsConnectStatus, c, contract.ScopeRead, owner, a.service.AccountsConnectStatus)
	call(mux, contract.MethodAccountsSetKey, c, contract.ScopeAdmin, owner, a.service.AccountsSetKey)
	call(mux, contract.MethodAccountsQuota, c, contract.ScopeRead, owner, a.service.AccountsQuota)
	call(mux, contract.MethodRoomCreate, c, contract.ScopeAdmin, owner, a.service.RoomCreate)
	call(mux, contract.MethodRoomRepoAdd, c, contract.ScopeAdmin, owner, a.service.RoomRepoAdd)
	call(mux, contract.MethodRoomRepos, c, contract.ScopeRead, owner, a.service.RoomRepos)
	call(mux, contract.MethodSessionStart, c, contract.ScopeOperate, user, a.service.SessionStart)
	call(mux, contract.MethodSessionMove, c, contract.ScopeOperate, user, a.service.SessionMove)
	call(mux, contract.MethodSessionClose, c, contract.ScopeOperate, user, a.service.SessionClose)
	call(mux, contract.MethodSessionSetProfile, c, contract.ScopeOperate, user, a.service.SessionSetProfile)
	call(mux, contract.MethodSessionRestart, c, contract.ScopeOperate, owner, a.service.SessionRestart)
	call(mux, contract.MethodSessionSearch, c, contract.ScopeRead, owner, a.service.SessionSearch)
	call(mux, contract.MethodPostSearch, c, contract.ScopeRead, owner, a.service.PostSearch)
	call(mux, contract.MethodPost, c, contract.ScopeOperate, user, func(ctx context.Context, p contract.Principal, req contract.PostRequest) (contract.PostResponse, *contract.Error) {
		if box(c) {
			if req.Room == "" {
				req.Room = c.Room
			} else if req.Room != c.Room {
				return contract.PostResponse{}, forbidden()
			}
		}
		return a.service.Post(ctx, p, req)
	})
	call(mux, contract.MethodEventsList, c, contract.ScopeRead, user, func(ctx context.Context, p contract.Principal, req contract.EventsListRequest) (contract.EventsListResponse, *contract.Error) {
		if box(c) && req.Room != c.Room {
			return contract.EventsListResponse{}, forbidden()
		}
		return a.service.EventsList(ctx, p, req)
	})
	call(mux, contract.MethodShellOutput, c, contract.ScopeRead, box, a.service.ShellOutput)
	mux.HandleFunc("GET /v1/shell/watch", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if !owner(c) || !hasScope(c, contract.ScopeRead) {
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
	call(mux, contract.MethodComputerChanged, c, contract.ScopeRead, box, a.service.ComputerChanged)
	// Input is a batched POST, not a WebSocket: the watch stream is one-way HTTP, a batch keeps a
	// mouse-move flood to one request per frame, and POST needs no new socket handling in the web listener.
	call(mux, contract.MethodComputerInput, c, contract.ScopeComputers, owner, func(ctx context.Context, p contract.Principal, req contract.ComputerInputRequest) (contract.ComputerInputResponse, *contract.Error) {
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
	call(mux, contract.MethodBoxHello, c, contract.ScopeRead, box, a.service.BoxHello)
	call(mux, contract.MethodTurnOutput, c, contract.ScopeRead, box, a.service.TurnOutput)
	call(mux, contract.MethodTurnActivity, c, contract.ScopeRead, box, a.service.TurnActivity)
	call(mux, contract.MethodDeliveryDone, c, contract.ScopeRead, box, a.service.DeliveryDone)
	call(mux, contract.MethodArchiveDone, c, contract.ScopeRead, box, a.service.ArchiveDone)
	call(mux, contract.MethodTranscriptAppend, c, contract.ScopeRead, box, a.service.TranscriptAppend)
	call(mux, contract.MethodTranscriptFetch, c, contract.ScopeRead, box, a.service.TranscriptFetch)
	call(mux, contract.MethodTranscriptRead, c, contract.ScopeRead, owner, a.service.TranscriptRead)
	call(mux, contract.MethodShellJobEnded, c, contract.ScopeRead, box, a.service.ShellJobEnded)
	mux.HandleFunc("GET /v1/events", func(w http.ResponseWriter, r *http.Request) {
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

// op.submit checks its own scope by operation kind. device.scopes answers every owner device, whatever it holds, so a client can learn
// what it may use.
func call[Req, Resp any](mux *http.ServeMux, method contract.Method, c Caller, scope contract.Scope, allowed func(Caller) bool, fn func(context.Context, contract.Principal, Req) (Resp, *contract.Error)) {
	mux.HandleFunc("POST /v1/"+string(method), func(w http.ResponseWriter, r *http.Request) {
		if !allowed(c) || (method != contract.MethodOpSubmit && method != contract.MethodDeviceScopes && !hasScope(c, scope)) {
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
	case contract.ErrForbidden, contract.ErrHostexecScope, contract.ErrNoGrant:
		status = http.StatusForbidden
	case contract.ErrNotFound:
		status = http.StatusNotFound
	case contract.ErrConflict, contract.ErrStale, contract.ErrVisualSuperseded, contract.ErrMoveBusy, contract.ErrHasChildren, contract.ErrParentClosing, contract.ErrSessionClosing:
		status = http.StatusConflict
	case contract.ErrUnavailable:
		status = http.StatusServiceUnavailable
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(err)
}
