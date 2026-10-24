package roomsvc

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"dyna-agent/internal/subagent"
)

const MaxBulkSessions = 512

// BulkSessionOptions applies one shared choice to each selected session.
// Close hands work to an ancestor; Force permits failed handoffs. Adopt
// moves each session under Parent.
type BulkSessionOptions struct {
	Sessions []string `json:"sessions"`
	Action   string   `json:"action"`
	Profile  string   `json:"profile,omitempty"`
	Model    string   `json:"model,omitempty"`
	Force    bool     `json:"force,omitempty"`
	Parent   string   `json:"parent,omitempty"`
}

type BulkSessionItem struct {
	Session  string                  `json:"session"`
	OK       bool                    `json:"ok"`
	Error    string                  `json:"error,omitempty"`
	Close    *subagent.CloseRecovery `json:"close,omitempty"`
	Member   *Member                 `json:"member,omitempty"`
	Transfer *subagent.TransferState `json:"transfer,omitempty"`
}

type BulkSessionResult struct {
	Action    string            `json:"action"`
	Results   []BulkSessionItem `json:"results"`
	Succeeded int               `json:"succeeded"`
	Failed    int               `json:"failed"`
}

// BulkSessionAction retains input order and reports independent failures.
// Resolve the whole selection first so aliases cannot act on one session twice.
// Existing single-session methods remain authoritative for write guards.
func (s *Service) BulkSessionAction(ctx context.Context, roomRef string, opts BulkSessionOptions) (BulkSessionResult, error) {
	out := BulkSessionResult{Action: opts.Action}
	if len(opts.Sessions) == 0 || len(opts.Sessions) > MaxBulkSessions {
		return out, fmt.Errorf("select between 1 and %d sessions per request", MaxBulkSessions)
	}
	switch opts.Action {
	case "stop", "close", "promote":
	case "adopt":
		parent, err := s.SessionInRoom(roomRef, opts.Parent)
		if err != nil {
			return out, fmt.Errorf("choose a parent session in this room: %w", err)
		}
		opts.Parent = parent.ID
	case "transfer":
		if strings.TrimSpace(opts.Profile) == "" {
			return out, fmt.Errorf("choose a model for the selected sessions")
		}
	default:
		return out, fmt.Errorf("unknown bulk session action %q", opts.Action)
	}
	if _, err := s.rooms().Resolve(roomRef); err != nil {
		return out, err
	}
	out.Results = make([]BulkSessionItem, len(opts.Sessions))
	seen := map[string]bool{}
	for i, ref := range opts.Sessions {
		out.Results[i].Session = ref
		info, err := s.SessionInRoom(roomRef, ref)
		if err != nil {
			out.Results[i].Error = err.Error()
			continue
		}
		if seen[info.ID] {
			return BulkSessionResult{Action: opts.Action}, fmt.Errorf("session %s is selected more than once", info.ID)
		}
		seen[info.ID] = true
		out.Results[i].Session = info.ID
	}
	jobs := make(chan int)
	var workers sync.WaitGroup
	for range min(8, len(opts.Sessions)) {
		workers.Go(func() {
			for i := range jobs {
				item := &out.Results[i]
				if item.Error != "" {
					continue
				}
				if err := ctx.Err(); err != nil {
					item.Error = err.Error()
					continue
				}
				var err error
				switch opts.Action {
				case "stop", "close", "promote", "adopt":
					var member Member
					switch opts.Action {
					case "stop":
						member, err = s.StopSession(ctx, roomRef, item.Session)
					case "close":
						member, err = s.closeSession(ctx, roomRef, item.Session, CloseOptions{Force: opts.Force})
					case "promote":
						_, err = s.Notify.Sessions.Promote(ctx, item.Session)
					case "adopt":
						_, err = s.Notify.Sessions.Adopt(ctx, item.Session, opts.Parent)
					}
					if err == nil && (opts.Action == "promote" || opts.Action == "adopt") {
						member, err = s.memberByID(item.Session)
					}
					if err == nil {
						item.Member = &member
					}
				case "transfer":
					var previous *subagent.TransferState
					previous, err = s.SessionTransfer(roomRef, item.Session)
					if err == nil {
						retry := previous != nil && (previous.Phase == "failed" || previous.Phase == "cancelled")
						var status subagent.TransferState
						status, err = s.Notify.Sessions.TransferContext(ctx, item.Session, subagent.TransferOptions{Profile: opts.Profile, Model: opts.Model, WaitSeconds: 30, Retry: retry})
						if err == nil {
							item.Transfer = &status
						}
					}
				}
				item.OK = err == nil
				if err != nil {
					item.Error = err.Error()
					var refusal *subagent.CloseRefusal
					if errors.As(err, &refusal) {
						recovery := refusal.CloseRecovery()
						item.Close = &recovery
					}
				}
			}
		})
	}
	for i := range out.Results {
		jobs <- i
	}
	close(jobs)
	workers.Wait()
	for _, item := range out.Results {
		if item.OK {
			out.Succeeded++
		} else {
			out.Failed++
		}
	}
	return out, nil
}
