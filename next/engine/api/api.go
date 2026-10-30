// Package api serves the engine's local HTTP API.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/resident-labs/dyna/contract"
)

type API struct {
	service Service
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
	if a.web != nil {
		call(mux, contract.MethodWebLogin, c, owner, a.web.login)
	}
	call(mux, contract.MethodRoomList, c, owner, a.service.RoomList)
	call(mux, contract.MethodSessionList, c, user, a.service.SessionList)
	call(mux, contract.MethodOpSubmit, c, user, a.service.OpSubmit)
	call(mux, contract.MethodOpDecide, c, user, a.service.OpDecide)
	call(mux, contract.MethodOpWithdraw, c, user, a.service.OpWithdraw)
	call(mux, contract.MethodOpList, c, user, a.service.OpList)
	call(mux, contract.MethodHostAdd, c, owner, a.service.HostAdd)
	call(mux, contract.MethodRoomCreate, c, owner, a.service.RoomCreate)
	call(mux, contract.MethodRoomRepoAdd, c, owner, a.service.RoomRepoAdd)
	call(mux, contract.MethodRoomRepos, c, owner, a.service.RoomRepos)
	call(mux, contract.MethodSessionStart, c, owner, a.service.SessionStart)
	call(mux, contract.MethodSessionClose, c, user, a.service.SessionClose)
	call(mux, contract.MethodPost, c, user, func(ctx context.Context, p contract.Principal, req contract.PostRequest) (contract.PostResponse, *contract.Error) {
		if box(c) {
			if req.Room == "" {
				req.Room = c.Room
			} else if req.Room != c.Room {
				return contract.PostResponse{}, forbidden()
			}
		}
		return a.service.Post(ctx, p, req)
	})
	call(mux, contract.MethodEventsList, c, user, func(ctx context.Context, p contract.Principal, req contract.EventsListRequest) (contract.EventsListResponse, *contract.Error) {
		if box(c) && req.Room != c.Room {
			return contract.EventsListResponse{}, forbidden()
		}
		return a.service.EventsList(ctx, p, req)
	})
	call(mux, contract.MethodBoxHello, c, box, a.service.BoxHello)
	call(mux, contract.MethodTurnOutput, c, box, a.service.TurnOutput)
	call(mux, contract.MethodDeliveryDone, c, box, a.service.DeliveryDone)
	call(mux, contract.MethodArchiveDone, c, box, a.service.ArchiveDone)
	call(mux, contract.MethodTranscriptAppend, c, box, a.service.TranscriptAppend)
	call(mux, contract.MethodShellJobEnded, c, box, a.service.ShellJobEnded)
	mux.HandleFunc("GET /v1/events", func(w http.ResponseWriter, r *http.Request) {
		if !user(c) {
			writeError(w, forbidden())
			return
		}
		room := contract.RoomID(r.URL.Query().Get("room"))
		if room == "" || (c.Principal.Kind == contract.PrincipalSession && room != c.Room) {
			if room == "" {
				writeError(w, invalid())
			} else {
				writeError(w, forbidden())
			}
			return
		}
		var after int64
		if s := r.URL.Query().Get("after"); s != "" {
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
		f.Flush()
		for {
			select {
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
				if _, err = fmt.Fprintf(w, "id: %d\ndata: %s\n\n", event.Seq, data); err != nil {
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
		mux.Handle("/v1/files", a.files(c))
		mux.Handle("/v1/files/", a.files(c))
	}
	if box(c) && a.git != nil {
		mux.Handle("/git/", a.git(c))
	}
	if box(c) && a.model != nil {
		mux.Handle("/model/", a.model(c))
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, &contract.Error{Code: contract.ErrNotFound, Message: "route not found"})
	})
	return mux
}

func owner(c Caller) bool { return c.Principal.Kind == contract.PrincipalOwner }
func box(c Caller) bool {
	return c.Principal.Kind == contract.PrincipalSession && c.Box != "" && c.Principal.Session != ""
}
func user(c Caller) bool { return owner(c) || box(c) }

func call[Req, Resp any](mux *http.ServeMux, method contract.Method, c Caller, allowed func(Caller) bool, fn func(context.Context, contract.Principal, Req) (Resp, *contract.Error)) {
	mux.HandleFunc("POST /v1/"+string(method), func(w http.ResponseWriter, r *http.Request) {
		if !allowed(c) {
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
	case contract.ErrForbidden:
		status = http.StatusForbidden
	case contract.ErrNotFound:
		status = http.StatusNotFound
	case contract.ErrConflict, contract.ErrStale:
		status = http.StatusConflict
	case contract.ErrUnavailable:
		status = http.StatusServiceUnavailable
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(err)
}
