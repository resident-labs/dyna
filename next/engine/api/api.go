// Package api serves the engine's local HTTP API.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/contract/tools"
	"github.com/resident-labs/dyna/engine/connect"
	"github.com/resident-labs/dyna/engine/githubwrites"
	"github.com/resident-labs/dyna/engine/store"
)

type API struct {
	service    Service
	Build      string
	model      func(Caller) http.Handler
	localModel http.Handler
	git        func(Caller) http.Handler
	github     http.Handler
	web        *Web
	files      func(Caller) http.Handler
	connect    ConnectOperations
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

// SetLocalModelRoute configures the owner socket's model route before opening sockets.
func (a *API) SetLocalModelRoute(route http.Handler) { a.localModel = route }

// SetGitHubRead configures the GitHub route of a Run's socket before opening sockets.
func (a *API) SetGitHubRead(route http.Handler) { a.github = route }

// githubTools serves GitHub's read-only MCP tools to a box. The service holds them, because a box's hello says whether they are
// there; a service without them, such as a test's, has none.
func (a *API) githubTools(c Caller, w http.ResponseWriter, r *http.Request) {
	if held, ok := a.service.(interface{ GitHubTools() http.Handler }); ok {
		if tools := held.GitHubTools(); tools != nil {
			// The room is the socket's: the engine fixed it when it opened this socket for the box, and the request has no say.
			var grant contract.GitHubWrites
			if writes, ok := a.service.(interface {
				GitHubWritesOf(context.Context, contract.RoomID) (contract.GitHubWrites, error)
			}); ok {
				var err error
				if grant, err = writes.GitHubWritesOf(r.Context(), c.Room); err != nil {
					writeError(w, &contract.Error{Code: contract.ErrUnavailable, Message: "the room's githubWrites setting could not be read"})
					return
				}
			}
			tools.ServeHTTP(w, r.WithContext(githubwrites.WithAllowed(r.Context(), grant)))
			return
		}
	}
	writeError(w, &contract.Error{Code: contract.ErrUnavailable, Message: "GitHub reads are not available: the engine has no GitHub token"})
}

// SetGitRoute configures the per-box git route before opening sockets.
func (a *API) SetGitRoute(route func(Caller) http.Handler) { a.git = route }

// runTrackRequest is a Run's pr.track or issue.track. A Run lists no sessions, so it names one as a post does.
type runTrackRequest struct {
	Repo    contract.RepoName `json:"repo"`
	Number  int               `json:"number"`
	Session string            `json:"session,omitempty"`
}

// runCall serves one path of a Run's socket.
func runCall[Req, Res any](mux *http.ServeMux, method contract.Method, fn func(context.Context, Req) (Res, *contract.Error)) {
	mux.HandleFunc("POST /v1/"+string(method), func(w http.ResponseWriter, r *http.Request) {
		var req Req
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil {
			writeError(w, badBody(err))
			return
		}
		result, err := fn(r.Context(), req)
		if err != nil {
			writeError(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(result)
	})
}

// runHandler is all a Run's socket serves. POST /v1/post posts in the room, and POST /v1/pr.track and /v1/issue.track
// track a PR or an issue for one of its sessions. Every other path is GitHub's, because gh sends its API paths unchanged
// over a Unix socket. The method table is not involved: no method is open to a Run.
func (a *API) runHandler(sub contract.SubscriptionID, room contract.RoomID) http.Handler {
	mux := http.NewServeMux()
	runCall(mux, contract.MethodPost, func(ctx context.Context, req contract.PostRequest) (contract.PostResponse, *contract.Error) {
		return a.service.HookPost(ctx, sub, room, req)
	})
	runCall(mux, contract.MethodPrTrack, func(ctx context.Context, req runTrackRequest) (contract.PrTrackResponse, *contract.Error) {
		return a.service.HookTrack(ctx, room, req.Session, contract.PrTrackRequest{Repo: req.Repo, Number: req.Number})
	})
	runCall(mux, contract.MethodIssueTrack, func(ctx context.Context, req runTrackRequest) (contract.IssueTrackResponse, *contract.Error) {
		return a.service.HookTrackIssue(ctx, room, req.Session, contract.IssueTrackRequest{Repo: req.Repo, Number: req.Number})
	})
	mux.HandleFunc("/v1/", func(w http.ResponseWriter, _ *http.Request) { writeError(w, forbidden()) })
	if a.github != nil {
		mux.Handle("/", a.github)
	}
	return mux
}

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
	call(mux, contract.MethodGoalSet, c, a.service.GoalSet)
	call(mux, contract.MethodGoalList, c, a.service.GoalList)
	call(mux, contract.MethodGoalItem, c, a.service.GoalItem)
	call(mux, contract.MethodGoalDone, c, a.service.GoalDone)
	call(mux, contract.MethodGoalJournal, c, a.service.GoalJournal)
	call(mux, contract.MethodWorkflowRun, c, a.service.WorkflowRun)
	call(mux, contract.MethodWorkflowReload, c, a.service.WorkflowReload)
	call(mux, contract.MethodWorkflowStatus, c, a.service.WorkflowStatus)
	call(mux, contract.MethodWorkflowStep, c, a.service.WorkflowStep)
	call(mux, contract.MethodWorkflowList, c, a.service.WorkflowList)
	call(mux, contract.MethodWorkflowPin, c, a.service.WorkflowPin)
	call(mux, contract.MethodWorkflowStepStart, c, a.service.WorkflowStepStart)
	call(mux, contract.MethodWorkflowStepEnd, c, a.service.WorkflowStepEnd)
	call(mux, contract.MethodWorkflowStepTranscript, c, a.service.WorkflowStepTranscript)
	call(mux, contract.MethodWorkflowSetStatus, c, a.service.WorkflowSetStatus)
	call(mux, contract.MethodSubscriptionAdd, c, a.service.SubscriptionAdd)
	call(mux, contract.MethodSubscriptionList, c, a.service.SubscriptionList)
	call(mux, contract.MethodSubscriptionRemove, c, a.service.SubscriptionRemove)
	call(mux, contract.MethodSubscriptionSetEnabled, c, a.service.SubscriptionSetEnabled)
	call(mux, contract.MethodAddonList, c, a.service.AddonList)
	call(mux, contract.MethodAddonSetEnabled, c, a.service.AddonSetEnabled)
	call(mux, contract.MethodAddonSetRootOnly, c, a.service.AddonSetRootOnly)
	call(mux, contract.MethodAddonSetHosts, c, a.service.AddonSetHosts)
	call(mux, contract.MethodBoxNotice, c, a.service.BoxNotice)
	call(mux, contract.MethodMCPAdd, c, a.service.MCPAdd)
	call(mux, contract.MethodMCPList, c, a.service.MCPList)
	call(mux, contract.MethodMCPRemove, c, a.service.MCPRemove)
	call(mux, contract.MethodPackageAdd, c, a.service.PackageAdd)
	call(mux, contract.MethodPackageList, c, a.service.PackageList)
	call(mux, contract.MethodPackageRemove, c, a.service.PackageRemove)
	call(mux, contract.MethodSkillList, c, a.service.SkillList)
	call(mux, contract.MethodSkillRemove, c, a.service.SkillRemove)
	call(mux, contract.MethodSkillSave, c, a.service.SkillSave)
	call(mux, contract.MethodImageList, c, a.service.ImageList)
	call(mux, contract.MethodImageRemove, c, a.service.ImageRemove)
	call(mux, contract.MethodImageSave, c, a.service.ImageSave)
	call(mux, contract.MethodImageShow, c, a.service.ImageShow)
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
			return contract.DeviceScopesResponse{Scopes: c.Scopes, Host: c.Host}, nil
		}
		return contract.DeviceScopesResponse{Scopes: store.FullDeviceScopes}, nil
	})
	call(mux, contract.MethodDeviceRevoke, c, a.service.DeviceRevoke)
	call(mux, contract.MethodDeviceSetScopes, c, a.service.DeviceSetScopes)
	call(mux, contract.MethodDeviceSetHost, c, a.service.DeviceSetHost)
	call(mux, contract.MethodConversationRead, c, a.service.ConversationRead)
	call(mux, contract.MethodRoomSetLimits, c, a.service.RoomSetLimits)
	call(mux, contract.MethodRoomPause, c, a.service.RoomPause)
	call(mux, contract.MethodSessionStop, c, a.service.SessionStop)
	call(mux, contract.MethodRoomList, c, a.service.RoomList)
	call(mux, contract.MethodSessionList, c, a.service.SessionList)
	call(mux, contract.MethodHostexecRun, c, a.service.HostexecRun)
	call(mux, contract.MethodHostexecStop, c, a.service.HostexecStop)
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
			if problem := pinRoom(&req.Operation.Room, c); problem != nil {
				return contract.OpSubmitResponse{}, problem
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
	call(mux, contract.MethodQuestionAsk, c, a.service.QuestionAsk)
	call(mux, contract.MethodQuestionAnswer, c, a.service.QuestionAnswer)
	call(mux, contract.MethodQuestionList, c, a.service.QuestionList)
	call(mux, contract.MethodHostAdd, c, a.service.HostAdd)
	call(mux, contract.MethodHostList, c, a.service.HostList)
	call(mux, contract.MethodHostRemove, c, a.service.HostRemove)
	call(mux, contract.MethodPoolAdd, c, a.service.PoolAdd)
	call(mux, contract.MethodPoolList, c, a.service.PoolList)
	call(mux, contract.MethodPoolRemove, c, a.service.PoolRemove)
	call(mux, contract.MethodPoolSet, c, a.service.PoolSet)
	call(mux, contract.MethodServiceSet, c, a.service.ServiceSet)
	call(mux, contract.MethodServiceList, c, a.service.ServiceList)
	call(mux, contract.MethodServiceShow, c, a.service.ServiceShow)
	call(mux, contract.MethodServiceLogs, c, a.service.ServiceLogs)
	call(mux, contract.MethodServiceRestart, c, a.service.ServiceRestart)
	call(mux, contract.MethodServiceStop, c, a.service.ServiceStop)
	call(mux, contract.MethodServiceStart, c, a.service.ServiceStart)
	call(mux, contract.MethodServiceUpdate, c, a.service.ServiceUpdate)
	call(mux, contract.MethodServiceRollback, c, a.service.ServiceRollback)
	call(mux, contract.MethodServiceRemove, c, a.service.ServiceRemove)
	call(mux, contract.MethodServiceSecretSet, c, a.service.ServiceSecretSet)
	call(mux, contract.MethodWorkflowSettings, c, a.service.WorkflowSettings)
	call(mux, contract.MethodWorkflowSet, c, a.service.WorkflowSet)
	call(mux, contract.MethodRoomHosts, c, a.service.RoomHosts)
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
	call(mux, contract.MethodProxyKey, c, a.service.ProxyKey)
	call(mux, contract.MethodProxyUsage, c, a.service.ProxyUsage)
	call(mux, contract.MethodRoomCreate, c, a.service.RoomCreate)
	call(mux, contract.MethodRoomDelete, c, a.service.RoomDelete)
	call(mux, contract.MethodHostSetLimits, c, a.service.HostSetLimits)
	call(mux, contract.MethodHostSet, c, a.service.HostSet)
	call(mux, contract.MethodRoomSet, c, a.service.RoomSet)
	call(mux, contract.MethodRoomRepoAdd, c, a.service.RoomRepoAdd)
	call(mux, contract.MethodRoomRepos, c, a.service.RoomRepos)
	call(mux, contract.MethodRepoCISet, c, a.service.RepoCISet)
	call(mux, contract.MethodSessionStart, c, a.service.SessionStart)
	call(mux, contract.MethodComposeList, c, a.service.ComposeList)
	call(mux, contract.MethodSessionMove, c, a.service.SessionMove)
	call(mux, contract.MethodSessionClose, c, a.service.SessionClose)
	call(mux, contract.MethodSessionSetProfile, c, a.service.SessionSetProfile)
	call(mux, contract.MethodSessionAccount, c, a.service.SessionAccount)
	call(mux, contract.MethodSessionResponsibilities, c, a.service.SessionResponsibilities)
	call(mux, contract.MethodSessionSetAccount, c, a.service.SessionSetAccount)
	call(mux, contract.MethodSessionSetBranch, c, a.service.SessionSetBranch)
	call(mux, contract.MethodSessionRestart, c, a.service.SessionRestart)
	call(mux, contract.MethodSessionRefresh, c, a.service.SessionRefresh)
	call(mux, contract.MethodSessionSearch, c, a.service.SessionSearch)
	call(mux, contract.MethodPostSearch, c, a.service.PostSearch)
	call(mux, contract.MethodSearchPosts, c, a.service.SearchPosts)
	call(mux, contract.MethodSearchSessions, c, a.service.SearchSessions)
	call(mux, contract.MethodSearchRooms, c, a.service.SearchRooms)
	call(mux, contract.MethodHistoryRead, c, a.service.HistoryRead)
	call(mux, contract.MethodTranscriptDigest, c, a.service.TranscriptDigest)
	call(mux, contract.MethodPrList, c, a.service.PrList)
	call(mux, contract.MethodPrStatus, c, a.service.PrStatus)
	call(mux, contract.MethodPrTrack, c, a.service.PrTrack)
	call(mux, contract.MethodPrUntrack, c, a.service.PrUntrack)
	call(mux, contract.MethodIssueList, c, a.service.IssueList)
	call(mux, contract.MethodIssueStatus, c, a.service.IssueStatus)
	call(mux, contract.MethodIssueTrack, c, a.service.IssueTrack)
	call(mux, contract.MethodIssueUntrack, c, a.service.IssueUntrack)
	call(mux, contract.MethodPost, c, a.service.Post)
	call(mux, contract.MethodEventsList, c, a.service.EventsList)
	call(mux, contract.MethodShellOutput, c, a.service.ShellOutput)
	call(mux, contract.MethodBoxStats, c, a.service.BoxStats)
	call(mux, contract.MethodBoxStatsReport, c, a.service.BoxStatsReport)
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
	call(mux, contract.MethodComputerStart, c, func(ctx context.Context, p contract.Principal, req contract.ComputerStartRequest) (contract.ComputerStartResponse, *contract.Error) {
		if req.Session == "" {
			return contract.ComputerStartResponse{}, invalid()
		}
		return a.service.ComputerStart(ctx, p, req)
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
	mux.HandleFunc("GET /v1/computers/stream", func(w http.ResponseWriter, r *http.Request) {
		c := requestCaller(r.Context(), c)
		if !owner(c) || !hasScope(c, contract.ScopeComputers) {
			writeError(w, forbidden())
			return
		}
		session := contract.SessionID(r.URL.Query().Get("session"))
		if session == "" {
			writeError(w, invalid())
			return
		}
		argv, release, err := a.service.ComputerStream(r.Context(), session)
		if err != nil {
			writeError(w, err)
			return
		}
		defer release()
		computerStream(w, r, argv)
	})
	call(mux, contract.MethodBoxHello, c, a.service.BoxHello)
	call(mux, contract.MethodBoxCompacted, c, a.service.BoxCompacted)
	call(mux, contract.MethodTurnOutput, c, a.service.TurnOutput)
	call(mux, contract.MethodTurnActivity, c, a.service.TurnActivity)
	call(mux, contract.MethodDeliveryDone, c, a.service.DeliveryDone)
	call(mux, contract.MethodDiffSaved, c, a.service.DiffSaved)
	call(mux, contract.MethodTranscriptAppend, c, a.service.TranscriptAppend)
	call(mux, contract.MethodTranscriptFetch, c, a.service.TranscriptFetch)
	call(mux, contract.MethodTranscriptRead, c, a.service.TranscriptRead)
	call(mux, contract.MethodSessionWork, c, a.service.SessionWork)
	call(mux, contract.MethodShellJobEnded, c, a.service.ShellJobEnded)
	call(mux, contract.MethodShellJobRunning, c, a.service.ShellJobRunning)
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
		ch, err := a.service.BoxChannel(r.Context(), c.Principal, r.URL.Query().Get("binary"))
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
	if box(c) {
		mux.HandleFunc("/github/", func(w http.ResponseWriter, r *http.Request) { a.githubTools(c, w, r) })
	}
	// Never on the web listener, which Connect carries off the host.
	if owner(c) && !c.Web && a.localModel != nil {
		mux.Handle("/model/", a.localModel)
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

// pinRoom holds a session to its own room: an omitted room becomes the caller's, and any other is refused.
func pinRoom(room *contract.RoomID, c Caller) *contract.Error {
	if *room == "" {
		*room = c.Room
	}
	if *room != c.Room {
		return contract.ForeignRoom(*room, c.Room)
	}
	return nil
}

// roomOf finds the room a request acts in, or nil. A refresh, a track and a subscription scoped to a session or the
// machine name their target another way, so an omitted room stays omitted: filling it would turn an empty refresh
// into one of the whole room, and the engine refuses a track or a machine subscription that names a room. With no room,
// a session subscription's session names are looked up in the caller's own room.
func roomOf(req any) *contract.RoomID {
	switch r := req.(type) {
	case *contract.SessionRefreshRequest, *contract.PrTrackRequest, *contract.IssueTrackRequest:
		return nil
	case *contract.SubscriptionAddRequest:
		if r.Scope != contract.ScopeRoom {
			return nil
		}
	}
	field := reflect.ValueOf(req).Elem().FieldByName("Room")
	if !field.IsValid() {
		return nil
	}
	room, _ := field.Addr().Interface().(*contract.RoomID)
	return room
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
		if err := dec.Decode(&req); err != nil {
			writeError(w, badBody(err))
			return
		}
		if req == nil {
			writeError(w, invalid())
			return
		}
		var extra json.RawMessage
		if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
			writeError(w, invalid())
			return
		}
		if room := roomOf(req); room != nil && box(c) {
			if m == contract.MethodPost && *room != "" {
				// Post alone authorizes its narrow cross-room exception in the service and store.
			} else if problem := pinRoom(room, c); problem != nil {
				writeError(w, problem)
				return
			}
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

// badBody refuses a request body. The decode error, if any, names an unknown field the way the dyna tool does, so a
// client sees which field to drop; any other decode error stays a bare refusal.
func badBody(decode error) *contract.Error {
	if field, unknown := strings.CutPrefix(decode.Error(), "json: unknown field "); unknown {
		return &contract.Error{Code: contract.ErrInvalid, Message: "unknown field " + field}
	}
	return invalid()
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
	case contract.ErrInvalid, contract.ErrCrossRoomAttachment, contract.ErrCommandTooLong:
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
