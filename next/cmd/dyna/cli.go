package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/dustin/go-humanize"
	"github.com/resident-labs/dyna/box/post"
	"github.com/resident-labs/dyna/box/visual"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/contract/tools"
	"github.com/spf13/cobra"
)

type client interface {
	ContextSnapshot(context.Context, contract.ContextSnapshotRequest) (contract.ContextSnapshotResponse, error)
	OpWithdraw(context.Context, contract.OpWithdrawRequest) (contract.OpWithdrawResponse, error)
	SessionMove(context.Context, contract.SessionMoveRequest) (contract.SessionMoveResponse, error)
	SessionList(context.Context, contract.SessionListRequest) (contract.SessionListResponse, error)
	HostexecRun(context.Context, contract.HostexecRunRequest) (contract.HostexecRunResponse, error)
	HostexecRevoke(context.Context, contract.HostexecRevokeRequest) (contract.HostexecRevokeResponse, error)
	File(context.Context, string, io.Writer) error
	Upload(context.Context, string) (contract.Attachment, error)
	HostAdd(context.Context, contract.HostAddRequest) (contract.HostAddResponse, error)
	RoomSetLimits(context.Context, contract.RoomSetLimitsRequest) (contract.RoomSetLimitsResponse, error)
	RoomPause(context.Context, contract.RoomPauseRequest) (contract.RoomPauseResponse, error)
	SessionStop(context.Context, contract.SessionStopRequest) (contract.SessionStopResponse, error)
	RoomCreate(context.Context, contract.RoomCreateRequest) (contract.RoomCreateResponse, error)
	RoomRepoAdd(context.Context, contract.RoomRepoAddRequest) (contract.RoomRepoAddResponse, error)
	RepoCISet(context.Context, contract.RepoCISetRequest) (contract.RepoCISetResponse, error)
	RoomRepos(context.Context, contract.RoomReposRequest) (contract.RoomReposResponse, error)
	SessionStart(context.Context, contract.SessionStartRequest) (contract.SessionStartResponse, error)
	SessionClose(context.Context, contract.SessionCloseRequest) (contract.SessionCloseResponse, error)
	SessionSetProfile(context.Context, contract.SessionSetProfileRequest) (contract.SessionSetProfileResponse, error)
	SessionRestart(context.Context, contract.SessionRestartRequest) (contract.SessionRestartResponse, error)
	Post(context.Context, contract.PostRequest) (contract.PostResponse, error)
	EventsList(context.Context, contract.EventsListRequest) (contract.EventsListResponse, error)
	Events(context.Context, contract.RoomID, int64, func(contract.Event) error) error
	OpList(context.Context, contract.OpListRequest) (contract.OpListResponse, error)
	OpDecide(context.Context, contract.OpDecideRequest) (contract.OpDecideResponse, error)
	OpSubmit(context.Context, contract.OpSubmitRequest) (contract.OpSubmitResponse, error)
	DevicePair(context.Context, contract.DevicePairRequest) (contract.DevicePairResponse, error)
	DeviceList(context.Context, contract.DeviceListRequest) (contract.DeviceListResponse, error)
	DeviceRevoke(context.Context, contract.DeviceRevokeRequest) (contract.DeviceRevokeResponse, error)
	DeviceSetScopes(context.Context, contract.DeviceSetScopesRequest) (contract.DeviceSetScopesResponse, error)
}

type socketClient struct{ http *http.Client }

func (c *socketClient) ContextSnapshot(ctx context.Context, req contract.ContextSnapshotRequest) (contract.ContextSnapshotResponse, error) {
	return call[contract.ContextSnapshotRequest, contract.ContextSnapshotResponse](ctx, c, contract.MethodContextSnapshot, req)
}

func newClient(socket string) *socketClient {
	return &socketClient{http: &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}}}
}

type requestType interface {
	contract.ContextSnapshotRequest |
		contract.OpWithdrawRequest |
		contract.SessionMoveRequest |
		contract.SessionListRequest |
		contract.RoomSetLimitsRequest | contract.RoomPauseRequest | contract.SessionStopRequest | contract.HostexecRunRequest | contract.HostexecRevokeRequest | contract.WebLoginRequest | contract.HostAddRequest | contract.RoomCreateRequest | contract.RoomRepoAddRequest | contract.RepoCISetRequest | contract.RoomReposRequest | contract.SessionStartRequest | contract.SessionCloseRequest | contract.SessionSetProfileRequest | contract.SessionRestartRequest | contract.PostRequest | contract.EventsListRequest | contract.OpListRequest | contract.OpDecideRequest | contract.OpSubmitRequest | contract.DevicePairRequest | contract.DeviceListRequest | contract.DeviceRevokeRequest | contract.DeviceSetScopesRequest | contract.ConnectStartRequest | contract.ConnectStatusRequest | contract.ConnectCancelRequest | contract.ConnectUnlinkRequest
}
type responseType interface {
	contract.ContextSnapshotResponse |
		contract.OpWithdrawResponse |
		contract.SessionMoveResponse |
		contract.SessionListResponse |
		contract.RoomSetLimitsResponse | contract.RoomPauseResponse | contract.SessionStopResponse | contract.HostexecRunResponse | contract.HostexecRevokeResponse | contract.WebLoginResponse | contract.HostAddResponse | contract.RoomCreateResponse | contract.RoomRepoAddResponse | contract.RepoCISetResponse | contract.RoomReposResponse | contract.SessionStartResponse | contract.SessionCloseResponse | contract.SessionSetProfileResponse | contract.SessionRestartResponse | contract.PostResponse | contract.EventsListResponse | contract.OpListResponse | contract.OpDecideResponse | contract.OpSubmitResponse | contract.DevicePairResponse | contract.DeviceListResponse | contract.DeviceRevokeResponse | contract.DeviceSetScopesResponse | contract.ConnectStartResponse | contract.ConnectStatusResponse | contract.ConnectCancelResponse | contract.ConnectUnlinkResponse
}

func call[Q requestType, R responseType](ctx context.Context, c *socketClient, method contract.Method, input Q) (R, error) {
	var result R
	body, err := json.Marshal(input)
	if err != nil {
		return result, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://dyna/v1/"+string(method), bytes.NewReader(body))
	if err != nil {
		return result, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return result, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var e contract.Error
		if err := json.NewDecoder(resp.Body).Decode(&e); err != nil {
			return result, err
		}
		return result, fmt.Errorf("%s: %s", e.Code, e.Message)
	}
	err = json.NewDecoder(resp.Body).Decode(&result)
	return result, err
}

func (c *socketClient) ConnectStart(ctx context.Context, q contract.ConnectStartRequest) (contract.ConnectStartResponse, error) {
	return call[contract.ConnectStartRequest, contract.ConnectStartResponse](ctx, c, contract.MethodConnectStart, q)
}
func (c *socketClient) ConnectStatus(ctx context.Context, q contract.ConnectStatusRequest) (contract.ConnectStatusResponse, error) {
	return call[contract.ConnectStatusRequest, contract.ConnectStatusResponse](ctx, c, contract.MethodConnectStatus, q)
}
func (c *socketClient) ConnectCancel(ctx context.Context, q contract.ConnectCancelRequest) (contract.ConnectCancelResponse, error) {
	return call[contract.ConnectCancelRequest, contract.ConnectCancelResponse](ctx, c, contract.MethodConnectCancel, q)
}
func (c *socketClient) ConnectUnlink(ctx context.Context, q contract.ConnectUnlinkRequest) (contract.ConnectUnlinkResponse, error) {
	return call[contract.ConnectUnlinkRequest, contract.ConnectUnlinkResponse](ctx, c, contract.MethodConnectUnlink, q)
}

func (c *socketClient) SessionList(ctx context.Context, q contract.SessionListRequest) (contract.SessionListResponse, error) {
	return call[contract.SessionListRequest, contract.SessionListResponse](ctx, c, contract.MethodSessionList, q)
}
func (c *socketClient) SessionMove(ctx context.Context, q contract.SessionMoveRequest) (contract.SessionMoveResponse, error) {
	return call[contract.SessionMoveRequest, contract.SessionMoveResponse](ctx, c, contract.MethodSessionMove, q)
}
func (c *socketClient) OpWithdraw(ctx context.Context, q contract.OpWithdrawRequest) (contract.OpWithdrawResponse, error) {
	return call[contract.OpWithdrawRequest, contract.OpWithdrawResponse](ctx, c, contract.MethodOpWithdraw, q)
}
func (c *socketClient) HostexecRun(ctx context.Context, q contract.HostexecRunRequest) (contract.HostexecRunResponse, error) {
	return call[contract.HostexecRunRequest, contract.HostexecRunResponse](ctx, c, contract.MethodHostexecRun, q)
}
func (c *socketClient) HostexecRevoke(ctx context.Context, q contract.HostexecRevokeRequest) (contract.HostexecRevokeResponse, error) {
	return call[contract.HostexecRevokeRequest, contract.HostexecRevokeResponse](ctx, c, contract.MethodHostexecRevoke, q)
}
func (c *socketClient) File(ctx context.Context, hash string, out io.Writer) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://dyna/v1/files/"+hash, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("output file: %s", resp.Status)
	}
	_, err = io.Copy(out, resp.Body)
	return err
}

func (c *socketClient) Upload(ctx context.Context, path string) (contract.Attachment, error) {
	return post.UploadHTTP(ctx, c.http, path)
}

type commandExit int

func (e commandExit) Error() string { return fmt.Sprintf("command exited with status %d", e) }
func (c *socketClient) HostAdd(ctx context.Context, q contract.HostAddRequest) (contract.HostAddResponse, error) {
	return call[contract.HostAddRequest, contract.HostAddResponse](ctx, c, contract.MethodHostAdd, q)
}
func (c *socketClient) RoomSetLimits(ctx context.Context, q contract.RoomSetLimitsRequest) (contract.RoomSetLimitsResponse, error) {
	return call[contract.RoomSetLimitsRequest, contract.RoomSetLimitsResponse](ctx, c, contract.MethodRoomSetLimits, q)
}
func (c *socketClient) RoomPause(ctx context.Context, q contract.RoomPauseRequest) (contract.RoomPauseResponse, error) {
	return call[contract.RoomPauseRequest, contract.RoomPauseResponse](ctx, c, contract.MethodRoomPause, q)
}
func (c *socketClient) SessionStop(ctx context.Context, q contract.SessionStopRequest) (contract.SessionStopResponse, error) {
	return call[contract.SessionStopRequest, contract.SessionStopResponse](ctx, c, contract.MethodSessionStop, q)
}

func (c *socketClient) RoomCreate(ctx context.Context, q contract.RoomCreateRequest) (contract.RoomCreateResponse, error) {
	return call[contract.RoomCreateRequest, contract.RoomCreateResponse](ctx, c, contract.MethodRoomCreate, q)
}
func (c *socketClient) RepoCISet(ctx context.Context, q contract.RepoCISetRequest) (contract.RepoCISetResponse, error) {
	return call[contract.RepoCISetRequest, contract.RepoCISetResponse](ctx, c, contract.MethodRepoCISet, q)
}
func (c *socketClient) RoomRepoAdd(ctx context.Context, q contract.RoomRepoAddRequest) (contract.RoomRepoAddResponse, error) {
	return call[contract.RoomRepoAddRequest, contract.RoomRepoAddResponse](ctx, c, contract.MethodRoomRepoAdd, q)
}
func (c *socketClient) RoomRepos(ctx context.Context, q contract.RoomReposRequest) (contract.RoomReposResponse, error) {
	return call[contract.RoomReposRequest, contract.RoomReposResponse](ctx, c, contract.MethodRoomRepos, q)
}
func (c *socketClient) SessionStart(ctx context.Context, q contract.SessionStartRequest) (contract.SessionStartResponse, error) {
	return call[contract.SessionStartRequest, contract.SessionStartResponse](ctx, c, contract.MethodSessionStart, q)
}
func (c *socketClient) SessionSetProfile(ctx context.Context, q contract.SessionSetProfileRequest) (contract.SessionSetProfileResponse, error) {
	return call[contract.SessionSetProfileRequest, contract.SessionSetProfileResponse](ctx, c, contract.MethodSessionSetProfile, q)
}
func (c *socketClient) SessionClose(ctx context.Context, q contract.SessionCloseRequest) (contract.SessionCloseResponse, error) {
	return call[contract.SessionCloseRequest, contract.SessionCloseResponse](ctx, c, contract.MethodSessionClose, q)
}
func (c *socketClient) SessionRestart(ctx context.Context, q contract.SessionRestartRequest) (contract.SessionRestartResponse, error) {
	return call[contract.SessionRestartRequest, contract.SessionRestartResponse](ctx, c, contract.MethodSessionRestart, q)
}
func (c *socketClient) Post(ctx context.Context, q contract.PostRequest) (contract.PostResponse, error) {
	return call[contract.PostRequest, contract.PostResponse](ctx, c, contract.MethodPost, q)
}
func (c *socketClient) EventsList(ctx context.Context, q contract.EventsListRequest) (contract.EventsListResponse, error) {
	return call[contract.EventsListRequest, contract.EventsListResponse](ctx, c, contract.MethodEventsList, q)
}
func (c *socketClient) OpList(ctx context.Context, q contract.OpListRequest) (contract.OpListResponse, error) {
	return call[contract.OpListRequest, contract.OpListResponse](ctx, c, contract.MethodOpList, q)
}
func (c *socketClient) OpDecide(ctx context.Context, q contract.OpDecideRequest) (contract.OpDecideResponse, error) {
	return call[contract.OpDecideRequest, contract.OpDecideResponse](ctx, c, contract.MethodOpDecide, q)
}
func (c *socketClient) OpSubmit(ctx context.Context, q contract.OpSubmitRequest) (contract.OpSubmitResponse, error) {
	return call[contract.OpSubmitRequest, contract.OpSubmitResponse](ctx, c, contract.MethodOpSubmit, q)
}
func (c *socketClient) DevicePair(ctx context.Context, q contract.DevicePairRequest) (contract.DevicePairResponse, error) {
	return call[contract.DevicePairRequest, contract.DevicePairResponse](ctx, c, contract.MethodDevicePair, q)
}
func (c *socketClient) DeviceList(ctx context.Context, q contract.DeviceListRequest) (contract.DeviceListResponse, error) {
	return call[contract.DeviceListRequest, contract.DeviceListResponse](ctx, c, contract.MethodDeviceList, q)
}
func (c *socketClient) DeviceRevoke(ctx context.Context, q contract.DeviceRevokeRequest) (contract.DeviceRevokeResponse, error) {
	return call[contract.DeviceRevokeRequest, contract.DeviceRevokeResponse](ctx, c, contract.MethodDeviceRevoke, q)
}
func (c *socketClient) DeviceSetScopes(ctx context.Context, q contract.DeviceSetScopesRequest) (contract.DeviceSetScopesResponse, error) {
	return call[contract.DeviceSetScopesRequest, contract.DeviceSetScopesResponse](ctx, c, contract.MethodDeviceSetScopes, q)
}

// scopeList turns repeated --scope flags into typed scopes. The engine checks the values.
func scopeList(values []string) []contract.Scope {
	scopes := make([]contract.Scope, 0, len(values))
	for _, value := range values {
		scopes = append(scopes, contract.Scope(value))
	}
	return scopes
}
func (c *socketClient) Events(ctx context.Context, room contract.RoomID, after int64, emit func(contract.Event) error) error {
	u := "http://dyna/v1/events?room=" + url.QueryEscape(string(room)) + "&after=" + strconv.FormatInt(after, 10)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("event stream: %s", resp.Status)
	}
	scan := bufio.NewScanner(resp.Body)
	scan.Buffer(make([]byte, 4096), 4<<20)
	var name string
	for scan.Scan() {
		line := scan.Text()
		if line == "" {
			name = ""
			continue
		}
		if after, ok := strings.CutPrefix(line, "event: "); ok {
			name = after
			continue
		}
		if !strings.HasPrefix(line, "data: ") || (name != "" && name != "message") {
			continue
		}
		var event contract.Event
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
			return err
		}
		if err := emit(event); err != nil {
			return err
		}
	}
	return scan.Err()
}

func defaultSocket() string {
	const boxSocket = "/run/dyna/engine/engine.sock"
	if _, err := os.Stat(boxSocket); err == nil {
		return boxSocket
	}
	return filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), "dyna", "owner.sock")
}

func addCLI(root *cobra.Command, getClient func() client) {
	hostexec := &cobra.Command{Use: "hostexec"}
	var runHost, cwd string
	var timeout time.Duration
	run := &cobra.Command{Use: "run --host HOST -- COMMAND [ARG...]", Args: cobra.MinimumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		c := getClient()
		res, err := c.HostexecRun(cmd.Context(), contract.HostexecRunRequest{Host: contract.HostName(runHost), Argv: args, Cwd: cwd, Timeout: contract.DurationMS(timeout.Milliseconds())})
		if err != nil {
			return err
		}
		op := res.Operation
		if op.Result == nil {
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Command %s waits for approval; op.ran wakes the session with the result.\n", op.ID)
			return err
		}
		if _, err = io.WriteString(cmd.OutOrStdout(), op.Result.Output); err != nil {
			return err
		}
		if op.Result.OutputFile != "" {
			if err = c.File(cmd.Context(), op.Result.OutputFile, cmd.OutOrStdout()); err != nil {
				return err
			}
		}
		if op.Result.Truncated {
			fmt.Fprintln(cmd.ErrOrStderr(), "Output truncated at 8 MiB.")
		}
		if op.Result.Error != "" {
			return fmt.Errorf("%s", op.Result.Error)
		}
		if op.Result.ExitCode != 0 {
			cmd.SilenceErrors = true
			return commandExit(op.Result.ExitCode)
		}
		return nil
	}}
	var grantHost, grantRoom, grantTo, grantReason string
	var grantHours int
	grant := &cobra.Command{Use: "grant --host HOST --hours N --reason WHY", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		args := contract.HostexecGrantArgs{Room: contract.RoomID(grantRoom), Hosts: []contract.HostName{contract.HostName(grantHost)}, Reason: grantReason, Expires: time.Now().Add(time.Duration(grantHours) * time.Hour)}
		if grantTo != "" {
			args.To = contract.Principal{Kind: contract.PrincipalSession, Session: contract.SessionID(grantTo)}
		}
		r, err := getClient().OpSubmit(cmd.Context(), contract.OpSubmitRequest{Operation: contract.Operation{Kind: args.OpKind(), Room: args.Room, Args: args}})
		if err == nil {
			_, err = fmt.Fprintln(cmd.OutOrStdout(), r.Operation.ID)
		}
		return err
	}}
	grant.Flags().StringVar(&grantHost, "host", "", "Host name")
	grant.Flags().IntVar(&grantHours, "hours", 24, "Hours the grant lasts")
	grant.Flags().StringVar(&grantReason, "reason", "", "Why the session needs access")
	grant.Flags().StringVar(&grantTo, "to", "", "Session that gets the grant (owner only)")
	grant.Flags().StringVar(&grantRoom, "room", "", "Room ID (owner only)")
	_ = grant.MarkFlagRequired("host")   // fails only for an undefined flag
	_ = grant.MarkFlagRequired("reason") // fails only for an undefined flag
	run.Flags().StringVar(&runHost, "host", "", "Host name")
	run.Flags().StringVar(&cwd, "cwd", "", "Working directory")
	run.Flags().DurationVar(&timeout, "timeout", 10*time.Minute, "Run timeout")
	_ = run.MarkFlagRequired("host")
	revoke := &cobra.Command{Use: "revoke GRANT", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		_, err := getClient().HostexecRevoke(cmd.Context(), contract.HostexecRevokeRequest{Grant: contract.GrantID(args[0])})
		return err
	}}
	hostexec.AddCommand(run, revoke, grant)
	root.AddCommand(hostexec)

	host := &cobra.Command{Use: "host"}
	var ssh string
	var hostRooms []string
	var admin bool
	add := &cobra.Command{Use: "add NAME", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		rooms := make([]contract.RoomID, len(hostRooms))
		for i, room := range hostRooms {
			rooms[i] = contract.RoomID(room)
		}
		result, err := getClient().HostAdd(cmd.Context(), contract.HostAddRequest{Name: contract.HostName(args[0]), SSH: ssh, OwnerOnlyAdmin: admin, Rooms: rooms})
		if err == nil && result.NoSwap {
			fmt.Fprintln(cmd.ErrOrStderr(), "Warning: host has no swap; memory pressure can kill box processes.")
		}
		return err
	}}
	add.Flags().StringSliceVar(&hostRooms, "room", nil, "Room ID allowed on this host (repeatable; default: all rooms)")
	add.Flags().StringVar(&ssh, "ssh", "", "SSH destination")
	add.Flags().BoolVar(&admin, "owner-only-admin", false, "Only the owner has host admin rights")
	host.AddCommand(add)
	root.AddCommand(host)

	room := &cobra.Command{Use: "room"}
	for _, paused := range []bool{true, false} {
		name := "resume"
		if paused {
			name = "pause"
		}
		room.AddCommand(&cobra.Command{Use: name + " ROOM", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
			_, err := getClient().RoomPause(cmd.Context(), contract.RoomPauseRequest{Room: contract.RoomID(args[0]), Paused: paused})
			return err
		}})
	}
	var limitCPUs int
	var limitMem string
	limit := &cobra.Command{Use: "limit ROOM --cpus N --mem SIZE", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		mem, err := humanize.ParseBytes(limitMem)
		if err != nil || mem > math.MaxInt64 {
			return fmt.Errorf("invalid memory size %q", limitMem)
		}
		_, err = getClient().RoomSetLimits(cmd.Context(), contract.RoomSetLimitsRequest{Room: contract.RoomID(args[0]), CPUs: limitCPUs, MemBytes: int64(mem)})
		return err
	}}
	limit.Flags().IntVar(&limitCPUs, "cpus", 0, "CPU limit; 0 means unlimited")
	limit.Flags().StringVar(&limitMem, "mem", "", "Memory limit; 0 means unlimited")
	_ = limit.MarkFlagRequired("cpus")
	_ = limit.MarkFlagRequired("mem")
	room.AddCommand(limit)
	var rootProfile string
	create := &cobra.Command{Use: "create NAME", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		r, err := getClient().RoomCreate(cmd.Context(), contract.RoomCreateRequest{Name: args[0], RootProfile: contract.ProfileName(rootProfile)})
		if err == nil {
			_, err = fmt.Fprintln(cmd.OutOrStdout(), r.Room)
		}
		return err
	}}
	create.Flags().StringVar(&rootProfile, "root-profile", "", "Root session profile")
	var follow bool
	log := &cobra.Command{Use: "log ROOM", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		c := getClient()
		room := contract.RoomID(args[0])
		var after int64
		printEvent := func(e contract.Event) error {
			if e.Seq == 0 {
				return nil
			}
			data, err := json.Marshal(e)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), string(data))
			return err
		}
		for {
			r, err := c.EventsList(cmd.Context(), contract.EventsListRequest{Room: room, AfterSeq: after, Limit: 256})
			if err != nil {
				return err
			}
			for _, e := range r.Events {
				if err := printEvent(e); err != nil {
					return err
				}
				after = e.Seq
			}
			if len(r.Events) < 256 {
				break
			}
		}
		if follow {
			return c.Events(cmd.Context(), room, after, printEvent)
		}
		return nil
	}}
	log.Flags().BoolVar(&follow, "follow", false, "Follow new events")
	repo := &cobra.Command{Use: "repo"}
	var makeDefault bool
	addRepo := &cobra.Command{Use: "add ROOM REPO", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		_, err := getClient().RoomRepoAdd(cmd.Context(), contract.RoomRepoAddRequest{Room: contract.RoomID(args[0]), Repo: contract.RepoName(args[1]), Default: makeDefault})
		return err
	}}
	addRepo.Flags().BoolVar(&makeDefault, "default", false, "Make this the room default")
	listRepos := &cobra.Command{Use: "list ROOM", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		r, err := getClient().RoomRepos(cmd.Context(), contract.RoomReposRequest{Room: contract.RoomID(args[0])})
		if err != nil {
			return err
		}
		for _, repo := range r.Repos {
			if repo.Default {
				if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s (default)\n", repo.Repo); err != nil {
					return err
				}
			} else if _, err := fmt.Fprintln(cmd.OutOrStdout(), repo.Repo); err != nil {
				return err
			}
		}
		return nil
	}}
	var ciMode, ciSize string
	var ciHosts []string
	repoCI := &cobra.Command{Use: "ci REPO", Short: "Set where CI runs for a repository", Long: "--mode box serves jobs from each --host NAME=COUNT, at most COUNT runners at once. --mode github removes them at once. See next/docs/ci-runners.md.", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		request := contract.RepoCISetRequest{Repo: contract.RepoName(args[0]), Mode: contract.CIMode(ciMode)}
		for _, host := range ciHosts {
			name, count, ok := strings.Cut(host, "=")
			runners, err := strconv.Atoi(count)
			if !ok || err != nil {
				return fmt.Errorf("--host needs NAME=COUNT, got %q", host)
			}
			request.Hosts = append(request.Hosts, contract.RepoCIHost{Repo: request.Repo, Host: contract.HostName(name), Runners: runners, Size: contract.BoxSizeName(ciSize)})
		}
		_, err := getClient().RepoCISet(cmd.Context(), request)
		return err
	}}
	repoCI.Flags().StringVar(&ciMode, "mode", "", "github or box")
	repoCI.Flags().StringSliceVar(&ciHosts, "host", nil, "Runner host as NAME=COUNT (repeatable)")
	repoCI.Flags().StringVar(&ciSize, "size", string(contract.BoxSizeLarge), "Runner size: its CPUs, memory and disk cap")
	_ = repoCI.MarkFlagRequired("mode") // fails only for an undefined flag
	repo.AddCommand(addRepo, listRepos, repoCI)
	room.AddCommand(create, log, repo)
	root.AddCommand(room)

	session := &cobra.Command{Use: "session"}
	session.AddCommand(&cobra.Command{Use: "stop SESSION", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		result, err := getClient().SessionStop(cmd.Context(), contract.SessionStopRequest{Session: contract.SessionID(args[0])})
		if err != nil {
			return err
		}
		if !result.Stopping {
			_, err = fmt.Fprintln(cmd.OutOrStdout(), "No turn is running.")
		}
		return err
	}})
	var roomID, name, profile, goal, brief, sessionRepo, parent, size string
	var sameHost bool
	start := &cobra.Command{Use: "start", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		r, err := getClient().SessionStart(cmd.Context(), contract.SessionStartRequest{Room: contract.RoomID(roomID), Name: name, Parent: contract.SessionID(parent), Repo: contract.RepoName(sessionRepo), Profile: contract.ProfileName(profile), Goal: goal, Brief: brief, Size: contract.BoxSizeName(size), SameHost: sameHost})
		if err == nil {
			_, err = fmt.Fprintln(cmd.OutOrStdout(), r.Session)
		}
		return err
	}}
	start.Flags().StringVar(&roomID, "room", "", "Room ID")
	start.Flags().StringVar(&name, "name", "", "Session name")
	start.Flags().StringVar(&profile, "profile", "", "Profile")
	start.Flags().StringVar(&sessionRepo, "repo", "", "Session repository")
	start.Flags().StringVar(&parent, "parent", "", "Parent session ID")
	start.Flags().StringVar(&goal, "goal", "", "Goal")
	start.Flags().StringVar(&brief, "brief", "", "Brief")
	start.Flags().StringVar(&size, "size", "", "Box size for a lead: agent, small, medium or large")
	start.Flags().BoolVar(&sameHost, "same-host", false, "Place the box on its parent’s host")
	var discard bool
	closeCmd := &cobra.Command{Use: "close SESSION", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		_, err := getClient().SessionClose(cmd.Context(), contract.SessionCloseRequest{Session: contract.SessionID(args[0]), Discard: discard})
		return err
	}}
	closeCmd.Flags().BoolVar(&discard, "discard", false, "Close without archiving")
	restart := &cobra.Command{Use: "restart SESSION", Short: "Replace a failed session box", Long: "Replace a failed session box. The old volume and unpushed work are lost. If the old host is unavailable, its resources may remain there.", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		_, err := getClient().SessionRestart(cmd.Context(), contract.SessionRestartRequest{Session: contract.SessionID(args[0])})
		return err
	}}
	setProfile := &cobra.Command{Use: "set-profile SESSION PROFILE", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		_, err := getClient().SessionSetProfile(cmd.Context(), contract.SessionSetProfileRequest{Session: contract.SessionID(args[0]), Profile: contract.ProfileName(args[1])})
		return err
	}}
	session.AddCommand(&cobra.Command{Use: "list ROOM", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		result, err := getClient().SessionList(cmd.Context(), contract.SessionListRequest{Room: contract.RoomID(args[0])})
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(result.Sessions)
	}}, &cobra.Command{Use: "move SESSION NEW_PARENT", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		_, err := getClient().SessionMove(cmd.Context(), contract.SessionMoveRequest{Session: contract.SessionID(args[0]), NewParent: contract.SessionID(args[1])})
		return err
	}})
	session.AddCommand(start, closeCmd, restart, setProfile)
	root.AddCommand(session)
	var postRoom, htmlPath, update string
	var allowNet bool
	var mentions, paths []string
	postCmd := &cobra.Command{Use: "post TEXT", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		c := getClient()
		request, checks, err := (post.Preparer{Upload: c.Upload, Render: visual.Preview}).Prepare(cmd.Context(), tools.PostInput{Room: contract.RoomID(postRoom), Text: args[0], Mentions: mentions, Paths: paths, HTML: htmlPath, AllowNet: allowNet, VisualUpdate: contract.EventID(update)})
		if err != nil {
			return err
		}
		printVisualChecks(cmd.ErrOrStderr(), checks)
		request.ToolCallID = os.Getenv(contract.ToolCallIDEnv)
		r, err := c.Post(cmd.Context(), request)
		if err == nil {
			_, err = fmt.Fprintln(cmd.OutOrStdout(), r.Event)
		}
		return err
	}}
	postCmd.Flags().StringVar(&htmlPath, "html", "", "Attach an HTML visual card")
	postCmd.Flags().StringVar(&update, "update", "", "Add a version to a visual post")
	postCmd.Flags().BoolVar(&allowNet, "allow-net", false, "Allow HTTPS resources in the visual")
	postCmd.Flags().StringArrayVar(&paths, "attach", nil, "Attach a file (repeatable)")
	postCmd.Flags().StringVar(&postRoom, "room", "", "Room ID")
	postCmd.Flags().StringSliceVar(&mentions, "mention", nil, "Session mentions")
	root.AddCommand(postCmd, visualCommand())
	contextCmd := &cobra.Command{Use: "context"}
	contextCmd.AddCommand(&cobra.Command{Use: "show SECTION", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		result, err := getClient().ContextSnapshot(cmd.Context(), contract.ContextSnapshotRequest{Section: contract.ContextSectionKey(args[0])})
		if err != nil {
			return err
		}
		for _, section := range result.Sections {
			if _, err = fmt.Fprintln(cmd.OutOrStdout(), section.Text); err != nil {
				return err
			}
		}
		return nil
	}})
	root.AddCommand(contextCmd)

	op := &cobra.Command{Use: "op"}
	opList := &cobra.Command{Use: "list ROOM", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		r, err := getClient().OpList(cmd.Context(), contract.OpListRequest{Room: contract.RoomID(args[0])})
		if err != nil {
			return err
		}
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(r.Operations)
	}}
	opApprove := &cobra.Command{Use: "approve ID", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		_, err := getClient().OpDecide(cmd.Context(), contract.OpDecideRequest{ID: contract.OpID(args[0]), Approve: true})
		return err
	}}
	var denyReason string
	opDeny := &cobra.Command{Use: "deny ID", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		_, err := getClient().OpDecide(cmd.Context(), contract.OpDecideRequest{ID: contract.OpID(args[0]), Approve: false, Reason: denyReason})
		return err
	}}
	opDeny.Flags().StringVar(&denyReason, "reason", "", "Why the operation is denied")
	_ = opDeny.MarkFlagRequired("reason") // fails only for an undefined flag, and "reason" is defined above
	op.AddCommand(&cobra.Command{Use: "withdraw ID", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		_, err := getClient().OpWithdraw(cmd.Context(), contract.OpWithdrawRequest{ID: contract.OpID(args[0])})
		return err
	}})
	op.AddCommand(opList, opApprove, opDeny)
	root.AddCommand(op)

	connect := &cobra.Command{Use: "connect"}
	type connectClient interface {
		ConnectStart(context.Context, contract.ConnectStartRequest) (contract.ConnectStartResponse, error)
		ConnectStatus(context.Context, contract.ConnectStatusRequest) (contract.ConnectStatusResponse, error)
		ConnectCancel(context.Context, contract.ConnectCancelRequest) (contract.ConnectCancelResponse, error)
		ConnectUnlink(context.Context, contract.ConnectUnlinkRequest) (contract.ConnectUnlinkResponse, error)
	}
	getConnect := func() (connectClient, error) {
		c, ok := getClient().(connectClient)
		if !ok {
			return nil, fmt.Errorf("Connect unavailable")
		}
		return c, nil
	}
	var connectRelay, connectName string
	connectStart := &cobra.Command{Use: "start", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		c, err := getConnect()
		if err != nil {
			return err
		}
		_, err = c.ConnectStart(cmd.Context(), contract.ConnectStartRequest{Relay: connectRelay, Name: connectName})
		return err
	}}
	connectStart.Flags().StringVar(&connectRelay, "relay", "", "Relay origin")
	connectStart.Flags().StringVar(&connectName, "name", "", "Machine name")
	connect.AddCommand(connectStart, &cobra.Command{Use: "status", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		c, err := getConnect()
		if err != nil {
			return err
		}
		status, err := c.ConnectStatus(cmd.Context(), contract.ConnectStatusRequest{})
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(status)
	}}, &cobra.Command{Use: "cancel", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		c, err := getConnect()
		if err != nil {
			return err
		}
		_, err = c.ConnectCancel(cmd.Context(), contract.ConnectCancelRequest{})
		return err
	}}, &cobra.Command{Use: "unlink", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		c, err := getConnect()
		if err != nil {
			return err
		}
		_, err = c.ConnectUnlink(cmd.Context(), contract.ConnectUnlinkRequest{})
		return err
	}})
	root.AddCommand(connect)

	device := &cobra.Command{Use: "device"}
	var pairScopes []string
	devicePair := &cobra.Command{Use: "pair", Args: cobra.NoArgs, Short: "Make a pairing code", Long: "Make a 10-character pairing code. It works once, lives 5 minutes, and dies after 5 wrong tries. A new code replaces the old one. Without --scope, the code grants every scope on a loopback route, and read and operate elsewhere.", RunE: func(cmd *cobra.Command, _ []string) error {
		r, err := getClient().DevicePair(cmd.Context(), contract.DevicePairRequest{Scopes: scopeList(pairScopes)})
		if err == nil {
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s expires %s\n", r.Code, r.ExpiresAt.Format(time.RFC3339))
		}
		return err
	}}
	devicePair.Flags().StringArrayVar(&pairScopes, "scope", nil, "Scope to grant (repeatable)")
	deviceList := &cobra.Command{Use: "list", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		r, err := getClient().DeviceList(cmd.Context(), contract.DeviceListRequest{})
		if err != nil {
			return err
		}
		for _, d := range r.Devices {
			scopes := make([]string, 0, len(d.Scopes))
			for _, scope := range d.Scopes {
				scopes = append(scopes, string(scope))
			}
			if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%s\t%s\n", d.ID, d.Name, strings.Join(scopes, ","), d.LastSeenAt.Format(time.RFC3339), d.ExpiresAt.Format(time.RFC3339)); err != nil {
				return err
			}
		}
		return nil
	}}
	deviceRevoke := &cobra.Command{Use: "revoke ID", Args: cobra.ExactArgs(1), Short: "Revoke a paired device", Long: "Revoke a paired device. It fails on its next request, and an open stream or terminal ends within 15 seconds.", RunE: func(cmd *cobra.Command, args []string) error {
		_, err := getClient().DeviceRevoke(cmd.Context(), contract.DeviceRevokeRequest{ID: args[0]})
		return err
	}}
	var setScopes []string
	deviceSetScopes := &cobra.Command{Use: "set-scopes ID", Args: cobra.ExactArgs(1), Short: "Replace the scopes of a paired device", RunE: func(cmd *cobra.Command, args []string) error {
		_, err := getClient().DeviceSetScopes(cmd.Context(), contract.DeviceSetScopesRequest{ID: args[0], Scopes: scopeList(setScopes)})
		return err
	}}
	deviceSetScopes.Flags().StringArrayVar(&setScopes, "scope", nil, "Scope to grant (repeatable)")
	_ = deviceSetScopes.MarkFlagRequired("scope") // fails only for an undefined flag, and "scope" is defined above
	device.AddCommand(devicePair, deviceList, deviceRevoke, deviceSetScopes)
	root.AddCommand(device)

	pr := &cobra.Command{Use: "pr"}
	var prTitle, prBodyFile, prBase, prHead string
	var prDraft bool
	prOpen := &cobra.Command{Use: "open", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		var body string
		if prBodyFile != "" {
			data, err := os.ReadFile(prBodyFile)
			if err != nil {
				return err
			}
			body = string(data)
		}
		r, err := getClient().OpSubmit(cmd.Context(), contract.OpSubmitRequest{Operation: contract.Operation{
			Kind: "pr.open",
			Args: contract.PrOpenArgs{Title: prTitle, Body: body, Base: prBase, Head: prHead, Draft: prDraft},
		}})
		if err != nil {
			return err
		}
		res := r.Operation.Result
		if res == nil {
			return fmt.Errorf("pr open %s has no result yet (state %s)", r.Operation.ID, r.Operation.State)
		}
		if res.Error != "" || res.ExitCode != 0 {
			return fmt.Errorf("pr open %s failed (exit code %d): %s", r.Operation.ID, res.ExitCode, res.Error)
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), res.Output)
		return err
	}}
	prOpen.Flags().StringVar(&prTitle, "title", "", "Pull request title")
	prOpen.Flags().StringVar(&prBodyFile, "body-file", "", "File with the pull request body")
	prOpen.Flags().StringVar(&prHead, "head", "", "Head branch (default: session branch)")
	prOpen.Flags().StringVar(&prBase, "base", "main", "Base branch")
	prOpen.Flags().BoolVar(&prDraft, "draft", false, "Open as a draft")
	_ = prOpen.MarkFlagRequired("title") // fails only for an undefined flag, and "title" is defined above
	pr.AddCommand(prOpen)
	root.AddCommand(pr)
}
