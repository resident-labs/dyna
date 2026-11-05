package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/dustin/go-humanize"
	"github.com/resident-labs/dyna/box/post"
	"github.com/resident-labs/dyna/box/visual"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/contract/tools"
	dynaroom "github.com/resident-labs/dyna/room"
	"github.com/spf13/cobra"
)

type client interface {
	ContextSnapshot(context.Context, contract.ContextSnapshotRequest) (contract.ContextSnapshotResponse, error)
	GoalSet(context.Context, contract.GoalSetRequest) (contract.GoalSetResponse, error)
	GoalList(context.Context, contract.GoalListRequest) (contract.GoalListResponse, error)
	GoalItem(context.Context, contract.GoalItemRequest) (contract.GoalItemResponse, error)
	GoalDone(context.Context, contract.GoalDoneRequest) (contract.GoalDoneResponse, error)
	GoalJournal(context.Context, contract.GoalJournalRequest) (contract.GoalJournalResponse, error)
	OpWithdraw(context.Context, contract.OpWithdrawRequest) (contract.OpWithdrawResponse, error)
	SessionMove(context.Context, contract.SessionMoveRequest) (contract.SessionMoveResponse, error)
	SessionList(context.Context, contract.SessionListRequest) (contract.SessionListResponse, error)
	SessionResponsibilities(context.Context, contract.SessionResponsibilitiesRequest) (contract.SessionResponsibilitiesResponse, error)
	HostexecRun(context.Context, contract.HostexecRunRequest) (contract.HostexecRunResponse, error)
	HostexecStop(context.Context, contract.HostexecStopRequest) (contract.HostexecStopResponse, error)
	File(context.Context, string, io.Writer) error
	Upload(context.Context, contract.RoomID, string) (contract.Attachment, error)
	HostAdd(context.Context, contract.HostAddRequest) (contract.HostAddResponse, error)
	HostSetLimits(context.Context, contract.HostSetLimitsRequest) (contract.HostSetLimitsResponse, error)
	HostSet(context.Context, contract.HostSetRequest) (contract.HostSetResponse, error)
	RoomSet(context.Context, contract.RoomSetRequest) (contract.RoomSetResponse, error)
	HostRemove(context.Context, contract.HostRemoveRequest) (contract.HostRemoveResponse, error)
	PoolAdd(context.Context, contract.PoolAddRequest) (contract.PoolAddResponse, error)
	PoolList(context.Context, contract.PoolListRequest) (contract.PoolListResponse, error)
	ServiceSet(context.Context, contract.ServiceSetRequest) (contract.ServiceSetResponse, error)
	ServiceList(context.Context, contract.ServiceListRequest) (contract.ServiceListResponse, error)
	ServiceShow(context.Context, contract.ServiceRef) (contract.ServiceShowResponse, error)
	ServiceLogs(context.Context, contract.ServiceLogsRequest) (contract.ServiceLogsResponse, error)
	ServiceRestart(context.Context, contract.ServiceRef) (contract.ServiceActionResponse, error)
	ServiceStop(context.Context, contract.ServiceRef) (contract.ServiceActionResponse, error)
	ServiceStart(context.Context, contract.ServiceRef) (contract.ServiceActionResponse, error)
	ServiceUpdate(context.Context, contract.ServiceRef) (contract.ServiceActionResponse, error)
	ServiceRollback(context.Context, contract.ServiceRef) (contract.ServiceActionResponse, error)
	ServiceRemove(context.Context, contract.ServiceRef) (contract.ServiceActionResponse, error)
	ServiceSecretSet(context.Context, contract.ServiceSecretSetRequest) (contract.ServiceSecretSetResponse, error)
	PoolRemove(context.Context, contract.PoolRemoveRequest) (contract.PoolRemoveResponse, error)
	RoomHosts(context.Context, contract.RoomHostsRequest) (contract.RoomHostsResponse, error)
	RoomSetLimits(context.Context, contract.RoomSetLimitsRequest) (contract.RoomSetLimitsResponse, error)
	RoomPause(context.Context, contract.RoomPauseRequest) (contract.RoomPauseResponse, error)
	SessionStop(context.Context, contract.SessionStopRequest) (contract.SessionStopResponse, error)
	RoomCreate(context.Context, contract.RoomCreateRequest) (contract.RoomCreateResponse, error)
	RoomDelete(context.Context, contract.RoomDeleteRequest) (contract.RoomDeleteResponse, error)
	RoomRepoAdd(context.Context, contract.RoomRepoAddRequest) (contract.RoomRepoAddResponse, error)
	RepoCISet(context.Context, contract.RepoCISetRequest) (contract.RepoCISetResponse, error)
	RoomRepos(context.Context, contract.RoomReposRequest) (contract.RoomReposResponse, error)
	SessionStart(context.Context, contract.SessionStartRequest) (contract.SessionStartResponse, error)
	ComposeList(context.Context, contract.ComposeListRequest) (contract.ComposeListResponse, error)
	SessionClose(context.Context, contract.SessionCloseRequest) (contract.SessionCloseResponse, error)
	SessionSetProfile(context.Context, contract.SessionSetProfileRequest) (contract.SessionSetProfileResponse, error)
	SessionSetBranch(context.Context, contract.SessionSetBranchRequest) (contract.SessionSetBranchResponse, error)
	SessionRestart(context.Context, contract.SessionRestartRequest) (contract.SessionRestartResponse, error)
	SessionRefresh(context.Context, contract.SessionRefreshRequest) (contract.SessionRefreshResponse, error)
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
	DeviceSetHost(context.Context, contract.DeviceSetHostRequest) (contract.DeviceSetHostResponse, error)
	PrList(context.Context, contract.PrListRequest) (contract.PrListResponse, error)
	PrStatus(context.Context, contract.PrStatusRequest) (contract.PrStatusResponse, error)
	PrTrack(context.Context, contract.PrTrackRequest) (contract.PrTrackResponse, error)
	PrUntrack(context.Context, contract.PrUntrackRequest) (contract.PrUntrackResponse, error)
	IssueList(context.Context, contract.IssueListRequest) (contract.IssueListResponse, error)
	IssueStatus(context.Context, contract.IssueStatusRequest) (contract.IssueStatusResponse, error)
	IssueTrack(context.Context, contract.IssueTrackRequest) (contract.IssueTrackResponse, error)
	IssueUntrack(context.Context, contract.IssueUntrackRequest) (contract.IssueUntrackResponse, error)
	HostList(context.Context, contract.HostListRequest) (contract.HostListResponse, error)
	ProfileList(context.Context, contract.ProfileListRequest) (contract.ProfileListResponse, error)
	ProfileSet(context.Context, contract.ProfileSetRequest) (contract.ProfileSetResponse, error)
	ProfileRemove(context.Context, contract.ProfileRemoveRequest) (contract.ProfileRemoveResponse, error)
}

type socketClient struct{ http *http.Client }

func (c *socketClient) ContextSnapshot(ctx context.Context, req contract.ContextSnapshotRequest) (contract.ContextSnapshotResponse, error) {
	return call[contract.ContextSnapshotRequest, contract.ContextSnapshotResponse](ctx, c, contract.MethodContextSnapshot, req)
}

func (c *socketClient) ProfileList(ctx context.Context, q contract.ProfileListRequest) (contract.ProfileListResponse, error) {
	return call[contract.ProfileListRequest, contract.ProfileListResponse](ctx, c, contract.MethodProfileList, q)
}
func (c *socketClient) ProfileSet(ctx context.Context, q contract.ProfileSetRequest) (contract.ProfileSetResponse, error) {
	return call[contract.ProfileSetRequest, contract.ProfileSetResponse](ctx, c, contract.MethodProfileSet, q)
}
func (c *socketClient) ProfileRemove(ctx context.Context, q contract.ProfileRemoveRequest) (contract.ProfileRemoveResponse, error) {
	return call[contract.ProfileRemoveRequest, contract.ProfileRemoveResponse](ctx, c, contract.MethodProfileRemove, q)
}

func newClient(socket string) *socketClient {
	return &socketClient{http: &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}}}
}

type requestType interface {
	contract.ProfileListRequest | contract.ProfileSetRequest | contract.ProfileRemoveRequest |
		contract.ProxyKeyRequest | contract.ProxyUsageRequest |
		contract.WorkflowRunRequest | contract.WorkflowReloadRequest | contract.WorkflowStatusRequest | contract.WorkflowListRequest | contract.WorkflowSettingsRequest | contract.WorkflowSetRequest |
		contract.AddonListRequest | contract.AddonRemoveRequest | contract.ImageSaveRequest | contract.ImageListRequest | contract.ImageShowRequest | contract.ImageRemoveRequest | contract.PackageAddRequest | contract.SkillSaveRequest | contract.AddonSetHostsRequest | contract.MCPAddRequest |
		contract.SubscriptionAddRequest | contract.SubscriptionListRequest | contract.SubscriptionRemoveRequest | contract.SubscriptionSetEnabledRequest |
		contract.WebSearchRequest | contract.WebsearchSetKeyRequest | contract.WebsearchRemoveKeyRequest |
		contract.ContextSnapshotRequest |
		contract.IssueListRequest | contract.IssueStatusRequest | contract.IssueTrackRequest | contract.IssueUntrackRequest |
		contract.QuestionAskRequest | contract.QuestionAnswerRequest | contract.QuestionListRequest |
		contract.OpWithdrawRequest |
		contract.SessionMoveRequest | contract.SessionSetBranchRequest |
		contract.SessionListRequest |
		contract.SessionResponsibilitiesRequest |
		contract.GoalSetRequest | contract.GoalListRequest | contract.GoalItemRequest | contract.GoalDoneRequest | contract.GoalJournalRequest |
		contract.ServiceSetRequest | contract.ServiceListRequest | contract.ServiceRef | contract.ServiceLogsRequest | contract.ServiceSecretSetRequest |
		contract.PoolAddRequest | contract.PoolListRequest | contract.PoolRemoveRequest | contract.RoomHostsRequest | contract.HostRemoveRequest | contract.HostSetRequest | contract.RoomSetRequest |
		contract.RoomSetLimitsRequest | contract.RoomPauseRequest | contract.SessionStopRequest | contract.HostexecRunRequest | contract.HostexecStopRequest | contract.WebLoginRequest | contract.HostAddRequest | contract.HostSetLimitsRequest | contract.HostListRequest | contract.RoomCreateRequest | contract.RoomDeleteRequest | contract.RoomRepoAddRequest | contract.RepoCISetRequest | contract.RoomReposRequest | contract.ComposeListRequest | contract.SessionStartRequest | contract.SessionCloseRequest | contract.SessionSetProfileRequest | contract.SessionRestartRequest | contract.SessionRefreshRequest | contract.PostRequest | contract.EventsListRequest | contract.OpListRequest | contract.OpDecideRequest | contract.OpSubmitRequest | contract.DevicePairRequest | contract.DeviceListRequest | contract.DeviceRevokeRequest | contract.DeviceSetScopesRequest | contract.DeviceSetHostRequest | contract.ConnectStartRequest | contract.ConnectStatusRequest | contract.ConnectCancelRequest | contract.ConnectUnlinkRequest | contract.PrListRequest | contract.PrStatusRequest | contract.PrTrackRequest | contract.PrUntrackRequest | contract.SearchRequest | contract.HistoryReadRequest | contract.TranscriptDigestRequest
}
type responseType interface {
	contract.ProfileListResponse | contract.ProfileSetResponse | contract.ProfileRemoveResponse |
		contract.ProxyKeyResponse | contract.ProxyUsageResponse |
		contract.WorkflowRunResponse | contract.WorkflowReloadResponse | contract.WorkflowStatusResponse | contract.WorkflowListResponse | contract.WorkflowSettingsResponse | contract.WorkflowSetResponse |
		contract.AddonListResponse | contract.AddonChangeResponse | contract.AddonSetHostsResponse | contract.ImageSaveResponse | contract.ImageListResponse | contract.ImageShowResponse | contract.ImageRemoveResponse |
		contract.SubscriptionAddResponse | contract.SubscriptionListResponse | contract.SubscriptionRemoveResponse | contract.SubscriptionSetEnabledResponse |
		contract.WebSearchResponse | contract.WebsearchSetKeyResponse | contract.WebsearchRemoveKeyResponse |
		contract.ContextSnapshotResponse |
		contract.IssueListResponse | contract.IssueStatusResponse | contract.IssueTrackResponse | contract.IssueUntrackResponse |
		contract.QuestionAskResponse | contract.QuestionAnswerResponse | contract.QuestionListResponse |
		contract.OpWithdrawResponse |
		contract.SessionMoveResponse | contract.SessionSetBranchResponse |
		contract.SessionListResponse |
		contract.SessionResponsibilitiesResponse |
		contract.GoalSetResponse | contract.GoalListResponse | contract.GoalItemResponse | contract.GoalDoneResponse | contract.GoalJournalResponse |
		contract.ServiceSetResponse | contract.ServiceListResponse | contract.ServiceShowResponse | contract.ServiceLogsResponse | contract.ServiceActionResponse | contract.ServiceSecretSetResponse |
		contract.PoolAddResponse | contract.PoolListResponse | contract.PoolRemoveResponse | contract.RoomHostsResponse | contract.HostRemoveResponse | contract.HostSetResponse | contract.RoomSetResponse |
		contract.RoomSetLimitsResponse | contract.RoomPauseResponse | contract.SessionStopResponse | contract.HostexecRunResponse | contract.HostexecStopResponse | contract.WebLoginResponse | contract.HostAddResponse | contract.HostSetLimitsResponse | contract.HostListResponse | contract.RoomCreateResponse | contract.RoomDeleteResponse | contract.RoomRepoAddResponse | contract.RepoCISetResponse | contract.RoomReposResponse | contract.ComposeListResponse | contract.SessionStartResponse | contract.SessionCloseResponse | contract.SessionSetProfileResponse | contract.SessionRestartResponse | contract.SessionRefreshResponse | contract.PostResponse | contract.EventsListResponse | contract.OpListResponse | contract.OpDecideResponse | contract.OpSubmitResponse | contract.DevicePairResponse | contract.DeviceListResponse | contract.DeviceRevokeResponse | contract.DeviceSetScopesResponse | contract.DeviceSetHostResponse | contract.ConnectStartResponse | contract.ConnectStatusResponse | contract.ConnectCancelResponse | contract.ConnectUnlinkResponse | contract.PrListResponse | contract.PrStatusResponse | contract.PrTrackResponse | contract.PrUntrackResponse | contract.SearchResponse | contract.HistoryReadResponse | contract.TranscriptDigestResponse
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

func (c *socketClient) GoalSet(ctx context.Context, q contract.GoalSetRequest) (contract.GoalSetResponse, error) {
	return call[contract.GoalSetRequest, contract.GoalSetResponse](ctx, c, contract.MethodGoalSet, q)
}
func (c *socketClient) GoalList(ctx context.Context, q contract.GoalListRequest) (contract.GoalListResponse, error) {
	return call[contract.GoalListRequest, contract.GoalListResponse](ctx, c, contract.MethodGoalList, q)
}
func (c *socketClient) GoalItem(ctx context.Context, q contract.GoalItemRequest) (contract.GoalItemResponse, error) {
	return call[contract.GoalItemRequest, contract.GoalItemResponse](ctx, c, contract.MethodGoalItem, q)
}
func (c *socketClient) GoalDone(ctx context.Context, q contract.GoalDoneRequest) (contract.GoalDoneResponse, error) {
	return call[contract.GoalDoneRequest, contract.GoalDoneResponse](ctx, c, contract.MethodGoalDone, q)
}
func (c *socketClient) GoalJournal(ctx context.Context, q contract.GoalJournalRequest) (contract.GoalJournalResponse, error) {
	return call[contract.GoalJournalRequest, contract.GoalJournalResponse](ctx, c, contract.MethodGoalJournal, q)
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
func (c *socketClient) HostexecStop(ctx context.Context, q contract.HostexecStopRequest) (contract.HostexecStopResponse, error) {
	return call[contract.HostexecStopRequest, contract.HostexecStopResponse](ctx, c, contract.MethodHostexecStop, q)
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

func (c *socketClient) Upload(ctx context.Context, room contract.RoomID, path string) (contract.Attachment, error) {
	return post.UploadHTTP(ctx, c.http, room, path)
}

type commandExit int

func (e commandExit) Error() string { return fmt.Sprintf("command exited with status %d", e) }
func (c *socketClient) HostAdd(ctx context.Context, q contract.HostAddRequest) (contract.HostAddResponse, error) {
	return call[contract.HostAddRequest, contract.HostAddResponse](ctx, c, contract.MethodHostAdd, q)
}
func (c *socketClient) HostRemove(ctx context.Context, q contract.HostRemoveRequest) (contract.HostRemoveResponse, error) {
	return call[contract.HostRemoveRequest, contract.HostRemoveResponse](ctx, c, contract.MethodHostRemove, q)
}
func (c *socketClient) PoolAdd(ctx context.Context, q contract.PoolAddRequest) (contract.PoolAddResponse, error) {
	return call[contract.PoolAddRequest, contract.PoolAddResponse](ctx, c, contract.MethodPoolAdd, q)
}
func (c *socketClient) PoolList(ctx context.Context, q contract.PoolListRequest) (contract.PoolListResponse, error) {
	return call[contract.PoolListRequest, contract.PoolListResponse](ctx, c, contract.MethodPoolList, q)
}
func (c *socketClient) PoolRemove(ctx context.Context, q contract.PoolRemoveRequest) (contract.PoolRemoveResponse, error) {
	return call[contract.PoolRemoveRequest, contract.PoolRemoveResponse](ctx, c, contract.MethodPoolRemove, q)
}
func (c *socketClient) RoomHosts(ctx context.Context, q contract.RoomHostsRequest) (contract.RoomHostsResponse, error) {
	return call[contract.RoomHostsRequest, contract.RoomHostsResponse](ctx, c, contract.MethodRoomHosts, q)
}
func (c *socketClient) HostList(ctx context.Context, q contract.HostListRequest) (contract.HostListResponse, error) {
	return call[contract.HostListRequest, contract.HostListResponse](ctx, c, contract.MethodHostList, q)
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
func (c *socketClient) HostSetLimits(ctx context.Context, q contract.HostSetLimitsRequest) (contract.HostSetLimitsResponse, error) {
	return call[contract.HostSetLimitsRequest, contract.HostSetLimitsResponse](ctx, c, contract.MethodHostSetLimits, q)
}
func (c *socketClient) RoomSet(ctx context.Context, q contract.RoomSetRequest) (contract.RoomSetResponse, error) {
	return call[contract.RoomSetRequest, contract.RoomSetResponse](ctx, c, contract.MethodRoomSet, q)
}
func (c *socketClient) HostSet(ctx context.Context, q contract.HostSetRequest) (contract.HostSetResponse, error) {
	return call[contract.HostSetRequest, contract.HostSetResponse](ctx, c, contract.MethodHostSet, q)
}
func (c *socketClient) RoomDelete(ctx context.Context, q contract.RoomDeleteRequest) (contract.RoomDeleteResponse, error) {
	return call[contract.RoomDeleteRequest, contract.RoomDeleteResponse](ctx, c, contract.MethodRoomDelete, q)
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
func (c *socketClient) ComposeList(ctx context.Context, q contract.ComposeListRequest) (contract.ComposeListResponse, error) {
	return call[contract.ComposeListRequest, contract.ComposeListResponse](ctx, c, contract.MethodComposeList, q)
}
func (c *socketClient) SessionStart(ctx context.Context, q contract.SessionStartRequest) (contract.SessionStartResponse, error) {
	return call[contract.SessionStartRequest, contract.SessionStartResponse](ctx, c, contract.MethodSessionStart, q)
}
func (c *socketClient) SessionSetBranch(ctx context.Context, q contract.SessionSetBranchRequest) (contract.SessionSetBranchResponse, error) {
	return call[contract.SessionSetBranchRequest, contract.SessionSetBranchResponse](ctx, c, contract.MethodSessionSetBranch, q)
}
func (c *socketClient) SessionSetProfile(ctx context.Context, q contract.SessionSetProfileRequest) (contract.SessionSetProfileResponse, error) {
	return call[contract.SessionSetProfileRequest, contract.SessionSetProfileResponse](ctx, c, contract.MethodSessionSetProfile, q)
}
func (c *socketClient) SessionResponsibilities(ctx context.Context, q contract.SessionResponsibilitiesRequest) (contract.SessionResponsibilitiesResponse, error) {
	return call[contract.SessionResponsibilitiesRequest, contract.SessionResponsibilitiesResponse](ctx, c, contract.MethodSessionResponsibilities, q)
}
func (c *socketClient) SessionClose(ctx context.Context, q contract.SessionCloseRequest) (contract.SessionCloseResponse, error) {
	return call[contract.SessionCloseRequest, contract.SessionCloseResponse](ctx, c, contract.MethodSessionClose, q)
}
func (c *socketClient) SessionRestart(ctx context.Context, q contract.SessionRestartRequest) (contract.SessionRestartResponse, error) {
	return call[contract.SessionRestartRequest, contract.SessionRestartResponse](ctx, c, contract.MethodSessionRestart, q)
}
func (c *socketClient) SessionRefresh(ctx context.Context, q contract.SessionRefreshRequest) (contract.SessionRefreshResponse, error) {
	return call[contract.SessionRefreshRequest, contract.SessionRefreshResponse](ctx, c, contract.MethodSessionRefresh, q)
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
func (c *socketClient) PrList(ctx context.Context, q contract.PrListRequest) (contract.PrListResponse, error) {
	return call[contract.PrListRequest, contract.PrListResponse](ctx, c, contract.MethodPrList, q)
}
func (c *socketClient) PrStatus(ctx context.Context, q contract.PrStatusRequest) (contract.PrStatusResponse, error) {
	return call[contract.PrStatusRequest, contract.PrStatusResponse](ctx, c, contract.MethodPrStatus, q)
}
func (c *socketClient) PrTrack(ctx context.Context, q contract.PrTrackRequest) (contract.PrTrackResponse, error) {
	return call[contract.PrTrackRequest, contract.PrTrackResponse](ctx, c, contract.MethodPrTrack, q)
}
func (c *socketClient) PrUntrack(ctx context.Context, q contract.PrUntrackRequest) (contract.PrUntrackResponse, error) {
	return call[contract.PrUntrackRequest, contract.PrUntrackResponse](ctx, c, contract.MethodPrUntrack, q)
}
func (c *socketClient) IssueList(ctx context.Context, q contract.IssueListRequest) (contract.IssueListResponse, error) {
	return call[contract.IssueListRequest, contract.IssueListResponse](ctx, c, contract.MethodIssueList, q)
}
func (c *socketClient) IssueStatus(ctx context.Context, q contract.IssueStatusRequest) (contract.IssueStatusResponse, error) {
	return call[contract.IssueStatusRequest, contract.IssueStatusResponse](ctx, c, contract.MethodIssueStatus, q)
}
func (c *socketClient) IssueTrack(ctx context.Context, q contract.IssueTrackRequest) (contract.IssueTrackResponse, error) {
	return call[contract.IssueTrackRequest, contract.IssueTrackResponse](ctx, c, contract.MethodIssueTrack, q)
}
func (c *socketClient) IssueUntrack(ctx context.Context, q contract.IssueUntrackRequest) (contract.IssueUntrackResponse, error) {
	return call[contract.IssueUntrackRequest, contract.IssueUntrackResponse](ctx, c, contract.MethodIssueUntrack, q)
}
func (c *socketClient) DeviceSetHost(ctx context.Context, q contract.DeviceSetHostRequest) (contract.DeviceSetHostResponse, error) {
	return call[contract.DeviceSetHostRequest, contract.DeviceSetHostResponse](ctx, c, contract.MethodDeviceSetHost, q)
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

// hostSwitch reads a host switch flag, which is on or off.
func hostSwitch(name, value string) (*bool, error) {
	switch value {
	case "on":
		return new(true), nil
	case "off":
		return new(false), nil
	}
	return nil, fmt.Errorf("--%s takes on or off, not %q", name, value)
}

func onOff(on bool) string {
	if on {
		return "on"
	}
	return "off"
}

func addCLI(root *cobra.Command, getClient func() client) {
	hostexec := &cobra.Command{Use: "hostexec"}
	var runHost, cwd string
	var timeout time.Duration
	run := &cobra.Command{Use: "bash --host HOST COMMAND", Short: "Run a command on a host with bash -c, under a host grant.", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		c := getClient()
		var id [16]byte
		_, _ = rand.Read(id[:])
		req := contract.HostexecRunRequest{Host: contract.HostName(runHost), Command: args[0], Cwd: cwd, Timeout: contract.DurationMS(timeout.Milliseconds()), ClientID: hex.EncodeToString(id[:])}
		// An interrupt stops the run and still waits for its result. A dropped call would not stop it, so the stop is asked
		// for by name, and a second interrupt ends this at once.
		interrupted, restore := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer restore()
		finished := make(chan struct{})
		defer close(finished)
		go func() {
			select {
			case <-finished:
				return
			case <-interrupted.Done():
			}
			restore()
			stopCtx, cancel := context.WithTimeout(context.WithoutCancel(cmd.Context()), 5*time.Second)
			defer cancel()
			if _, err := c.HostexecStop(stopCtx, contract.HostexecStopRequest{ClientID: req.ClientID}); err != nil {
				fmt.Fprintln(cmd.ErrOrStderr(), "The run could not be stopped:", err)
			}
		}()
		res, err := c.HostexecRun(cmd.Context(), req)
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
	grant := &cobra.Command{Use: "grant --host HOST --reason WHY", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		args := contract.HostexecGrantArgs{Room: contract.RoomID(grantRoom), Hosts: []contract.HostName{contract.HostName(grantHost)}, Reason: grantReason}
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
	grant.Flags().StringVar(&grantReason, "reason", "", "Why the session needs access")
	grant.Flags().StringVar(&grantTo, "to", "", "Session that gets the grant (owner only)")
	grant.Flags().StringVar(&grantRoom, "room", "", "Room ID (owner only)")
	_ = grant.MarkFlagRequired("host")   // fails only for an undefined flag
	_ = grant.MarkFlagRequired("reason") // fails only for an undefined flag
	run.Flags().StringVar(&runHost, "host", "", "Host name")
	run.Flags().StringVar(&cwd, "cwd", "", "Working directory")
	run.Flags().DurationVar(&timeout, "timeout", 10*time.Minute, "Run timeout")
	_ = run.MarkFlagRequired("host")
	hostexec.AddCommand(grant)
	root.AddCommand(hostexec, run)

	host := &cobra.Command{Use: "host"}
	var ssh string
	var hostRooms []string
	var admin bool
	var addAgents, addCI string
	add := &cobra.Command{Use: "add NAME", Long: "Add a host. With --agents off and --ci off the host is exec only: it needs just SSH and bash, takes host-exec runs, and never has a box or runner. With --agents on (the default) new session boxes may land on it; with --ci on (the default, except on the host the engine runs on) the CI pool may start runners there, placed as boxes are.", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		rooms := make([]contract.RoomID, len(hostRooms))
		for i, room := range hostRooms {
			rooms[i] = contract.RoomID(room)
		}
		agents, err := hostSwitch("agents", addAgents)
		if err != nil {
			return err
		}
		var ci *bool
		if cmd.Flags().Changed("ci") {
			if ci, err = hostSwitch("ci", addCI); err != nil {
				return err
			}
		}
		result, err := getClient().HostAdd(cmd.Context(), contract.HostAddRequest{Name: contract.HostName(args[0]), SSH: ssh, OwnerOnlyAdmin: admin, Rooms: rooms, Agents: agents, CI: ci})
		if err == nil && result.NoSwap {
			fmt.Fprintln(cmd.ErrOrStderr(), "Warning: host has no swap; memory pressure can kill box processes.")
		}
		return err
	}}
	add.Flags().StringSliceVar(&hostRooms, "room", nil, "Room ID allowed on this host (repeatable; default: all rooms)")
	add.Flags().StringVar(&ssh, "ssh", "", "SSH destination")
	add.Flags().BoolVar(&admin, "owner-only-admin", false, "Confirm that only the owner administers the machine; a host that runs boxes or CI runners needs it, an exec-only host (--agents off --ci off) does not")
	add.Flags().StringVar(&addAgents, "agents", "on", "on or off: whether new session boxes may land on this host")
	add.Flags().StringVar(&addCI, "ci", "", "on or off: whether the CI pool may start runners on this host (default on, off on the engine's own host)")
	var setAgents, setCI string
	var setAdmin bool
	var clearCreateProblem bool
	setHost := &cobra.Command{Use: "set NAME [--agents on|off] [--ci on|off]", Short: "Turn a host's agents and CI switches", Long: "Turn a host's two switches. With agents off no new session box lands on the host; its room list still says which rooms it serves while agents are on. With ci off the CI pool starts no new runner there, and runners come back when ci is on again. Both off disables the host and keeps it registered. A host added exec only (`host add --agents off --ci off`) stays exec only, with an SSH and bash check alone, whatever its switches say. Turning agents or ci on for an exec-only host runs the full host check first, which needs --owner-only-admin as host add does. Boxes and runners already on the host stay until they stop, and a stopped box there waits until agents are on again.", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		req := contract.HostSetRequest{Host: contract.HostName(args[0]), ClearCreateProblem: clearCreateProblem, OwnerOnlyAdmin: setAdmin}
		var err error
		if cmd.Flags().Changed("agents") {
			if req.Agents, err = hostSwitch("agents", setAgents); err != nil {
				return err
			}
		}
		if cmd.Flags().Changed("ci") {
			if req.CI, err = hostSwitch("ci", setCI); err != nil {
				return err
			}
		}
		if req.Agents == nil && req.CI == nil && !req.ClearCreateProblem {
			return fmt.Errorf("pass --agents, --ci or --clear-create-problem")
		}
		_, err = getClient().HostSet(cmd.Context(), req)
		return err
	}}
	setHost.Flags().BoolVar(&setAdmin, "owner-only-admin", false, "Confirm that only the owner administers the machine; turning agents or ci on for an exec-only host needs it")
	setHost.Flags().BoolVar(&clearCreateProblem, "clear-create-problem", false, "Clear the host's create-failure quarantine")
	setHost.Flags().StringVar(&setAgents, "agents", "", "on or off: whether new session boxes may land on this host")
	setHost.Flags().StringVar(&setCI, "ci", "", "on or off: whether the CI pool may start runners on this host")
	var limitRooms []string
	var limitAll bool
	limitHost := &cobra.Command{Use: "limit NAME", Short: "Set the rooms a host serves", Long: "Replace the rooms a host serves. Boxes already on the host stay there. A room delete refuses while it is the only room of a host, so this is how to clear that refusal.", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if limitAll == (len(limitRooms) > 0) {
			return fmt.Errorf("pass --room for each room to serve, or --all for every room")
		}
		rooms := make([]contract.RoomID, len(limitRooms))
		for i, room := range limitRooms {
			rooms[i] = contract.RoomID(room)
		}
		_, err := getClient().HostSetLimits(cmd.Context(), contract.HostSetLimitsRequest{Host: contract.HostName(args[0]), Rooms: rooms})
		return err
	}}
	limitHost.Flags().StringSliceVar(&limitRooms, "room", nil, "Room ID this host serves (repeatable)")
	limitHost.Flags().BoolVar(&limitAll, "all", false, "Serve every room")
	list := &cobra.Command{Use: "list", Args: cobra.NoArgs, Short: "Show each host's disk use", RunE: func(cmd *cobra.Command, _ []string) error {
		r, err := getClient().HostList(cmd.Context(), contract.HostListRequest{})
		if err != nil {
			return err
		}
		for _, h := range r.Hosts {
			state, used, total := "ok", humanize.IBytes(uint64(h.DiskUsed)), humanize.IBytes(uint64(h.DiskBytes))
			if h.ExecOnly {
				state, used, total = "offline", "-", "-"
				if h.Reachable {
					state = "online"
				}
			} else if h.DiskLow {
				state = "low"
			}
			if h.Health == contract.HostDegraded {
				state += "\tdegraded; no new boxes or runners"
			}
			if h.Problem == contract.HostProblemCreateFailed {
				state += "\tbox creation failed; clear with host set --clear-create-problem"
			}
			if h.Problem == contract.HostProblemShareUnavailable {
				state += "\tshared folder mount unavailable, so no session boxes (CI may run); fix FUSE, then host set --clear-create-problem"
			}
			for _, p := range h.RunnerImages {
				state += fmt.Sprintf("\tCI runner image of room %s (%s) failed to build, jobs use the shared image: %s", p.Room, p.Image, p.Reason)
			}
			if h.Leftovers > 0 {
				state += fmt.Sprintf("\t%d left from deleted rooms", h.Leftovers)
			}
			if h.Pool != "" {
				state += "\tpool " + h.Pool
			}
			if h.ExecOnly {
				state += "\texec only"
			} else if h.Name != contract.ControlPlane {
				state += "\tagents " + onOff(h.Agents) + "\tci " + onOff(h.CI)
			}
			if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%s\n", h.Name, used, total, state); err != nil {
				return err
			}
		}
		return nil
	}}
	var discardBoxes bool
	removeHost := &cobra.Command{Use: "remove NAME [--discard-boxes]", Short: "Remove a host with nothing on it", Long: "Remove a host and its forward. It refuses while a box is on the host, and says what is. A pool's node goes back through its script.\n\n" +
		"With --discard-boxes it removes the host with its boxes, as a host lost for good, whether or not the host answers. Every box on it becomes failed with reason \"host removed\", and its volume is deleted: work that was not pushed is lost. On a host that does not answer, the volumes go with the machine. Each session's parent hears of the failure once, and `dyna session restart SESSION` gives the session a new, empty box on another host. A session being closed is closed without its archive. It prints the sessions to restart.", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		r, err := getClient().HostRemove(cmd.Context(), contract.HostRemoveRequest{Host: contract.HostName(args[0]), DiscardBoxes: discardBoxes})
		if err != nil {
			return err
		}
		for _, session := range r.Failed {
			if _, err := fmt.Fprintln(cmd.OutOrStdout(), session); err != nil {
				return err
			}
		}
		return nil
	}}
	removeHost.Flags().BoolVar(&discardBoxes, "discard-boxes", false, "Fail every box on the host and delete its volume; work not pushed is lost")
	host.AddCommand(add, setHost, limitHost, list, removeHost)
	root.AddCommand(host)
	root.AddCommand(poolCommand(getClient))
	root.AddCommand(serviceCommand(getClient))
	root.AddCommand(profileCommand(getClient))

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
	var githubWrites string
	rootApproves := map[contract.RootApproval]*string{}
	for _, kind := range contract.AllRootApprovals {
		rootApproves[kind] = new(string)
	}
	setRoom := &cobra.Command{Use: "set ROOM [--root-approves-KIND on|off] [--github-writes GROUPS@REPOS|off]", Short: "Change a room setting", Long: "Change a room setting; a setting left out stays. --github-writes lets the room's sessions make GitHub writes without a host grant: GROUPS are issues, pulls and projects, comma-separated, and REPOS are repositories of the room or *, comma-separated, as in issues,projects@owner/repo; off turns it off. It is off by default, and merges, deletes and everything else stay refused. With --root-approves-KIND on, the room's root decides that kind of its sessions' requests with op approve and op deny, as the owner can, and the owner still can. KIND is hostexec (host exec grants), addons (global skills, packages and MCP servers), hosts (room hosts rules) or pools (pool add). Each is off by default, and the request still waits for a decision.", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		req := contract.RoomSetRequest{Room: contract.RoomID(args[0]), RootApproves: map[contract.RootApproval]bool{}}
		for _, kind := range contract.AllRootApprovals {
			if *rootApproves[kind] == "" {
				continue
			}
			on, err := hostSwitch("root-approves-"+string(kind), *rootApproves[kind])
			if err != nil {
				return err
			}
			req.RootApproves[kind] = *on
		}
		if githubWrites != "" {
			writes, err := parseGitHubWrites(githubWrites)
			if err != nil {
				return err
			}
			req.GitHubWrites = &writes
		}
		if len(req.RootApproves) == 0 && req.GitHubWrites == nil {
			return fmt.Errorf("set at least one --root-approves-KIND on|off or --github-writes")
		}
		_, err := getClient().RoomSet(cmd.Context(), req)
		return err
	}}
	for _, kind := range contract.AllRootApprovals {
		setRoom.Flags().StringVar(rootApproves[kind], "root-approves-"+string(kind), "", "on or off: whether the room's root may decide its sessions' "+string(kind)+" requests")
	}
	setRoom.Flags().StringVar(&githubWrites, "github-writes", "", "GROUPS@REPOS (issues,pulls,projects @ repositories of the room or *), or off: the GitHub writes the room's sessions may make")
	room.AddCommand(limit, setRoom, roomHostsCommand(getClient))
	var rootProfile string
	create := &cobra.Command{Use: "create NAME", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		r, err := getClient().RoomCreate(cmd.Context(), contract.RoomCreateRequest{Name: args[0], RootProfile: contract.ProfileName(rootProfile)})
		if err == nil {
			_, err = fmt.Fprintln(cmd.OutOrStdout(), r.Room)
		}
		return err
	}}
	create.Flags().StringVar(&rootProfile, "root-profile", "", "Root session profile")
	var discardRoom bool
	deleteRoom := &cobra.Command{Use: "delete ROOM --discard", Short: "Delete a room and all its data", Long: "Delete a room: its sessions, boxes, posts, files, transcripts and shared folder. Unpushed work is lost and archive refs are deleted. The deletion runs in the background; it resumes after a restart. A host that is offline is cleaned when it returns; host list counts what it still holds. It refuses while an account or host is limited to this room alone, because an empty limit means every room: change those limits first.", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if !discardRoom {
			return fmt.Errorf("deleting a room discards all its work; pass --discard to confirm")
		}
		_, err := getClient().RoomDelete(cmd.Context(), contract.RoomDeleteRequest{Room: contract.RoomID(args[0]), Discard: true})
		return err
	}}
	deleteRoom.Flags().BoolVar(&discardRoom, "discard", false, "Confirm that all the room's work is discarded")
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
	var ciOn, ciOff bool
	var ciMax int
	repoCI := &cobra.Command{Use: "ci REPO", Short: "Turn a repository's CI runners on or off", Long: "--on serves the repository's jobs that say runs-on: dyna-small, dyna-medium, dyna-large or dyna-xlarge, each from a runner of that size, on a host with ci on that admission picks as it picks one for a box, or on a node of the room's pool. --max bounds its runners across sizes. --off removes them at once. See next/docs/ci-runners.md.", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		_, err := getClient().RepoCISet(cmd.Context(), contract.RepoCISetRequest{Repo: contract.RepoName(args[0]), On: ciOn, Max: ciMax})
		return err
	}}
	repoCI.Flags().BoolVar(&ciOn, "on", false, "Serve the repository's jobs from Dyna's runners")
	repoCI.Flags().BoolVar(&ciOff, "off", false, "Stop its runners, even mid-job, and leave its jobs to GitHub-hosted runners")
	repoCI.Flags().IntVar(&ciMax, "max", 0, "Most runners at once across sizes; none means as many as admission allows")
	repoCI.MarkFlagsOneRequired("on", "off")
	repoCI.MarkFlagsMutuallyExclusive("on", "off")
	repoCI.MarkFlagsMutuallyExclusive("off", "max")
	repo.AddCommand(addRepo, listRepos, repoCI)
	room.AddCommand(create, deleteRoom, log, repo)
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
	var roomID, name, profile, goal, doneWhen, brief, sessionRepo, parent, size, composeName string
	var sameHost bool
	var gpu int
	start := &cobra.Command{Use: "start", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		r, err := getClient().SessionStart(cmd.Context(), contract.SessionStartRequest{Room: contract.RoomID(roomID), Name: name, Parent: contract.SessionID(parent), Repo: contract.RepoName(sessionRepo), Profile: contract.ProfileName(profile), Goal: goal, DoneWhen: doneWhen, Brief: brief, Size: contract.BoxSizeName(size), SameHost: sameHost, Compose: composeName, GPU: contract.GPUGiB(gpu)})
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
	start.Flags().StringVar(&doneWhen, "done-when", "", "When the goal is done")
	start.Flags().StringVar(&brief, "brief", "", "Brief")
	start.Flags().StringVar(&size, "size", "", "Box size for a lead: agent, small, medium or large")
	start.Flags().BoolVar(&sameHost, "same-host", false, "Place the box on its parent’s host")
	start.Flags().StringVar(&composeName, "compose", "", "Compose file, by name; without it the file named default, if any")
	start.Flags().IntVar(&gpu, "gpu", 0, "GiB of GPU memory the box asks for on a host card; 0 asks for none")
	closeCmd := &cobra.Command{Use: "close SESSION", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		closed, err := getClient().SessionClose(cmd.Context(), contract.SessionCloseRequest{Session: contract.SessionID(args[0])})
		if err == nil && len(closed.Handed) > 0 {
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Handed to %s: %s\n", closed.To, dynaroom.ResponsibilityNames(closed.Handed))
		}
		if err == nil && len(closed.Unheld) > 0 {
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Left with no holder, because %s is root and root holds no goals: %s. goal list --session %s shows them; goal done ends them, and settles their open items.\n", closed.To, dynaroom.GoalNames(closed.Unheld), closed.To)
		}
		return err
	}}
	restart := &cobra.Command{Use: "restart SESSION", Short: "Replace a failed session box", Long: "Replace a failed session box. The old volume and unpushed work are lost. If the old host is unavailable, its resources may remain there.", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		_, err := getClient().SessionRestart(cmd.Context(), contract.SessionRestartRequest{Session: contract.SessionID(args[0])})
		return err
	}}
	var refreshRoom string
	refresh := &cobra.Command{Use: "refresh [SESSION]", Short: "Mark a box to restart once nothing needs it", Long: "Mark a running box to restart once no turn, job or terminal needs it. It keeps its volume, unlike session restart, which discards it.", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if (len(args) == 1) == (refreshRoom != "") {
			return fmt.Errorf("give a session or --room")
		}
		request := contract.SessionRefreshRequest{Room: contract.RoomID(refreshRoom)}
		if len(args) == 1 {
			request.Session = contract.SessionID(args[0])
		}
		r, err := getClient().SessionRefresh(cmd.Context(), request)
		if err != nil {
			return err
		}
		if len(r.Marked) == 0 {
			_, err = fmt.Fprintln(cmd.OutOrStdout(), "No running box to mark.")
		}
		for _, id := range r.Marked {
			if _, err := fmt.Fprintln(cmd.OutOrStdout(), id); err != nil {
				return err
			}
		}
		return err
	}}
	refresh.Flags().StringVar(&refreshRoom, "room", "", "Mark every open session in this room")
	setProfile := &cobra.Command{Use: "set-profile SESSION PROFILE", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		_, err := getClient().SessionSetProfile(cmd.Context(), contract.SessionSetProfileRequest{Session: contract.SessionID(args[0]), Profile: contract.ProfileName(args[1])})
		return err
	}}
	setBranch := &cobra.Command{Use: "set-branch SESSION BRANCH", Short: "Make BRANCH the session's branch on its repository (owner socket only; renames nothing on GitHub)", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		_, err := getClient().SessionSetBranch(cmd.Context(), contract.SessionSetBranchRequest{Session: contract.SessionID(args[0]), Branch: args[1]})
		return err
	}}
	session.AddCommand(&cobra.Command{Use: "list ROOM", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		result, err := getClient().SessionList(cmd.Context(), contract.SessionListRequest{Room: contract.RoomID(args[0])})
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(result.Sessions)
	}}, &cobra.Command{Use: "responsibilities SESSION", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		result, err := getClient().SessionResponsibilities(cmd.Context(), contract.SessionResponsibilitiesRequest{Session: contract.SessionID(args[0])})
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
	}}, &cobra.Command{Use: "move SESSION NEW_PARENT", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		_, err := getClient().SessionMove(cmd.Context(), contract.SessionMoveRequest{Session: contract.SessionID(args[0]), NewParent: contract.SessionID(args[1])})
		return err
	}})
	var composeRoom, composeRepo string
	composeList := &cobra.Command{Use: "compose-list", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		r, err := getClient().ComposeList(cmd.Context(), contract.ComposeListRequest{Room: contract.RoomID(composeRoom), Repo: contract.RepoName(composeRepo)})
		if err != nil {
			return err
		}
		for _, file := range r.Files {
			line := file.Name + " (" + string(file.Level) + ")"
			if file.Problem != "" {
				line += ": " + file.Problem
			}
			if _, err := fmt.Fprintln(cmd.OutOrStdout(), line); err != nil {
				return err
			}
		}
		return nil
	}}
	composeList.Flags().StringVar(&composeRoom, "room", "", "Room ID")
	composeList.Flags().StringVar(&composeRepo, "repo", "", "Repository a child would use")
	session.AddCommand(start, closeCmd, restart, refresh, setProfile, setBranch, composeList)
	root.AddCommand(session)
	var postRoom, htmlPath, update string
	var allowNet bool
	var forward string
	var mentions, paths []string
	postCmd := &cobra.Command{Use: "post TEXT", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		c := getClient()
		upload := func(ctx context.Context, path string) (contract.Attachment, error) {
			return c.Upload(ctx, contract.RoomID(postRoom), path)
		}
		request, checks, err := (post.Preparer{Upload: upload, Render: visual.Preview}).Prepare(cmd.Context(), tools.PostInput{Room: contract.RoomID(postRoom), Text: args[0], Mentions: mentions, Paths: paths, HTML: htmlPath, AllowNet: allowNet, VisualUpdate: contract.EventID(update), Forward: contract.EventID(forward)})
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
	postCmd.Flags().StringVar(&forward, "forward", "", "Forward a post by ID, with TEXT as your note (may be empty)")
	postCmd.Flags().StringVar(&postRoom, "room", "", "Room ID")
	postCmd.Flags().StringSliceVar(&mentions, "mention", nil, "Session mentions")
	root.AddCommand(postCmd, visualCommand())
	root.AddCommand(goalCommand(getClient))
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
	var setHostName string
	deviceSet := &cobra.Command{Use: "set ID --host HOST", Args: cobra.ExactArgs(1), Short: "Name the host a paired device is on", Long: "Name the host a paired device is on, as in --host omarchy, so the owner's inputs from that device say which machine they come from. --host '' clears it.", RunE: func(cmd *cobra.Command, args []string) error {
		_, err := getClient().DeviceSetHost(cmd.Context(), contract.DeviceSetHostRequest{ID: args[0], Host: contract.HostName(setHostName)})
		return err
	}}
	deviceSet.Flags().StringVar(&setHostName, "host", "", "Host the device is on (empty clears it)")
	_ = deviceSet.MarkFlagRequired("host") // fails only for an undefined flag
	device.AddCommand(devicePair, deviceList, deviceRevoke, deviceSetScopes, deviceSet)
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
	var prRepo, prRoom, prSession string
	var prAll bool
	prNumber := func(arg string) (int, error) {
		number, err := strconv.Atoi(arg)
		if err != nil || number <= 0 {
			return 0, fmt.Errorf("%q is not a pull request number", arg)
		}
		return number, nil
	}
	printPR := func(cmd *cobra.Command, p contract.PrSummary) error {
		_, err := fmt.Fprintf(cmd.OutOrStdout(), "%s#%d\t%s\tci=%s\treview=%s\tmerge=%s\t%s\n", p.Repo, p.Number, p.State, p.CI, p.Review, p.Merge, p.URL)
		return err
	}
	prList := &cobra.Command{Use: "list", Args: cobra.NoArgs, Short: "List tracked pull requests", RunE: func(cmd *cobra.Command, _ []string) error {
		r, err := getClient().PrList(cmd.Context(), contract.PrListRequest{Room: contract.RoomID(prRoom), All: prAll})
		if err != nil {
			return err
		}
		for _, p := range r.PRs {
			if err := printPR(cmd, p); err != nil {
				return err
			}
		}
		return nil
	}}
	prList.Flags().StringVar(&prRoom, "room", "", "Room id (default: the session's room, or every room for the owner)")
	prList.Flags().BoolVar(&prAll, "all", false, "Also list merged and closed pull requests")
	prStatus := &cobra.Command{Use: "status NUMBER", Args: cobra.ExactArgs(1), Short: "Show a tracked pull request as of the last poll", RunE: func(cmd *cobra.Command, args []string) error {
		number, err := prNumber(args[0])
		if err != nil {
			return err
		}
		r, err := getClient().PrStatus(cmd.Context(), contract.PrStatusRequest{Repo: contract.RepoName(prRepo), Number: number})
		if err != nil {
			return err
		}
		return printPR(cmd, r.PR)
	}}
	prTrack := &cobra.Command{Use: "track NUMBER", Args: cobra.ExactArgs(1), Short: "Track a pull request and wake its session when it changes", RunE: func(cmd *cobra.Command, args []string) error {
		number, err := prNumber(args[0])
		if err != nil {
			return err
		}
		r, err := getClient().PrTrack(cmd.Context(), contract.PrTrackRequest{Repo: contract.RepoName(prRepo), Number: number, Room: contract.RoomID(prRoom), Session: contract.SessionID(prSession)})
		if err != nil {
			return err
		}
		return printPR(cmd, r.PR)
	}}
	prTrack.Flags().StringVar(&prRoom, "room", "", "Owner only: room whose root owns the pull request")
	prTrack.Flags().StringVar(&prSession, "session", "", "Owner only: session id that owns the pull request")
	prUntrack := &cobra.Command{Use: "untrack NUMBER", Args: cobra.ExactArgs(1), Short: "Stop tracking a pull request", RunE: func(cmd *cobra.Command, args []string) error {
		number, err := prNumber(args[0])
		if err != nil {
			return err
		}
		_, err = getClient().PrUntrack(cmd.Context(), contract.PrUntrackRequest{Repo: contract.RepoName(prRepo), Number: number})
		return err
	}}
	for _, command := range []*cobra.Command{prStatus, prTrack, prUntrack} {
		command.Flags().StringVar(&prRepo, "repo", "", "Base repository as owner/name (default: the session's repository)")
	}
	pr.AddCommand(prOpen, prList, prStatus, prTrack, prUntrack)
	root.AddCommand(pr)
	root.AddCommand(issueCommand(getClient))
}

// issueCommand holds the issue commands, which mirror pr list, status, track and untrack.
func issueCommand(getClient func() client) *cobra.Command {
	issue := &cobra.Command{Use: "issue"}
	var repo, room, session string
	var all bool
	number := func(arg string) (int, error) {
		n, err := strconv.Atoi(arg)
		if err != nil || n <= 0 {
			return 0, fmt.Errorf("%q is not an issue number", arg)
		}
		return n, nil
	}
	printIssue := func(cmd *cobra.Command, i contract.IssueSummary) error {
		_, err := fmt.Fprintf(cmd.OutOrStdout(), "%s#%d\t%s\tlabels=%s\tassignees=%s\t%s\n", i.Repo, i.Number, i.State, strings.Join(i.Labels, ","), strings.Join(i.Assignees, ","), i.URL)
		return err
	}
	list := &cobra.Command{Use: "list", Args: cobra.NoArgs, Short: "List tracked issues", RunE: func(cmd *cobra.Command, _ []string) error {
		r, err := getClient().IssueList(cmd.Context(), contract.IssueListRequest{Room: contract.RoomID(room), All: all})
		if err != nil {
			return err
		}
		for _, i := range r.Issues {
			if err := printIssue(cmd, i); err != nil {
				return err
			}
		}
		return nil
	}}
	list.Flags().StringVar(&room, "room", "", "Room id (default: the session's room, or every room for the owner)")
	list.Flags().BoolVar(&all, "all", false, "Also list closed issues")
	status := &cobra.Command{Use: "status NUMBER", Args: cobra.ExactArgs(1), Short: "Show a tracked issue as of the last change told", RunE: func(cmd *cobra.Command, args []string) error {
		n, err := number(args[0])
		if err != nil {
			return err
		}
		r, err := getClient().IssueStatus(cmd.Context(), contract.IssueStatusRequest{Repo: contract.RepoName(repo), Number: n})
		if err != nil {
			return err
		}
		return printIssue(cmd, r.Issue)
	}}
	track := &cobra.Command{Use: "track NUMBER", Args: cobra.ExactArgs(1), Short: "Track an issue and wake its session when it changes", RunE: func(cmd *cobra.Command, args []string) error {
		n, err := number(args[0])
		if err != nil {
			return err
		}
		r, err := getClient().IssueTrack(cmd.Context(), contract.IssueTrackRequest{Repo: contract.RepoName(repo), Number: n, Room: contract.RoomID(room), Session: contract.SessionID(session)})
		if err != nil {
			return err
		}
		return printIssue(cmd, r.Issue)
	}}
	track.Flags().StringVar(&room, "room", "", "Owner only: room whose root owns the issue")
	track.Flags().StringVar(&session, "session", "", "Owner only: session id that owns the issue")
	untrack := &cobra.Command{Use: "untrack NUMBER", Args: cobra.ExactArgs(1), Short: "Stop tracking an issue", RunE: func(cmd *cobra.Command, args []string) error {
		n, err := number(args[0])
		if err != nil {
			return err
		}
		_, err = getClient().IssueUntrack(cmd.Context(), contract.IssueUntrackRequest{Repo: contract.RepoName(repo), Number: n})
		return err
	}}
	for _, command := range []*cobra.Command{status, track, untrack} {
		command.Flags().StringVar(&repo, "repo", "", "Repository as owner/name (default: the session's repository)")
	}
	issue.AddCommand(list, status, track, untrack)
	return issue
}

// goalCommand holds the goal commands. Inside a box --room may stay empty: the engine uses the caller's room.
func goalCommand(getClient func() client) *cobra.Command {
	goal := &cobra.Command{Use: "goal", Short: "Work on goals and their checklists"}
	var roomID string
	goal.PersistentFlags().StringVar(&roomID, "room", "", "Room ID")
	printJSON := func(cmd *cobra.Command, value any) error {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(value)
	}
	var id, holder, text, doneWhen, clientID string
	set := &cobra.Command{Use: "set", Short: "Create a goal, change one, or hand it to another session", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		r, err := getClient().GoalSet(cmd.Context(), contract.GoalSetRequest{ClientID: clientID, Room: contract.RoomID(roomID), Goal: contract.GoalID(id), Session: contract.SessionID(holder), Text: text, DoneWhen: doneWhen})
		if err == nil {
			_, err = fmt.Fprintln(cmd.OutOrStdout(), r.Goal)
		}
		return err
	}}
	set.Flags().StringVar(&id, "goal", "", "Goal to change")
	set.Flags().StringVar(&holder, "session", "", "Session that holds it")
	set.Flags().StringVar(&text, "text", "", "What the goal is")
	set.Flags().StringVar(&doneWhen, "done-when", "", "When it is done")
	set.Flags().StringVar(&clientID, "client-id", "", "Retry key")
	var all bool
	var listHolder string
	list := &cobra.Command{Use: "list", Short: "List a room's goals with their items", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		r, err := getClient().GoalList(cmd.Context(), contract.GoalListRequest{Room: contract.RoomID(roomID), Session: contract.SessionID(listHolder), All: all})
		if err != nil {
			return err
		}
		return printJSON(cmd, r.Goals)
	}}
	list.Flags().StringVar(&listHolder, "session", "", "Only this session's goals")
	list.Flags().BoolVar(&all, "all", false, "Include ended goals")
	var item int
	var itemText, itemState string
	itemCmd := &cobra.Command{Use: "item GOAL", Short: "Add a checklist item, or change one with --item", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		r, err := getClient().GoalItem(cmd.Context(), contract.GoalItemRequest{Room: contract.RoomID(roomID), Goal: contract.GoalID(args[0]), Item: item, Text: itemText, State: contract.GoalItemState(itemState)})
		if err == nil {
			_, err = fmt.Fprintln(cmd.OutOrStdout(), r.Item)
		}
		return err
	}}
	itemCmd.Flags().IntVar(&item, "item", 0, "Item number to change")
	itemCmd.Flags().StringVar(&itemText, "text", "", "Item text")
	itemCmd.Flags().StringVar(&itemState, "state", "", "open, done or dropped")
	var drop bool
	done := &cobra.Command{Use: "done GOAL SUMMARY", Short: "End a goal", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		_, err := getClient().GoalDone(cmd.Context(), contract.GoalDoneRequest{Room: contract.RoomID(roomID), Goal: contract.GoalID(args[0]), Summary: args[1], Drop: drop})
		return err
	}}
	done.Flags().BoolVar(&drop, "drop", false, "Give the goal up instead")
	journal := &cobra.Command{Use: "journal GOAL [TEXT]", Short: "Add to a goal's journal and read its newest 5 lines", Args: cobra.RangeArgs(1, 2), RunE: func(cmd *cobra.Command, args []string) error {
		req := contract.GoalJournalRequest{Room: contract.RoomID(roomID), Goal: contract.GoalID(args[0])}
		if len(args) == 2 {
			req.Text = args[1]
		}
		r, err := getClient().GoalJournal(cmd.Context(), req)
		if err != nil {
			return err
		}
		return printJSON(cmd, r)
	}}
	goal.AddCommand(set, list, itemCmd, done, journal)
	return goal
}

// parseGitHubWrites reads --github-writes: `off`, or GROUPS@REPOS with one @, both comma-separated lists with no empty element. A
// group is a known one and a repository is owner/name or *; the engine checks the repositories against the room's.
func parseGitHubWrites(value string) (contract.GitHubWrites, error) {
	writes := contract.GitHubWrites{Repos: []contract.RepoName{}, Groups: []contract.GitHubWriteGroup{}}
	if value == "off" {
		return writes, nil
	}
	groups, repos, found := strings.Cut(value, "@")
	if !found || strings.Contains(repos, "@") {
		return writes, fmt.Errorf("--github-writes is GROUPS@REPOS with one @, such as issues,projects@owner/repo, or off; %q is not", value)
	}
	for _, group := range strings.Split(groups, ",") {
		if !slices.Contains(contract.AllGitHubWriteGroups, contract.GitHubWriteGroup(group)) {
			return writes, fmt.Errorf("--github-writes group %q is not one of %v", group, contract.AllGitHubWriteGroups)
		}
		writes.Groups = append(writes.Groups, contract.GitHubWriteGroup(group))
	}
	for _, repo := range strings.Split(repos, ",") {
		owner, name, slash := strings.Cut(repo, "/")
		if strings.ContainsAny(repo, " \t\r\n") || repo != contract.GitHubWriteAllRepos && (!slash || owner == "" || name == "" || strings.Contains(name, "/")) {
			return writes, fmt.Errorf("--github-writes repository %q is not owner/name or *", repo)
		}
		writes.Repos = append(writes.Repos, contract.RepoName(repo))
	}
	return writes, nil
}

func (c *socketClient) ServiceSet(ctx context.Context, q contract.ServiceSetRequest) (contract.ServiceSetResponse, error) {
	return call[contract.ServiceSetRequest, contract.ServiceSetResponse](ctx, c, contract.MethodServiceSet, q)
}

func (c *socketClient) ServiceList(ctx context.Context, q contract.ServiceListRequest) (contract.ServiceListResponse, error) {
	return call[contract.ServiceListRequest, contract.ServiceListResponse](ctx, c, contract.MethodServiceList, q)
}

func (c *socketClient) ServiceShow(ctx context.Context, q contract.ServiceRef) (contract.ServiceShowResponse, error) {
	return call[contract.ServiceRef, contract.ServiceShowResponse](ctx, c, contract.MethodServiceShow, q)
}

func (c *socketClient) ServiceLogs(ctx context.Context, q contract.ServiceLogsRequest) (contract.ServiceLogsResponse, error) {
	return call[contract.ServiceLogsRequest, contract.ServiceLogsResponse](ctx, c, contract.MethodServiceLogs, q)
}

func (c *socketClient) ServiceRestart(ctx context.Context, q contract.ServiceRef) (contract.ServiceActionResponse, error) {
	return call[contract.ServiceRef, contract.ServiceActionResponse](ctx, c, contract.MethodServiceRestart, q)
}

func (c *socketClient) ServiceStop(ctx context.Context, q contract.ServiceRef) (contract.ServiceActionResponse, error) {
	return call[contract.ServiceRef, contract.ServiceActionResponse](ctx, c, contract.MethodServiceStop, q)
}

func (c *socketClient) ServiceStart(ctx context.Context, q contract.ServiceRef) (contract.ServiceActionResponse, error) {
	return call[contract.ServiceRef, contract.ServiceActionResponse](ctx, c, contract.MethodServiceStart, q)
}

func (c *socketClient) ServiceUpdate(ctx context.Context, q contract.ServiceRef) (contract.ServiceActionResponse, error) {
	return call[contract.ServiceRef, contract.ServiceActionResponse](ctx, c, contract.MethodServiceUpdate, q)
}

func (c *socketClient) ServiceRollback(ctx context.Context, q contract.ServiceRef) (contract.ServiceActionResponse, error) {
	return call[contract.ServiceRef, contract.ServiceActionResponse](ctx, c, contract.MethodServiceRollback, q)
}

func (c *socketClient) ServiceRemove(ctx context.Context, q contract.ServiceRef) (contract.ServiceActionResponse, error) {
	return call[contract.ServiceRef, contract.ServiceActionResponse](ctx, c, contract.MethodServiceRemove, q)
}

func (c *socketClient) ServiceSecretSet(ctx context.Context, q contract.ServiceSecretSetRequest) (contract.ServiceSecretSetResponse, error) {
	return call[contract.ServiceSecretSetRequest, contract.ServiceSecretSetResponse](ctx, c, contract.MethodServiceSecretSet, q)
}
