package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/resident-labs/dyna/box/post"
	"github.com/resident-labs/dyna/contract"
)

type client interface {
	AddonSetRootOnly(context.Context, contract.AddonSetRootOnlyRequest) (contract.AddonSetRootOnlyResponse, error)
	AddonSetEnabled(context.Context, contract.AddonSetEnabledRequest) (contract.AddonSetEnabledResponse, error)
	SessionSetAccount(context.Context, contract.SessionSetAccountRequest) (contract.SessionSetAccountResponse, error)
	AccountsSetLimits(context.Context, contract.AccountsSetLimitsRequest) (contract.AccountsSetLimitsResponse, error)
	AccountsQuota(context.Context, contract.AccountsQuotaRequest) (contract.AccountsQuotaResponse, error)
	AccountsList(context.Context, contract.AccountsListRequest) (contract.AccountsListResponse, error)
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

func call[Q, R any](ctx context.Context, c *socketClient, method contract.Method, input Q) (R, error) {
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
