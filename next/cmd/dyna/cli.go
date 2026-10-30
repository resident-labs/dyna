package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/resident-labs/dyna/contract"
	"github.com/spf13/cobra"
)

type client interface {
	HostAdd(context.Context, contract.HostAddRequest) (contract.HostAddResponse, error)
	RoomCreate(context.Context, contract.RoomCreateRequest) (contract.RoomCreateResponse, error)
	SessionStart(context.Context, contract.SessionStartRequest) (contract.SessionStartResponse, error)
	SessionClose(context.Context, contract.SessionCloseRequest) (contract.SessionCloseResponse, error)
	Post(context.Context, contract.PostRequest) (contract.PostResponse, error)
	EventsList(context.Context, contract.EventsListRequest) (contract.EventsListResponse, error)
	Events(context.Context, contract.RoomID, int64, func(contract.Event) error) error
}

type socketClient struct{ http *http.Client }

func newClient(socket string) *socketClient {
	return &socketClient{http: &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}}}
}

type requestType interface {
	contract.HostAddRequest | contract.RoomCreateRequest | contract.SessionStartRequest | contract.SessionCloseRequest | contract.PostRequest | contract.EventsListRequest
}
type responseType interface {
	contract.HostAddResponse | contract.RoomCreateResponse | contract.SessionStartResponse | contract.SessionCloseResponse | contract.PostResponse | contract.EventsListResponse
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
func (c *socketClient) HostAdd(ctx context.Context, q contract.HostAddRequest) (contract.HostAddResponse, error) {
	return call[contract.HostAddRequest, contract.HostAddResponse](ctx, c, contract.MethodHostAdd, q)
}
func (c *socketClient) RoomCreate(ctx context.Context, q contract.RoomCreateRequest) (contract.RoomCreateResponse, error) {
	return call[contract.RoomCreateRequest, contract.RoomCreateResponse](ctx, c, contract.MethodRoomCreate, q)
}
func (c *socketClient) SessionStart(ctx context.Context, q contract.SessionStartRequest) (contract.SessionStartResponse, error) {
	return call[contract.SessionStartRequest, contract.SessionStartResponse](ctx, c, contract.MethodSessionStart, q)
}
func (c *socketClient) SessionClose(ctx context.Context, q contract.SessionCloseRequest) (contract.SessionCloseResponse, error) {
	return call[contract.SessionCloseRequest, contract.SessionCloseResponse](ctx, c, contract.MethodSessionClose, q)
}
func (c *socketClient) Post(ctx context.Context, q contract.PostRequest) (contract.PostResponse, error) {
	return call[contract.PostRequest, contract.PostResponse](ctx, c, contract.MethodPost, q)
}
func (c *socketClient) EventsList(ctx context.Context, q contract.EventsListRequest) (contract.EventsListResponse, error) {
	return call[contract.EventsListRequest, contract.EventsListResponse](ctx, c, contract.MethodEventsList, q)
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
	create := &cobra.Command{Use: "create NAME", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		r, err := getClient().RoomCreate(cmd.Context(), contract.RoomCreateRequest{Name: args[0]})
		if err == nil {
			_, err = fmt.Fprintln(cmd.OutOrStdout(), r.Room)
		}
		return err
	}}
	var follow bool
	log := &cobra.Command{Use: "log ROOM", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		c := getClient()
		room := contract.RoomID(args[0])
		var after int64
		printEvent := func(e contract.Event) error {
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
	room.AddCommand(create, log)
	root.AddCommand(room)

	session := &cobra.Command{Use: "session"}
	var roomID, name, profile, goal, brief string
	start := &cobra.Command{Use: "start", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		r, err := getClient().SessionStart(cmd.Context(), contract.SessionStartRequest{Room: contract.RoomID(roomID), Name: name, Profile: contract.ProfileName(profile), Goal: goal, Brief: brief})
		if err == nil {
			_, err = fmt.Fprintln(cmd.OutOrStdout(), r.Session)
		}
		return err
	}}
	start.Flags().StringVar(&roomID, "room", "", "Room ID")
	start.Flags().StringVar(&name, "name", "", "Session name")
	start.Flags().StringVar(&profile, "profile", "", "Profile")
	start.Flags().StringVar(&goal, "goal", "", "Goal")
	start.Flags().StringVar(&brief, "brief", "", "Brief")
	closeCmd := &cobra.Command{Use: "close SESSION", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		_, err := getClient().SessionClose(cmd.Context(), contract.SessionCloseRequest{Session: contract.SessionID(args[0])})
		return err
	}}
	session.AddCommand(start, closeCmd)
	root.AddCommand(session)
	var postRoom string
	var mentions []string
	post := &cobra.Command{Use: "post TEXT", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		r, err := getClient().Post(cmd.Context(), contract.PostRequest{Room: contract.RoomID(postRoom), Text: args[0], Mentions: mentions})
		if err == nil {
			_, err = fmt.Fprintln(cmd.OutOrStdout(), r.Event)
		}
		return err
	}}
	post.Flags().StringVar(&postRoom, "room", "", "Room ID")
	post.Flags().StringSliceVar(&mentions, "mention", nil, "Session mentions")
	root.AddCommand(post)
}
