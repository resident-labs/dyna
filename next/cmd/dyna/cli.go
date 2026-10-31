package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/spf13/cobra"
)

type client interface {
	RuleAdd(context.Context, contract.RuleAddRequest) (contract.RuleAddResponse, error)
	RuleRemove(context.Context, contract.RuleRemoveRequest) (contract.RuleRemoveResponse, error)
	RuleList(context.Context, contract.RuleListRequest) (contract.RuleListResponse, error)
	HostexecRun(context.Context, contract.HostexecRunRequest) (contract.HostexecRunResponse, error)
	HostexecRevoke(context.Context, contract.HostexecRevokeRequest) (contract.HostexecRevokeResponse, error)
	File(context.Context, string, io.Writer) error
	Upload(context.Context, string) (contract.Attachment, error)
	HostAdd(context.Context, contract.HostAddRequest) (contract.HostAddResponse, error)
	RoomCreate(context.Context, contract.RoomCreateRequest) (contract.RoomCreateResponse, error)
	RoomRepoAdd(context.Context, contract.RoomRepoAddRequest) (contract.RoomRepoAddResponse, error)
	RoomRepos(context.Context, contract.RoomReposRequest) (contract.RoomReposResponse, error)
	SessionStart(context.Context, contract.SessionStartRequest) (contract.SessionStartResponse, error)
	SessionClose(context.Context, contract.SessionCloseRequest) (contract.SessionCloseResponse, error)
	SessionRestart(context.Context, contract.SessionRestartRequest) (contract.SessionRestartResponse, error)
	Post(context.Context, contract.PostRequest) (contract.PostResponse, error)
	EventsList(context.Context, contract.EventsListRequest) (contract.EventsListResponse, error)
	Events(context.Context, contract.RoomID, int64, func(contract.Event) error) error
	OpList(context.Context, contract.OpListRequest) (contract.OpListResponse, error)
	OpDecide(context.Context, contract.OpDecideRequest) (contract.OpDecideResponse, error)
	OpSubmit(context.Context, contract.OpSubmitRequest) (contract.OpSubmitResponse, error)
}

type socketClient struct{ http *http.Client }

func newClient(socket string) *socketClient {
	return &socketClient{http: &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}}}
}

type requestType interface {
	contract.RuleAddRequest | contract.RuleRemoveRequest | contract.RuleListRequest | contract.HostexecRunRequest | contract.HostexecRevokeRequest | contract.WebLoginRequest | contract.HostAddRequest | contract.RoomCreateRequest | contract.RoomRepoAddRequest | contract.RoomReposRequest | contract.SessionStartRequest | contract.SessionCloseRequest | contract.SessionRestartRequest | contract.PostRequest | contract.EventsListRequest | contract.OpListRequest | contract.OpDecideRequest | contract.OpSubmitRequest
}
type responseType interface {
	contract.RuleAddResponse | contract.RuleRemoveResponse | contract.RuleListResponse | contract.HostexecRunResponse | contract.HostexecRevokeResponse | contract.WebLoginResponse | contract.HostAddResponse | contract.RoomCreateResponse | contract.RoomRepoAddResponse | contract.RoomReposResponse | contract.SessionStartResponse | contract.SessionCloseResponse | contract.SessionRestartResponse | contract.PostResponse | contract.EventsListResponse | contract.OpListResponse | contract.OpDecideResponse | contract.OpSubmitResponse
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

func (c *socketClient) RuleAdd(ctx context.Context, q contract.RuleAddRequest) (contract.RuleAddResponse, error) {
	return call[contract.RuleAddRequest, contract.RuleAddResponse](ctx, c, contract.MethodRuleAdd, q)
}
func (c *socketClient) RuleRemove(ctx context.Context, q contract.RuleRemoveRequest) (contract.RuleRemoveResponse, error) {
	return call[contract.RuleRemoveRequest, contract.RuleRemoveResponse](ctx, c, contract.MethodRuleRemove, q)
}
func (c *socketClient) RuleList(ctx context.Context, q contract.RuleListRequest) (contract.RuleListResponse, error) {
	return call[contract.RuleListRequest, contract.RuleListResponse](ctx, c, contract.MethodRuleList, q)
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
	var attachment contract.Attachment
	file, err := os.Open(path)
	if err != nil {
		return attachment, err
	}
	defer file.Close()
	name := filepath.Base(path)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://dyna/v1/files?name="+url.QueryEscape(name), file)
	if err != nil {
		return attachment, err
	}
	media := mime.TypeByExtension(filepath.Ext(name))
	if media == "" {
		media = "application/octet-stream"
	}
	req.Header.Set("Content-Type", media)
	resp, err := c.http.Do(req)
	if err != nil {
		return attachment, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return attachment, fmt.Errorf("upload %s: %s", name, resp.Status)
	}
	err = json.NewDecoder(resp.Body).Decode(&attachment)
	return attachment, err
}

type commandExit int

func (e commandExit) Error() string { return fmt.Sprintf("command exited with status %d", e) }
func (c *socketClient) HostAdd(ctx context.Context, q contract.HostAddRequest) (contract.HostAddResponse, error) {
	return call[contract.HostAddRequest, contract.HostAddResponse](ctx, c, contract.MethodHostAdd, q)
}
func (c *socketClient) RoomCreate(ctx context.Context, q contract.RoomCreateRequest) (contract.RoomCreateResponse, error) {
	return call[contract.RoomCreateRequest, contract.RoomCreateResponse](ctx, c, contract.MethodRoomCreate, q)
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
	for scan.Scan() {
		line := scan.Text()
		if !strings.HasPrefix(line, "data: ") {
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
	var rulesRoom string
	rules := &cobra.Command{Use: "rules", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		result, err := getClient().RuleList(cmd.Context(), contract.RuleListRequest{Room: contract.RoomID(rulesRoom)})
		if err != nil {
			return err
		}
		for _, rule := range result.Rules {
			if _, err := fmt.Fprintf(cmd.OutOrStdout(), "r%d: %s\n", rule.Number, rule.Text); err != nil {
				return err
			}
		}
		return nil
	}}
	rules.PersistentFlags().StringVar(&rulesRoom, "room", "", "Room ID")
	rules.AddCommand(&cobra.Command{Use: "add TEXT", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		result, err := getClient().RuleAdd(cmd.Context(), contract.RuleAddRequest{Room: contract.RoomID(rulesRoom), Text: args[0]})
		if err == nil {
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "r%d\n", result.Number)
		}
		return err
	}})
	rules.AddCommand(&cobra.Command{Use: "rm NUMBER", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		number, err := strconv.Atoi(strings.TrimPrefix(args[0], "r"))
		if err != nil {
			return err
		}
		_, err = getClient().RuleRemove(cmd.Context(), contract.RuleRemoveRequest{Room: contract.RoomID(rulesRoom), Number: number})
		return err
	}})
	root.AddCommand(rules)
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
	var admin bool
	add := &cobra.Command{Use: "add NAME", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		_, err := getClient().HostAdd(cmd.Context(), contract.HostAddRequest{Name: contract.HostName(args[0]), SSH: ssh, OwnerOnlyAdmin: admin})
		return err
	}}
	add.Flags().StringVar(&ssh, "ssh", "", "SSH destination")
	add.Flags().BoolVar(&admin, "owner-only-admin", false, "Only the owner has host admin rights")
	host.AddCommand(add)
	root.AddCommand(host)

	room := &cobra.Command{Use: "room"}
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
	repo.AddCommand(addRepo, listRepos)
	room.AddCommand(create, log, repo)
	root.AddCommand(room)

	session := &cobra.Command{Use: "session"}
	var roomID, name, profile, goal, brief, sessionRepo, parent string
	start := &cobra.Command{Use: "start", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		r, err := getClient().SessionStart(cmd.Context(), contract.SessionStartRequest{Room: contract.RoomID(roomID), Name: name, Parent: contract.SessionID(parent), Repo: contract.RepoName(sessionRepo), Profile: contract.ProfileName(profile), Goal: goal, Brief: brief})
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
	session.AddCommand(start, closeCmd, restart)
	root.AddCommand(session)
	var postRoom string
	var mentions, paths []string
	post := &cobra.Command{Use: "post TEXT", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if len(paths) > 16 {
			return fmt.Errorf("a post can carry at most 16 attachments")
		}
		c := getClient()
		var attachments []contract.Attachment
		for _, path := range paths {
			attachment, err := c.Upload(cmd.Context(), path)
			if err != nil {
				return err
			}
			attachments = append(attachments, attachment)
		}
		r, err := c.Post(cmd.Context(), contract.PostRequest{Room: contract.RoomID(postRoom), Text: args[0], Mentions: mentions, Attachments: attachments})
		if err == nil {
			_, err = fmt.Fprintln(cmd.OutOrStdout(), r.Event)
		}
		return err
	}}
	post.Flags().StringArrayVar(&paths, "attach", nil, "Attach a file (repeatable)")
	post.Flags().StringVar(&postRoom, "room", "", "Room ID")
	post.Flags().StringSliceVar(&mentions, "mention", nil, "Session mentions")
	root.AddCommand(post)

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
	op.AddCommand(opList, opApprove, opDeny)
	root.AddCommand(op)

	pr := &cobra.Command{Use: "pr"}
	var prTitle, prBodyFile, prBase string
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
			Args: contract.PrOpenArgs{Title: prTitle, Body: body, Base: prBase, Draft: prDraft},
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
	prOpen.Flags().StringVar(&prBase, "base", "main", "Base branch")
	prOpen.Flags().BoolVar(&prDraft, "draft", false, "Open as a draft")
	_ = prOpen.MarkFlagRequired("title") // fails only for an undefined flag, and "title" is defined above
	pr.AddCommand(prOpen)
	root.AddCommand(pr)
}
