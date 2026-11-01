package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/spf13/cobra"
)

func (f *fakeClient) RoomSetLimits(_ context.Context, q contract.RoomSetLimitsRequest) (contract.RoomSetLimitsResponse, error) {
	f.limits = q
	return contract.RoomSetLimitsResponse{}, nil
}
func (f *fakeClient) RoomPause(context.Context, contract.RoomPauseRequest) (contract.RoomPauseResponse, error) {
	return contract.RoomPauseResponse{}, nil
}
func (f *fakeClient) SessionStop(context.Context, contract.SessionStopRequest) (contract.SessionStopResponse, error) {
	return contract.SessionStopResponse{}, nil
}

func (f *fakeClient) SessionList(context.Context, contract.SessionListRequest) (contract.SessionListResponse, error) {
	return contract.SessionListResponse{}, nil
}
func (f *fakeClient) SessionMove(context.Context, contract.SessionMoveRequest) (contract.SessionMoveResponse, error) {
	return contract.SessionMoveResponse{}, nil
}
func (f *fakeClient) OpWithdraw(context.Context, contract.OpWithdrawRequest) (contract.OpWithdrawResponse, error) {
	return contract.OpWithdrawResponse{}, nil
}

func (f *fakeClient) ContextSnapshot(context.Context, contract.ContextSnapshotRequest) (contract.ContextSnapshotResponse, error) {
	return contract.ContextSnapshotResponse{}, nil
}

type fakeClient struct {
	host         contract.HostAddRequest
	hostNoSwap   bool
	room         contract.RoomCreateRequest
	limits       contract.RoomSetLimitsRequest
	repoAdd      contract.RoomRepoAddRequest
	repoList     contract.RoomReposRequest
	start        contract.SessionStartRequest
	close        contract.SessionCloseRequest
	setProfile   contract.SessionSetProfileRequest
	restart      contract.SessionRestartRequest
	post         contract.PostRequest
	events       []contract.Event
	after        int64
	opList       contract.OpListRequest
	opDecide     contract.OpDecideRequest
	ops          []contract.Operation
	submit       contract.OpSubmitRequest
	submitResp   contract.OpSubmitResponse
	uploads      []string
	hostexecResp contract.HostexecRunResponse
	devicePair   contract.DevicePairRequest
	devices      []contract.Device
	deviceRevoke contract.DeviceRevokeRequest
	deviceScopes contract.DeviceSetScopesRequest
	hosts        []contract.HostSummary

	roomDelete     contract.RoomDeleteRequest
	roomDeleteResp contract.RoomDeleteResponse
}

func (f *fakeClient) DevicePair(_ context.Context, q contract.DevicePairRequest) (contract.DevicePairResponse, error) {
	f.devicePair = q
	return contract.DevicePairResponse{Code: "ABCDEFGHJK", ExpiresAt: time.Date(2026, 10, 31, 8, 0, 0, 0, time.UTC)}, nil
}
func (f *fakeClient) DeviceList(context.Context, contract.DeviceListRequest) (contract.DeviceListResponse, error) {
	return contract.DeviceListResponse{Devices: f.devices}, nil
}
func (f *fakeClient) DeviceRevoke(_ context.Context, q contract.DeviceRevokeRequest) (contract.DeviceRevokeResponse, error) {
	f.deviceRevoke = q
	return contract.DeviceRevokeResponse{}, nil
}
func (f *fakeClient) DeviceSetScopes(_ context.Context, q contract.DeviceSetScopesRequest) (contract.DeviceSetScopesResponse, error) {
	f.deviceScopes = q
	return contract.DeviceSetScopesResponse{}, nil
}

func (f *fakeClient) HostexecRun(context.Context, contract.HostexecRunRequest) (contract.HostexecRunResponse, error) {
	return f.hostexecResp, nil
}
func (f *fakeClient) File(context.Context, string, io.Writer) error { return nil }
func (f *fakeClient) Upload(_ context.Context, _ contract.RoomID, path string) (contract.Attachment, error) {
	f.uploads = append(f.uploads, path)
	return contract.Attachment{Name: path}, nil
}
func (f *fakeClient) HostList(context.Context, contract.HostListRequest) (contract.HostListResponse, error) {
	return contract.HostListResponse{Hosts: f.hosts}, nil
}
func (f *fakeClient) HostAdd(_ context.Context, q contract.HostAddRequest) (contract.HostAddResponse, error) {
	f.host = q
	return contract.HostAddResponse{NoSwap: f.hostNoSwap}, nil
}
func (f *fakeClient) RoomCreate(_ context.Context, q contract.RoomCreateRequest) (contract.RoomCreateResponse, error) {
	f.room = q
	return contract.RoomCreateResponse{Room: "room_1"}, nil
}
func (f *fakeClient) RoomDelete(_ context.Context, q contract.RoomDeleteRequest) (contract.RoomDeleteResponse, error) {
	f.roomDelete = q
	return f.roomDeleteResp, nil
}
func (f *fakeClient) RoomRepoAdd(_ context.Context, q contract.RoomRepoAddRequest) (contract.RoomRepoAddResponse, error) {
	f.repoAdd = q
	return contract.RoomRepoAddResponse{}, nil
}
func (f *fakeClient) RoomRepos(_ context.Context, q contract.RoomReposRequest) (contract.RoomReposResponse, error) {
	f.repoList = q
	return contract.RoomReposResponse{Repos: []contract.RoomRepo{{Repo: "owner/a", Default: true}, {Repo: "owner/b"}}}, nil
}
func (f *fakeClient) SessionStart(_ context.Context, q contract.SessionStartRequest) (contract.SessionStartResponse, error) {
	f.start = q
	return contract.SessionStartResponse{Session: "ses_1"}, nil
}
func (f *fakeClient) SessionSetProfile(_ context.Context, q contract.SessionSetProfileRequest) (contract.SessionSetProfileResponse, error) {
	f.setProfile = q
	return contract.SessionSetProfileResponse{}, nil
}
func (f *fakeClient) SessionClose(_ context.Context, q contract.SessionCloseRequest) (contract.SessionCloseResponse, error) {
	f.close = q
	return contract.SessionCloseResponse{}, nil
}
func (f *fakeClient) SessionRestart(_ context.Context, q contract.SessionRestartRequest) (contract.SessionRestartResponse, error) {
	f.restart = q
	return contract.SessionRestartResponse{}, nil
}
func (f *fakeClient) Post(_ context.Context, q contract.PostRequest) (contract.PostResponse, error) {
	f.post = q
	return contract.PostResponse{Event: "ev_1"}, nil
}
func (f *fakeClient) EventsList(_ context.Context, q contract.EventsListRequest) (contract.EventsListResponse, error) {
	f.after = q.AfterSeq
	return contract.EventsListResponse{Events: f.events}, nil
}
func (f *fakeClient) Events(_ context.Context, _ contract.RoomID, after int64, emit func(contract.Event) error) error {
	f.after = after
	return emit(contract.Event{Seq: 3, Body: contract.PostCreated{Text: "third"}})
}
func (f *fakeClient) OpList(_ context.Context, q contract.OpListRequest) (contract.OpListResponse, error) {
	f.opList = q
	return contract.OpListResponse{Operations: f.ops}, nil
}
func (f *fakeClient) OpDecide(_ context.Context, q contract.OpDecideRequest) (contract.OpDecideResponse, error) {
	f.opDecide = q
	return contract.OpDecideResponse{}, nil
}
func (f *fakeClient) OpSubmit(_ context.Context, q contract.OpSubmitRequest) (contract.OpSubmitResponse, error) {
	f.submit = q
	return f.submitResp, nil
}
func runCLI(t *testing.T, f *fakeClient, args ...string) string {
	t.Helper()
	out, err := execCLI(f, args...)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func execCLI(f *fakeClient, args ...string) (string, error) {
	root := &cobra.Command{Use: "dyna", SilenceUsage: true, SilenceErrors: true}
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	addCLI(root, func() client { return f })
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), err
}
func TestHostAddMapsFlags(t *testing.T) {
	f := &fakeClient{}
	runCLI(t, f, "host", "add", "h", "--ssh", "u@h", "--owner-only-admin")
	if f.host.Name != "h" || f.host.SSH != "u@h" || !f.host.OwnerOnlyAdmin {
		t.Fatalf("%+v", f.host)
	}
}
func TestHostAddWarnsWithoutSwap(t *testing.T) {
	out := runCLI(t, &fakeClient{hostNoSwap: true}, "host", "add", "h", "--ssh", "u@h", "--owner-only-admin")
	if !strings.Contains(out, "host has no swap") {
		t.Fatalf("warning: %q", out)
	}
}
func TestRoomCreateMapsName(t *testing.T) {
	f := &fakeClient{}
	runCLI(t, f, "room", "create", "work", "--root-profile", "p")
	if f.room.Name != "work" || f.room.RootProfile != "p" {
		t.Fatalf("%+v", f.room)
	}
}
func TestRoomDeleteNeedsDiscard(t *testing.T) {
	f := &fakeClient{roomDeleteResp: contract.RoomDeleteResponse{OpenedAccounts: []contract.AccountID{"acct"}, OpenedHosts: []contract.HostName{"h"}}}
	if _, err := execCLI(f, "room", "delete", "r"); err == nil || !strings.Contains(err.Error(), "--discard") || f.roomDelete.Room != "" {
		t.Fatalf("err=%v request=%+v", err, f.roomDelete)
	}
	out := runCLI(t, f, "room", "delete", "r", "--discard")
	if f.roomDelete.Room != "r" || !f.roomDelete.Discard {
		t.Fatalf("%+v", f.roomDelete)
	}
	if !strings.Contains(out, "account acct was limited to this room") || !strings.Contains(out, "host h was limited to this room") {
		t.Fatal(out)
	}
}
func TestRoomRepos(t *testing.T) {
	f := &fakeClient{}
	runCLI(t, f, "room", "repo", "add", "r", "owner/a", "--default")
	if f.repoAdd.Room != "r" || f.repoAdd.Repo != "owner/a" || !f.repoAdd.Default {
		t.Fatalf("%+v", f.repoAdd)
	}
	out := runCLI(t, f, "room", "repo", "list", "r")
	if f.repoList.Room != "r" || out != "owner/a (default)\nowner/b\n" {
		t.Fatalf("%+v: %q", f.repoList, out)
	}
}
func TestSessionStartMapsFlags(t *testing.T) {
	f := &fakeClient{}
	runCLI(t, f, "session", "start", "--room", "r", "--name", "n", "--profile", "p", "--goal", "g", "--brief", "b", "--repo", "owner/name", "--parent", "s", "--size", "large")
	if f.start.Size != "large" || f.start.Room != "r" || f.start.Name != "n" || f.start.Profile != "p" || f.start.Goal != "g" || f.start.Brief != "b" || f.start.Repo != "owner/name" || f.start.Parent != "s" {
		t.Fatalf("%+v", f.start)
	}
}
func TestSessionSetProfileMapsArgs(t *testing.T) {
	f := &fakeClient{}
	runCLI(t, f, "session", "set-profile", "ses_1", "gpt")
	if f.setProfile.Session != "ses_1" || f.setProfile.Profile != "gpt" {
		t.Fatalf("%+v", f.setProfile)
	}
}
func TestSessionRestartMapsID(t *testing.T) {
	f := &fakeClient{}
	runCLI(t, f, "session", "restart", "s")
	if f.restart.Session != "s" {
		t.Fatalf("%+v", f.restart)
	}
	if _, err := execCLI(f, "session", "restart"); err == nil {
		t.Fatal("restart accepted a missing session")
	}
	help := runCLI(t, f, "session", "restart", "--help")
	if !strings.Contains(help, "old volume and unpushed work are lost") || !strings.Contains(help, "old host is unavailable") {
		t.Fatalf("restart help does not explain data loss and orphaned resources: %q", help)
	}
}
func TestSessionCloseDiscard(t *testing.T) {
	f := &fakeClient{}
	runCLI(t, f, "session", "close", "s", "--discard")
	if f.close.Session != "s" || !f.close.Discard {
		t.Fatalf("%+v", f.close)
	}
}

func TestSessionCloseMapsID(t *testing.T) {
	f := &fakeClient{}
	runCLI(t, f, "session", "close", "s")
	if f.close.Session != "s" {
		t.Fatalf("%+v", f.close)
	}
}
func TestPostMapsFlags(t *testing.T) {
	t.Setenv(contract.ToolCallIDEnv, "call-1")
	f := &fakeClient{}
	runCLI(t, f, "post", "hello", "--room", "r", "--mention", "bob")
	if f.post.Room != "r" || f.post.Text != "hello" || f.post.ToolCallID != "call-1" || len(f.post.Mentions) != 1 || f.post.Mentions[0] != "bob" {
		t.Fatalf("%+v", f.post)
	}
}
func TestPostAttachments(t *testing.T) {
	f := &fakeClient{}
	runCLI(t, f, "post", "hello", "--attach", "one.png", "--attach", "two.txt")
	if len(f.post.Attachments) != 2 || f.post.Attachments[0].Name != "one.png" || f.post.Attachments[1].Name != "two.txt" {
		t.Fatalf("attachments: %+v", f.post.Attachments)
	}
	args := []string{"post", "hello"}
	for i := 0; i < 17; i++ {
		args = append(args, "--attach", "file")
	}
	f.uploads = nil
	if _, err := execCLI(f, args...); err == nil || len(f.uploads) != 0 {
		t.Fatalf("too many attachments: err=%v uploads=%v", err, f.uploads)
	}
}
func TestHostexecPending(t *testing.T) {
	f := &fakeClient{hostexecResp: contract.HostexecRunResponse{Operation: contract.Operation{ID: "op_1", State: contract.OpPending}}}
	out := runCLI(t, f, "hostexec", "run", "--host", "h", "--", "true")
	if !strings.Contains(out, "waits for approval") || !strings.Contains(out, "op_1") || !strings.Contains(out, "op.ran") {
		t.Fatalf("pending output: %q", out)
	}
}
func TestRoomLogFollowsInOrder(t *testing.T) {
	f := &fakeClient{events: []contract.Event{{Seq: 1, Body: contract.PostCreated{Text: "first"}}, {Seq: 2, Body: contract.PostCreated{Text: "second"}}}}
	out := runCLI(t, f, "room", "log", "r", "--follow")
	if f.after != 2 || !(strings.Index(out, "first") < strings.Index(out, "second") && strings.Index(out, "second") < strings.Index(out, "third")) {
		t.Fatalf("after=%d output=%s", f.after, out)
	}
}
func TestOpListPrintsOperationsAsJSON(t *testing.T) {
	f := &fakeClient{ops: []contract.Operation{{ID: "op_1", Room: "r", State: contract.OpState("pending")}}}
	out := runCLI(t, f, "op", "list", "r")
	var got []struct {
		ID    contract.OpID    `json:"id"`
		State contract.OpState `json:"state"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output is not JSON: %v: %s", err, out)
	}
	if f.opList.Room != "r" || len(got) != 1 || got[0].ID != "op_1" || got[0].State != "pending" {
		t.Fatalf("%+v: %s", f.opList, out)
	}
}
func TestOpApproveMapsID(t *testing.T) {
	f := &fakeClient{}
	runCLI(t, f, "op", "approve", "op_1")
	if f.opDecide.ID != "op_1" || !f.opDecide.Approve {
		t.Fatalf("%+v", f.opDecide)
	}
}
func TestOpDenyMapsReason(t *testing.T) {
	f := &fakeClient{}
	runCLI(t, f, "op", "deny", "op_1", "--reason", "too broad")
	if f.opDecide.ID != "op_1" || f.opDecide.Approve || f.opDecide.Reason != "too broad" {
		t.Fatalf("%+v", f.opDecide)
	}
}
func TestPrOpenMapsFlagsAndBody(t *testing.T) {
	body := filepath.Join(t.TempDir(), "body.md")
	if err := os.WriteFile(body, []byte("## Summary\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := &fakeClient{submitResp: contract.OpSubmitResponse{Operation: contract.Operation{Result: &contract.OpResult{Output: "https://example.test/pr/1"}}}}
	out := runCLI(t, f, "pr", "open", "--title", "Add thing", "--body-file", body, "--base", "dev", "--draft")
	args, ok := f.submit.Operation.Args.(contract.PrOpenArgs)
	if !ok || f.submit.Operation.Kind != "pr.open" || args.Title != "Add thing" || args.Body != "## Summary\n" || args.Base != "dev" || !args.Draft {
		t.Fatalf("%+v", f.submit)
	}
	if out != "https://example.test/pr/1\n" {
		t.Fatalf("output=%q", out)
	}
}
func TestPrOpenDefaultsBaseToMain(t *testing.T) {
	f := &fakeClient{submitResp: contract.OpSubmitResponse{Operation: contract.Operation{Result: &contract.OpResult{}}}}
	runCLI(t, f, "pr", "open", "--title", "t")
	args, ok := f.submit.Operation.Args.(contract.PrOpenArgs)
	if !ok || args.Base != "main" || args.Draft || args.Body != "" {
		t.Fatalf("%+v", f.submit)
	}
}
func TestPrOpenReportsFailedResult(t *testing.T) {
	f := &fakeClient{submitResp: contract.OpSubmitResponse{Operation: contract.Operation{ID: "op_1", State: contract.OpFailed, Result: &contract.OpResult{ExitCode: 1, Error: "gh: not logged in"}}}}
	_, err := execCLI(f, "pr", "open", "--title", "t")
	if err == nil || !strings.Contains(err.Error(), "gh: not logged in") {
		t.Fatalf("err=%v", err)
	}
}
func TestPrOpenReportsMissingResult(t *testing.T) {
	f := &fakeClient{submitResp: contract.OpSubmitResponse{Operation: contract.Operation{ID: "op_1", State: contract.OpPending}}}
	_, err := execCLI(f, "pr", "open", "--title", "t")
	if err == nil || !strings.Contains(err.Error(), "pending") {
		t.Fatalf("err=%v", err)
	}
}

func TestDevicePairMapsRepeatableScopes(t *testing.T) {
	f := &fakeClient{}
	out := runCLI(t, f, "device", "pair", "--scope", "read", "--scope", "files")
	if len(f.devicePair.Scopes) != 2 || f.devicePair.Scopes[0] != contract.ScopeRead || f.devicePair.Scopes[1] != contract.ScopeFiles {
		t.Fatalf("%+v", f.devicePair)
	}
	if out != "ABCDEFGHJK expires 2026-10-31T08:00:00Z\n" {
		t.Fatalf("output=%q", out)
	}
}
func TestDeviceListPrintsDevices(t *testing.T) {
	f := &fakeClient{devices: []contract.Device{{
		ID: "dev_1", Name: "phone", Scopes: []contract.Scope{contract.ScopeRead, contract.ScopeOperate},
		LastSeenAt: time.Date(2026, 10, 31, 7, 0, 0, 0, time.UTC), ExpiresAt: time.Date(2026, 11, 30, 7, 0, 0, 0, time.UTC),
	}}}
	out := runCLI(t, f, "device", "list")
	if out != "dev_1\tphone\tread,operate\t2026-10-31T07:00:00Z\t2026-11-30T07:00:00Z\n" {
		t.Fatalf("output=%q", out)
	}
}
func TestHostListPrintsDiskState(t *testing.T) {
	f := &fakeClient{hosts: []contract.HostSummary{{Name: contract.ControlPlane, DiskBytes: 100 << 30, DiskUsed: 40 << 30}, {Name: "worker", DiskBytes: 100 << 30, DiskUsed: 90 << 30, DiskLow: true}}}
	out := runCLI(t, f, "host", "list")
	if out != "control-plane\t40 GiB\t100 GiB\tok\nworker\t90 GiB\t100 GiB\tlow\n" {
		t.Fatalf("output=%q", out)
	}
}
func TestDeviceRevokeMapsID(t *testing.T) {
	f := &fakeClient{}
	runCLI(t, f, "device", "revoke", "dev_1")
	if f.deviceRevoke.ID != "dev_1" {
		t.Fatalf("%+v", f.deviceRevoke)
	}
}
func TestDeviceSetScopesRequiresScope(t *testing.T) {
	f := &fakeClient{}
	if _, err := execCLI(f, "device", "set-scopes", "dev_1"); err == nil {
		t.Fatal("set-scopes accepted a missing --scope")
	}
	runCLI(t, f, "device", "set-scopes", "dev_1", "--scope", "read", "--scope", "computers")
	if f.deviceScopes.ID != "dev_1" || len(f.deviceScopes.Scopes) != 2 || f.deviceScopes.Scopes[1] != contract.ScopeComputers {
		t.Fatalf("%+v", f.deviceScopes)
	}
}

func TestHostexecGrantSubmitsOperation(t *testing.T) {
	f := &fakeClient{submitResp: contract.OpSubmitResponse{Operation: contract.Operation{ID: "op_1"}}}
	out := runCLI(t, f, "hostexec", "grant", "--host", "hermeslab", "--reason", "Inspect logs")
	args, ok := f.submit.Operation.Args.(contract.HostexecGrantArgs)
	if !ok || out != "op_1\n" || len(args.Hosts) != 1 || args.Hosts[0] != "hermeslab" || args.Reason != "Inspect logs" || args.To.Kind != "" {
		t.Fatalf("%+v: %q", f.submit, out)
	}
	if _, err := json.Marshal(f.submit); err != nil {
		t.Fatal(err)
	}

	runCLI(t, f, "hostexec", "grant", "--host", "h", "--reason", "x", "--room", "room_1", "--to", "ses_1")
	args = f.submit.Operation.Args.(contract.HostexecGrantArgs)
	if args.To.Session != "ses_1" || args.Room != "room_1" || f.submit.Operation.Room != "room_1" {
		t.Fatalf("%+v", f.submit)
	}
}

func TestRoomLimitCommand(t *testing.T) {
	f := &fakeClient{}
	runCLI(t, f, "room", "limit", "room_1", "--cpus", "2", "--mem", "8GiB")
	if f.limits != (contract.RoomSetLimitsRequest{Room: "room_1", CPUs: 2, MemBytes: 8 << 30}) {
		t.Fatal(f.limits)
	}
}
